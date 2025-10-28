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
	"github.com/your/module/internal/metrics"
	"github.com/your/module/internal/secrets"
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
	// GlobalDB is an optional handle to the hostapp's main localdb so the
	// registry can read global runtime settings (for example, global
	// Tailscale config) and use them as a fallback when per-cluster settings
	// are not present.
	GlobalDB *localdb.DB
}

// Registry manages per-cluster Instances.
type Registry struct {
	mu      sync.RWMutex
	opts    Options
	items   map[string]*Instance
	created map[string]time.Time
	// backoff/attempt tracking for connector creation to avoid thundering
	// retries when control plane or credentials are missing.
	backoffAttempts map[string]int
	backoffLast     map[string]time.Time
}

func NewRegistry(opts Options) *Registry {
	r := &Registry{opts: opts, items: map[string]*Instance{}, created: map[string]time.Time{}, backoffAttempts: map[string]int{}, backoffLast: map[string]time.Time{}}
	log.Printf("registry: NewRegistry globalDB present=%t stateDir=%q", opts.GlobalDB != nil, opts.StateDir)
	// Best-effort: seed registry with any existing per-cluster state directories
	// found under the configured state dir so the reconciler can create TS
	// connectors for clusters that were persisted on disk but not currently
	// represented in memory. This avoids requiring an external trigger to
	// materialize connectors for already-known clusters.
	if opts.StateDir != "" {
		if ents, err := os.ReadDir(opts.StateDir); err == nil {
			for _, e := range ents {
				if !e.IsDir() {
					continue
				}
				id := e.Name()
				// try to open per-cluster localdb
				p := filepath.Join(opts.StateDir, id)
				if db, err := localdb.Open(p); err == nil {
					// ensure common buckets
					_ = db.EnsureBuckets("settings", "cluster-settings", "credentials", "devices", "jobs", "joblogs", "audit")
					inst := &Instance{id: id, stateDir: p, DB: db}
					r.items[id] = inst
					r.created[id] = time.Now()
					log.Printf("registry: seeded instance id=%s dir=%s", id, p)
				}
			}
		}
	}
	// start background reconciler to create connectors for instances that
	// didn't have credentials at creation time. This is best-effort and
	// keeps hostapp responsive to per-cluster credential writes.
	go r.connectorReconciler()
	// start a background health reporter that logs degraded connectors so
	// operators can be alerted. Lightweight and best-effort.
	go r.healthReporter()
	return r
}

// connectorReconciler periodically iterates over known instances and attempts
// to create a TS connector for those that don't yet have one.
func (r *Registry) connectorReconciler() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		r.mu.RLock()
		ids := make([]string, 0, len(r.items))
		for id := range r.items {
			ids = append(ids, id)
		}
		r.mu.RUnlock()
		for _, id := range ids {
			r.mu.RLock()
			inst := r.items[id]
			r.mu.RUnlock()
			if inst == nil {
				continue
			}
			inst.mu.Lock()
			has := inst.TS != nil
			inst.mu.Unlock()
			if !has {
				// backoff logic: consult per-id attempt counts and last attempt
				// time to avoid hot-looping retries against an unavailable
				// control plane or missing credentials.
				now := time.Now()
				attempts := r.backoffAttempts[id]
				last := r.backoffLast[id]
				// compute delay: base 5s * 2^attempts, capped at 5m
				base := 5 * time.Second
				maxDelay := 5 * time.Minute
				delay := base * (1 << attempts)
				if delay > maxDelay {
					delay = maxDelay
				}
				if last.IsZero() || now.After(last.Add(delay)) {
					if err := r.createConnectorForInstance(inst); err != nil {
						r.backoffAttempts[id] = attempts + 1
						r.backoffLast[id] = now
						log.Printf("connector: reconcile create failed cluster=%s attempt=%d next_delay=%v err=%v", id, r.backoffAttempts[id], delay, err)
					} else {
						// reset backoff on success
						delete(r.backoffAttempts, id)
						delete(r.backoffLast, id)
						log.Printf("connector: reconcile create succeeded cluster=%s", id)
					}
				}
			}
		}
	}
}

// healthReporter periodically checks connectors and logs degraded/starting
// states so operators can wire alerts to host logs.
func (r *Registry) healthReporter() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		r.mu.RLock()
		ids := make([]string, 0, len(r.items))
		for id := range r.items {
			ids = append(ids, id)
		}
		r.mu.RUnlock()
		for _, id := range ids {
			r.mu.RLock()
			inst := r.items[id]
			r.mu.RUnlock()
			if inst == nil {
				continue
			}
			// check connector health with a short timeout
			if inst.TS == nil {
				log.Printf("connector: health check skipped cluster=%s no connector", id)
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			status, det := inst.TS.HealthWithRetry(ctx, 1*time.Second, 1, 200*time.Millisecond)
			cancel()
			if status != "ok" {
				// emit a cluster-scoped metric for health alerts so external
				// monitoring can pick up degraded connector state
				metrics.IncOpCluster(id, "hostapp", "connector", "health_alert", 1)
				log.Printf("connector: health alert cluster=%s status=%s details=%v", id, status, det)
			}
		}
	}
}

// createConnectorForInstance attempts to create and start a TS connector for an
// existing instance. It follows the same credential selection and normalization
// logic as the creation path in Get(). This is a best-effort helper used to
// pick up credentials written after an Instance was first created.
func (r *Registry) createConnectorForInstance(inst *Instance) error {
	if inst == nil || inst.DB == nil {
		return fmt.Errorf("instance or instance.DB is nil")
	}
	id := inst.id
	sm := settings.Manager{DB: inst.DB}
	var cs settings.Cluster
	_ = sm.GetCluster(id, &cs)
	var cred map[string]any
	if err := inst.DB.Get("credentials", "cl:"+id+":ts_client_auth", &cred); err != nil {
		log.Printf("connector: inst.DB.Get credentials missing/err cluster=%s err=%v", id, err)
	} else {
		log.Printf("connector: inst.DB.Get credentials cluster=%s raw=%v", id, cred)
	}
	clientKey := ""
	clientKeySource := ""
	if len(cred) > 0 {
		if v, ok := cred["value"].(string); ok {
			clientKey = v
			clientKeySource = "credentials"
			// If credential was stored encrypted, attempt decryption using
			// GUILDNET_MASTER_KEY so we pass the raw key to tsnet.
			if enc, ok2 := cred["encrypted"].(bool); ok2 && enc {
				if mk := strings.TrimSpace(os.Getenv("GUILDNET_MASTER_KEY")); mk != "" {
					if sec, err := secrets.New(mk); err == nil {
						if dec, derr := sec.Decrypt(clientKey); derr == nil {
							clientKey = dec
						} else {
							log.Printf("connector: failed to decrypt stored credential for cluster=%s err=%v", id, derr)
						}
					}
				}
			}
		}
	}
	if strings.TrimSpace(clientKey) != "" && !strings.HasPrefix(strings.TrimSpace(clientKey), "tskey-") {
		clientKey = "tskey-" + strings.TrimSpace(clientKey)
	}
	// If a per-cluster LoginServer isn't configured, fall back to the
	// hostapp's global Tailscale settings. We do this regardless of whether
	// a per-cluster clientKey is present because tsnet still needs a
	// loginServer URL to contact the control plane. However, only adopt
	// the global preauth key when no per-cluster clientKey is present.
	if strings.TrimSpace(cs.TSLoginServer) == "" && r.opts.GlobalDB != nil {
		var g settings.Tailscale
		err := settings.Manager{DB: r.opts.GlobalDB}.GetTailscale(&g)
		log.Printf("connector: globalDB present=%t getTailscaleErr=%v globalLogin=%q", r.opts.GlobalDB != nil, err, g.LoginServer)
		if strings.TrimSpace(g.LoginServer) != "" {
			cs.TSLoginServer = g.LoginServer
		}
		if strings.TrimSpace(g.PreauthKey) != "" && strings.TrimSpace(clientKey) == "" {
			clientKey = g.PreauthKey
			clientKeySource = "global"
		}
	}
	if strings.TrimSpace(cs.TSLoginServer) == "" && strings.TrimSpace(clientKey) == "" {
		return fmt.Errorf("no login server or client key available for cluster %s", id)
	}
	state := ""
	if h, err := os.UserHomeDir(); err == nil {
		state = filepath.Join(h, ".guildnet", "tsnet", "cluster-"+id)
	}
	c, err := connector.New(connector.Config{ClusterID: id, LoginServer: cs.TSLoginServer, ClientAuthKey: clientKey, StateDir: state})
	if err != nil {
		return err
	}
	if err := c.Start(context.Background()); err != nil {
		return err
	}
	inst.mu.Lock()
	inst.TS = c
	inst.mu.Unlock()
	log.Printf("connector: created for existing instance cluster=%s clientKeySet=%t clientKeySource=%q stateDir=%s", id, strings.TrimSpace(clientKey) != "", clientKeySource, state)
	return nil
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
		// If an instance already exists and a TS connector has not been
		// created yet, attempt to create one here so freshly-written per-cluster
		// credentials (or global fallback) can be picked up without requiring
		// a full hostapp restart.
		if inst != nil {
			inst.mu.Lock()
			hasTS := inst.TS != nil
			inst.mu.Unlock()
			if !hasTS {
				r.mu.RUnlock()
				// Try to create connector now using the existing instance's DB
				// and the same logic as in the creation path below.
				if err := r.createConnectorForInstance(inst); err != nil {
					// Non-fatal: log and continue returning the instance. The
					// connector creation will be retried on future Get calls.
					log.Printf("connector: deferred create failed for cluster=%s err=%v", id, err)
				}
				return inst, nil
			}
		}
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
		if err := db.Get("credentials", "cl:"+id+":ts_client_auth", &cred); err != nil {
			log.Printf("connector: db.Get credentials missing/err cluster=%s err=%v", id, err)
		} else {
			log.Printf("connector: db.Get credentials cluster=%s raw=%v", id, cred)
		}
		clientKey := ""
		clientKeySource := ""
		if len(cred) > 0 {
			if v, ok := cred["value"].(string); ok {
				clientKey = v
				clientKeySource = "credentials"
			}
		}
		// If credential was stored encrypted, attempt decryption using
		// GUILDNET_MASTER_KEY so we pass the raw key to tsnet when starting
		// the connector. This mirrors the reconciler path's behavior.
		if len(cred) > 0 {
			if enc, ok2 := cred["encrypted"].(bool); ok2 && enc {
				if mk := strings.TrimSpace(os.Getenv("GUILDNET_MASTER_KEY")); mk != "" {
					if sec, err := secrets.New(mk); err == nil {
						if dec, derr := sec.Decrypt(clientKey); derr == nil {
							clientKey = dec
						} else {
							log.Printf("connector: failed to decrypt stored credential for cluster=%s err=%v", id, derr)
						}
					}
				}
			}
		}
		// Some stored preauth keys may be missing the expected 'tskey-' prefix.
		// Normalize here so tsnet.AuthKey receives the canonical form regardless
		// of whether the key came from per-cluster credentials or global settings.
		if strings.TrimSpace(clientKey) != "" && !strings.HasPrefix(strings.TrimSpace(clientKey), "tskey-") {
			log.Printf("connector: normalizing preauth key for cluster=%s (adding tskey- prefix)", id)
			clientKey = "tskey-" + strings.TrimSpace(clientKey)
		}

		// Only attempt to create a per-cluster TS connector when we have either a per-cluster
		// login server configured or a stored client auth key. If both are empty, try to
		// fall back to global tailscale settings (held in the hostapp's global DB) so a
		// single shared tailscale control plane can be used when per-cluster settings
		// were not explicitly configured.
		// If a cluster-specific login server isn't configured, prefer the
		// global hostapp Tailscale login server so the connector can contact
		// the control plane. We still only pull the global preauth key when
		// no per-cluster clientKey exists.
		if strings.TrimSpace(cs.TSLoginServer) == "" && r.opts.GlobalDB != nil {
			var g settings.Tailscale
			err := settings.Manager{DB: r.opts.GlobalDB}.GetTailscale(&g)
			log.Printf("connector: (reconciler) globalDB present=%t getTailscaleErr=%v globalLogin=%q", r.opts.GlobalDB != nil, err, g.LoginServer)
			if strings.TrimSpace(g.LoginServer) != "" {
				cs.TSLoginServer = g.LoginServer
			}
			if strings.TrimSpace(g.PreauthKey) != "" && strings.TrimSpace(clientKey) == "" {
				clientKey = g.PreauthKey
				clientKeySource = "global"
			}
		}
		if strings.TrimSpace(cs.TSLoginServer) != "" || strings.TrimSpace(clientKey) != "" {
			// Default state dir under ~/.guildnet/tsnet/cluster-<id>
			state := ""
			if h, err := os.UserHomeDir(); err == nil {
				state = filepath.Join(h, ".guildnet", "tsnet", "cluster-"+id)
			}
			// Debug: log the effective clientKey (masked) and its source so we can trace
			// why an unexpected key might be used when creating the connector.
			maskedDebug := ""
			ck := strings.TrimSpace(clientKey)
			if ck != "" {
				if len(ck) > 12 {
					maskedDebug = ck[:12] + "..."
				} else {
					maskedDebug = ck
				}
			}
			log.Printf("connector: debug before New cluster=%s clientKey=%q clientKeySource=%q login=%q stateDir=%s", id, maskedDebug, clientKeySource, cs.TSLoginServer, state)
			c, err := connector.New(connector.Config{ClusterID: id, LoginServer: cs.TSLoginServer, ClientAuthKey: clientKey, StateDir: state})
			if err != nil {
				log.Printf("connector: New failed for cluster=%s login=%q clientKeySet=%t stateDir=%s err=%v", id, cs.TSLoginServer, strings.TrimSpace(clientKey) != "", state, err)
			} else {
				// Mask clientKey in logs for safety: show only first 8 chars if present.
				masked := ""
				if strings.TrimSpace(clientKey) != "" {
					ck := strings.TrimSpace(clientKey)
					if len(ck) > 8 {
						masked = ck[:8] + "..."
					} else {
						masked = ck
					}
				}
				log.Printf("connector: created for cluster=%s login=%q clientKeySet=%t clientKeyMask=%q clientKeySource=%q stateDir=%s", id, cs.TSLoginServer, strings.TrimSpace(clientKey) != "", masked, clientKeySource, state)
				// Best-effort start (non-blocking)
				if err := c.Start(context.Background()); err != nil {
					log.Printf("connector: Start failed for cluster=%s err=%v", id, err)
				} else {
					log.Printf("connector: Start initiated for cluster=%s", id)
				}
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
