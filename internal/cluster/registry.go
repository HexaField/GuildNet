package cluster

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/your/module/internal/db"
	"github.com/your/module/internal/httpx"
	"github.com/your/module/internal/k8s"
	"github.com/your/module/internal/localdb"
	"github.com/your/module/internal/settings"
	"github.com/your/module/internal/ts/connector"
	"k8s.io/client-go/dynamic"
)

// ID represents a cluster identifier used as the registry key.
type ID string

// NormalID normalizes a cluster id for filesystem-safe usage.
func NormalID(id string) string {
	return sanitizeID(id)
}

// Instance encapsulates per-cluster scoped dependencies and state.
type Instance struct {
	id       string
	stateDir string

	// Per-cluster components
	DB  *localdb.DB
	K8s *k8s.Client
	Dyn dynamic.Interface
	PF  *k8s.PortForwardManager
	TS  *connector.Connector
	// Optional per-cluster RethinkDB connector (lazy-initialized interface)
	RDB httpx.DBManager

	// capture of connectForK8s for race-free reconnects
	rdbDial func(ctx context.Context, kc *k8s.Client, addr, user, pass string) (httpx.DBManager, error)

	// capture of ping interval to avoid races on global during tests
	rdbPingInterval time.Duration

	mu  sync.Mutex
	ctx context.Context
	wg  sync.WaitGroup

	// teardown coordination
	cancel func()
}

// Status represents lightweight lifecycle status.
type Status struct {
	ID        string
	Started   bool
	StateDir  string
	HasDB     bool
	HasK8s    bool
	Forwards  int
	CreatedAt time.Time
}

// ExtendedStatus includes runtime capacity and role info for UI consumption.
type ExtendedStatus struct {
	Status
	// Optional human-friendly name for the device
	Name string `json:"name,omitempty"`
	// Tailnet IPs assigned to the device (if using tsnet/tailscale)
	TailnetIPs []string `json:"tailnetIPs,omitempty"`
	// Whether this device hosts a K8s client and therefore can support cluster workloads
	SupportsCluster bool `json:"supportsCluster"`
	// Capacity metrics (best-effort / reported by device)
	CPUMilli  int64 `json:"cpuMilli,omitempty"`
	MemoryMB  int64 `json:"memoryMB,omitempty"`
	StorageMB int64 `json:"storageMB,omitempty"`
	VRAMMB    int64 `json:"vramMB,omitempty"`
	// LastSeen is a best-effort timestamp when the instance was observed
	LastSeen time.Time `json:"lastSeen,omitempty"`
}

// Resolver provides cluster-specific materials needed to start an Instance.
type Resolver interface {
	// KubeconfigYAML should return a kubeconfig for the cluster or empty when unknown.
	KubeconfigYAML(clusterID string) (string, error)
}

// Options for the registry.
type Options struct {
	StateDir string
	Resolver Resolver
}

// Registry manages per-cluster Instances.
type Registry struct {
	mu      sync.RWMutex
	opts    Options
	items   map[string]*Instance
	created map[string]time.Time
}

func NewRegistry(opts Options) *Registry {
	return &Registry{opts: opts, items: map[string]*Instance{}, created: map[string]time.Time{}}
}

// hooks for testing/override
var (
	// connectForK8s returns an httpx.DBManager; tests can override to inject fakes.
	connectForK8s = func(ctx context.Context, kc *k8s.Client, addr, user, pass string) (httpx.DBManager, error) {
		m, err := db.ConnectForK8s(ctx, kc, addr, user, pass)
		return m, err
	}
	rdbPingInterval = 5 * time.Second
)

// Get returns an existing instance or creates a new one.
func (r *Registry) Get(ctx context.Context, clusterID string) (*Instance, error) {
	id := NormalID(clusterID)
	r.mu.RLock()
	if inst, ok := r.items[id]; ok {
		r.mu.RUnlock()
		return inst, nil
	}
	r.mu.RUnlock()

	// Create new instance
	r.mu.Lock()
	defer r.mu.Unlock()
	if inst, ok := r.items[id]; ok {
		return inst, nil
	}
	if r.opts.Resolver == nil {
		return nil, fmt.Errorf("cluster resolver not configured")
	}
	kc, err := r.opts.Resolver.KubeconfigYAML(id)
	if err != nil || kc == "" {
		return nil, fmt.Errorf("kubeconfig not found for cluster %s: %v", id, err)
	}
	// Per-cluster DB path
	stateDir := r.opts.StateDir
	if stateDir == "" {
		stateDir = "."
	}
	clDir := filepath.Join(stateDir, id)
	db, err := localdb.Open(clDir)
	if err != nil {
		return nil, fmt.Errorf("open cluster db: %w", err)
	}
	// Ensure common buckets per cluster
	_ = db.EnsureBuckets("settings", "cluster-settings", "credentials", "jobs", "joblogs", "audit")

	// Optional tsnet connector per cluster
	var conn *connector.Connector
	{
		sm := settings.Manager{DB: db}
		var cs settings.Cluster
		_ = sm.GetCluster(id, &cs)
		// Read client auth key from credentials bucket
		var cred map[string]any
		_ = db.Get("credentials", "cl:"+id+":ts_client_auth", &cred)
		clientKey := ""
		if len(cred) > 0 {
			if v, ok := cred["value"].(string); ok {
				clientKey = v
			}
		}
		if strings.TrimSpace(cs.TSLoginServer) != "" || strings.TrimSpace(clientKey) != "" {
			// Default state dir under ~/.guildnet/tsnet/cluster-<id>
			state := ""
			if h, err := os.UserHomeDir(); err == nil {
				state = filepath.Join(h, ".guildnet", "tsnet", "cluster-"+id)
			}
			if c, err := connector.New(connector.Config{ClusterID: id, LoginServer: cs.TSLoginServer, ClientAuthKey: clientKey, StateDir: state}); err == nil {
				// Best-effort start (non-blocking)
				_ = c.Start(context.Background())
				conn = c
			}
		}
	}
	// Build k8s client, using ts Dial if connector exists and has been started.
	var dial func(context.Context, string, string) (net.Conn, error)
	if conn != nil {
		dial = conn.DialContext
	}
	kcli, err := k8s.NewFromKubeconfig(ctx, kc, struct {
		APIProxyURL string
		ForceHTTP   bool
		Dial        func(ctx context.Context, network, addr string) (net.Conn, error)
	}{APIProxyURL: "", ForceHTTP: false, Dial: dial})
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("k8s client: %w", err)
	}
	// build dynamic client for CRD access and cache it on the instance
	var dynClient dynamic.Interface
	if d, derr := dynamic.NewForConfig(kcli.Config()); derr == nil {
		dynClient = d
	}
	inst := &Instance{id: id, stateDir: clDir, DB: db, K8s: kcli, Dyn: dynClient, TS: conn}
	// Capture current dialer to avoid races on global variable in tests
	inst.rdbDial = connectForK8s
	// Capture ping interval to avoid races on global variable in tests
	inst.rdbPingInterval = rdbPingInterval
	inst.PF = k8s.NewPortForwardManagerWithCluster(kcli.Config(), id, "")
	// tie to context
	cctx, cancel := context.WithCancel(context.Background())
	inst.cancel = cancel
	inst.ctx = cctx
	// existing placeholder goroutine (will be used by reconnection worker)
	// start reconciliation worker for pending DeviceParticipant upserts
	inst.wg.Add(1)
	go func() {
		defer inst.wg.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-cctx.Done():
				return
			case <-ticker.C:
				// attempt to reconcile pending deviceparticipants
				if inst.DB == nil {
					continue
				}
				var pending []map[string]any
				if err := inst.DB.List("pending_deviceparticipants", &pending); err != nil || len(pending) == 0 {
					continue
				}
				// If dynamic client not present, skip until available
				if inst.Dyn == nil {
					continue
				}
				for _, item := range pending {
					// each item expected to include id
					idVal, ok := item["id"].(string)
					if !ok || idVal == "" {
						continue
					}
					spec := map[string]any{}
					if v, ok := item["name"]; ok {
						spec["name"] = v
					}
					if v, ok := item["tailnetIPs"]; ok {
						spec["tailnetIPs"] = v
					}
					if v, ok := item["hostappVersion"]; ok {
						spec["hostappVersion"] = v
					}
					status := map[string]any{"state": "online"}
					if v, ok := item["lastSeen"]; ok {
						status["lastSeen"] = fmt.Sprint(v)
					}
					if _, err := k8s.CreateOrUpdateDeviceParticipant(context.Background(), inst.Dyn, "guildnet-system", idVal, spec, status); err == nil {
						// success -> delete pending
						_ = inst.DB.Delete("pending_deviceparticipants", idVal)
					} else {
						log.Printf("reconcile deviceparticipant failed id=%s err=%v", idVal, err)
					}
				}
			}
		}
	}()
	r.items[id] = inst
	r.created[id] = time.Now()
	log.Printf("cluster: start id=%s dir=%s", id, clDir)
	return inst, nil
}

// RDBPresent returns true if the registry has an initialized RethinkDB manager
// for the given cluster id.
func (r *Registry) RDBPresent(clusterID string) (bool, error) {
	id := NormalID(clusterID)
	r.mu.RLock()
	inst, ok := r.items[id]
	r.mu.RUnlock()
	if !ok || inst == nil {
		return false, fmt.Errorf("instance not found")
	}
	inst.mu.Lock()
	present := inst.RDB != nil
	inst.mu.Unlock()
	return present, nil
}

// EnsureRDB lazily initializes the per-cluster RethinkDB manager using
// the cluster's K8s client for in-cluster service discovery. addrOverride/user/pass
// are optional but the code enforces that RethinkDB must be reachable inside the
// Kubernetes cluster (no local loopback or external dev mode is permitted).
func (inst *Instance) EnsureRDB(ctx context.Context, addrOverride, user, pass string) error {
	if inst == nil || inst.K8s == nil {
		return fmt.Errorf("instance or k8s client is nil")
	}
	inst.mu.Lock()
	if inst.RDB != nil {
		inst.mu.Unlock()
		return nil
	}
	inst.mu.Unlock()

	// retry/backoff attempts
	attempts := 5
	delay := 100 * time.Millisecond
	var lastErr error
	for i := 0; i < attempts; i++ {
		mgrIface, err := inst.rdbDial(ctx, inst.K8s, addrOverride, user, pass)
		if err == nil && mgrIface != nil {
			inst.mu.Lock()
			inst.RDB = mgrIface
			inst.mu.Unlock()
			// start reconnection worker
			inst.wg.Add(1)
			go inst.rdbMonitor()
			return nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
			delay = delay * 2
		}
	}
	return fmt.Errorf("connect rethinkdb failed after retries: %w", lastErr)
}

// reconnect & health monitor: pings periodically and attempts reconnection on transient failures.
func (inst *Instance) rdbMonitor() {
	defer inst.wg.Done()
	// use instance-captured interval to avoid races with global during tests
	interval := inst.rdbPingInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-inst.ctx.Done():
			return
		case <-ticker.C:
			inst.mu.Lock()
			mgr := inst.RDB
			inst.mu.Unlock()
			if mgr == nil {
				// try to establish
				if err := inst.EnsureRDB(inst.ctx, "", "", ""); err != nil {
					// log and continue
					log.Printf("cluster: rdb monitor ensure failed id=%s err=%v", inst.id, err)
				}
				continue
			}
			if err := mgr.Ping(inst.ctx); err != nil {
				cls := db.ClassifyError(err)
				log.Printf("cluster: rdb ping failed id=%s class=%s err=%v", inst.id, cls, err)
				if cls == "transient" {
					// attempt reconnect with short backoff
					attempts := 3
					delay := 200 * time.Millisecond
					for i := 0; i < attempts; i++ {
						if inst.ctx.Err() != nil {
							return
						}
						newMgrIface, err := inst.rdbDial(inst.ctx, inst.K8s, "", "", "")
						if err == nil && newMgrIface != nil {
							inst.mu.Lock()
							// close old if closable
							if closer, ok := inst.RDB.(interface{ Close() error }); ok {
								_ = closer.Close()
							}
							inst.RDB = newMgrIface
							inst.mu.Unlock()
							break
						}
						time.Sleep(delay)
						delay = delay * 2
					}
				}
			}
		}
	}
}

// Close tears down an instance and removes it from registry.
func (r *Registry) Close(clusterID string) error {
	id := NormalID(clusterID)
	r.mu.Lock()
	defer r.mu.Unlock()
	inst, ok := r.items[id]
	if !ok {
		return nil
	}
	if inst.cancel != nil {
		inst.cancel()
	}
	if inst.DB != nil {
		_ = inst.DB.Close()
	}
	if inst.RDB != nil {
		if closer, ok := inst.RDB.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}
	// wait for background goroutines (monitor, workers) to exit
	if inst.wg != (sync.WaitGroup{}) {
		inst.wg.Wait()
	}
	// No explicit Close for K8s client; GC handles it. Port forwards will die with cancel.
	delete(r.items, id)
	delete(r.created, id)
	log.Printf("cluster: stop id=%s", id)
	return nil
}

// List returns current instance IDs.
func (r *Registry) List() []Status {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Status, 0, len(r.items))
	for id, inst := range r.items {
		s := Status{ID: id, Started: true, StateDir: inst.stateDir, HasDB: inst.DB != nil, HasK8s: inst.K8s != nil, CreatedAt: r.created[id]}
		out = append(out, s)
	}
	return out
}

func sanitizeID(s string) string {
	// keep simple: lowercase alnum and dash
	b := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b = append(b, r)
		case r >= 'A' && r <= 'Z':
			b = append(b, r+('a'-'A'))
		case r >= '0' && r <= '9':
			b = append(b, r)
		case r == '-' || r == '_' || r == '.':
			b = append(b, '-')
		default:
			// skip
		}
	}
	res := string(b)
	if res == "" {
		res = "default"
	}
	return res
}
