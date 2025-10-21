package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	crlog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	proxycontroller "github.com/your/module/internal/controller/proxy"
	"github.com/your/module/internal/httpx"
	"github.com/your/module/internal/k8s"
	"github.com/your/module/internal/metrics"
	"github.com/your/module/internal/model"
	"github.com/your/module/internal/proxy"
	"github.com/your/module/internal/settings"

	//"github.com/your/module/internal/store"
	"github.com/your/module/internal/store"
	"github.com/your/module/internal/ts"
	"github.com/your/module/pkg/config"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	// no direct transport import; use rest.TransportFor

	apiv1alpha1 "github.com/your/module/api/v1alpha1"
	"github.com/your/module/internal/operator"
	"github.com/your/module/internal/permission"
	corev1 "k8s.io/api/core/v1"

	// New imports
	"github.com/your/module/internal/api"
	"github.com/your/module/internal/cluster"

	hostapppkg "github.com/your/module/internal/hostapp"
	hostappapi "github.com/your/module/internal/hostapp/api"

	"github.com/your/module/internal/localdb"
	"github.com/your/module/internal/secrets"

	federation "github.com/your/module/internal/controller/federation"
)

// readMemMB attempts to read total memory in MB on Linux via /proc/meminfo.
func readMemMB() int64 {
	// best-effort only; return 0 when unknown
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "MemTotal:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					// value in kB
					if v, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
						return v / 1024
					}
				}
			}
		}
	}
	return 0
}

// readStorageMB returns available storage on the given path in MB (best-effort).
func readStorageMB(path string) int64 {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err == nil {
		// available blocks * block size
		avail := int64(stat.Bavail) * int64(stat.Bsize)
		return avail / (1024 * 1024)
	}
	return 0
}

// kubeconfigResolver implements cluster.Resolver backed by localdb credentials.
type kubeconfigResolver struct {
	DB  *localdb.DB
	Sec *secrets.Manager
}

func (r kubeconfigResolver) KubeconfigYAML(clusterID string) (string, error) {
	if r.DB == nil {
		return "", fmt.Errorf("no db")
	}
	var cred map[string]any
	key := fmt.Sprintf("cl:%s:kubeconfig", clusterID)
	if err := r.DB.Get("credentials", key, &cred); err != nil {
		return "", err
	}
	val := fmt.Sprint(cred["value"])
	if enc, _ := cred["encrypted"].(bool); enc {
		if r.Sec == nil {
			return "", fmt.Errorf("encrypted but no secrets manager")
		}
		s, err := r.Sec.Decrypt(val)
		if err != nil {
			return "", err
		}
		return s, nil
	}
	return val, nil
}

// startOperator boots a controller-runtime manager that reconciles Workspace CRDs.
func startOperator(ctx context.Context, restCfg *rest.Config, reg *cluster.Registry) error {
	scheme := k8sruntime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = apiv1alpha1.AddToScheme(scheme)
	// configure controller-runtime logger (dev mode)
	crlog.SetLogger(zap.New(zap.UseDevMode(true)))
	opts := ctrl.Options{Scheme: scheme}
	// Disable metrics and health probe servers to avoid port conflicts in embedded mode.
	opts.Metrics.BindAddress = "0"
	opts.HealthProbeBindAddress = "0"
	// Enable leader election so multiple operators (devices) safely coordinate.
	// Use a stable ID and prefer the guildnet-system namespace when available.
	leNS := os.Getenv("K8S_NAMESPACE")
	if strings.TrimSpace(leNS) == "" {
		leNS = "guildnet-system"
	}
	opts.LeaderElection = true
	opts.LeaderElectionID = "guildnet-operator-lock"
	opts.LeaderElectionNamespace = leNS
	mgr, err := ctrl.NewManager(restCfg, opts)
	if err != nil {
		return fmt.Errorf("manager create: %w", err)
	}
	r := &operator.WorkspaceReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme()}
	if err := r.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setup reconciler: %w", err)
	}

	// register federation FederatedService reconciler
	if err := (&federation.FederatedServiceReconciler{Client: mgr.GetClient(), Registry: reg}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setup federated service reconciler: %w", err)
	}
	// register FederatedCluster reconciler (membership tracking)
	if err := (&federation.FederatedClusterReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setup federated cluster reconciler: %w", err)
	}
	// register proxy controller
	if err := (&proxycontroller.ProxyReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme()}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setup proxy reconciler: %w", err)
	}
	go func() {
		if err := mgr.Start(ctx); err != nil {
			log.Printf("operator manager stopped: %v", err)
		}
	}()
	log.Printf("workspace operator started in-process")
	return nil
}

// WebSocket removed; SSE-only

// ensureSelfSigned creates a minimal self-signed certificate if not present.
func ensureSelfSigned(dir, certPath, keyPath string) error {
	// Production mode: do not generate self-signed certificates. Require valid cert and key to exist.
	if _, err := os.Stat(certPath); err == nil {
		if _, err2 := os.Stat(keyPath); err2 == nil {
			return nil
		}
	}
	return fmt.Errorf("tls cert or key not found: %s and %s; place valid certs in ./certs/ or %s", certPath, keyPath, dir)
}

// dns1123Name converts an arbitrary string into a DNS-1123 compliant name:
// - lowercased
// - only a-z, 0-9, and '-'
// - must start/end with alphanumeric; collapse multiple dashes
func dns1123Name(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			prevDash = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case r == '-' || r == '_' || r == ' ':
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		default:
			// drop
		}
	}
	res := strings.Trim(b.String(), "-")
	// trim repeated dashes
	for strings.Contains(res, "--") {
		res = strings.ReplaceAll(res, "--", "-")
	}
	return res
}

// mirrorPublishedToConfigMap writes the host's published services list into a shared ConfigMap so
// other devices can observe and resync. Non-fatal; best-effort.
func mirrorPublishedToConfigMap(ctx context.Context, k *k8s.Client, clusterID string, list []localdb.PublishedService) error {
	if k == nil || k.K == nil {
		return nil
	}
	ns := "guildnet-system"
	name := fmt.Sprintf("published-%s", dns1123Name(clusterID))
	b, _ := json.MarshalIndent(list, "", "  ")
	cm, err := k.K.CoreV1().ConfigMaps(ns).Get(ctx, name, metav1.GetOptions{})
	if err == nil && cm != nil {
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data["published.json"] = string(b)
		_, _ = k.K.CoreV1().ConfigMaps(ns).Update(ctx, cm, metav1.UpdateOptions{})
		return nil
	}
	// create
	_, _ = k.K.CoreV1().ConfigMaps(ns).Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{"app": "published-registry"}}, Data: map[string]string{"published.json": string(b)}}, metav1.CreateOptions{})
	return nil
}

// deriveAgentHost attempts to pick a stable host base from job spec image or name.
func deriveAgentHost(spec model.JobSpec) string {
	base := strings.TrimSpace(spec.Name)
	if base == "" {
		img := spec.Image
		last := img
		if i := strings.LastIndex(img, "/"); i >= 0 && i+1 < len(img) {
			last = img[i+1:]
		}
		if j := strings.IndexByte(last, ':'); j >= 0 {
			last = last[:j]
		}
		base = last
	}
	if base == "" {
		base = "workload"
	}
	host := dns1123Name(base)
	if host == "" {
		host = "workload"
	}
	return host
}

func main() {
	log.SetFlags(0)
	cmd := "serve"
	if len(os.Args) > 1 {
		// Compatibility shim: support "--mode operator"
		if os.Args[1] == "--mode" && len(os.Args) > 2 && os.Args[2] == "operator" {
			cmd = "operator"
		} else {
			cmd = os.Args[1]
		}
	}

	// helper: load kubeconfig YAML either from an env var that contains
	// the raw kubeconfig or from a path. Prefer GN_CONTROL_PLANE_KUBECONFIG,
	// then KUBECONFIG, then ~/.guildnet/kubeconfig. Returns empty string
	// when none found.
	loadKubeYAML := func() string {
		// try explicit env var first
		tryEnv := func(v string) string {
			v = strings.TrimSpace(v)
			if v == "" {
				return ""
			}
			// if it looks like a path and the file exists, read it
			if strings.HasPrefix(v, "/") || strings.HasPrefix(v, "./") || strings.HasPrefix(v, "~") {
				// expand ~
				if strings.HasPrefix(v, "~") {
					if h, err := os.UserHomeDir(); err == nil {
						v = filepath.Join(h, strings.TrimPrefix(v, "~"))
					}
				}
				if b, err := os.ReadFile(v); err == nil {
					return string(b)
				}
			}
			// otherwise treat env var as raw kubeconfig content
			// sanity check: if it contains 'apiVersion' or 'clusters' assume it's kube YAML
			if strings.Contains(v, "apiVersion:") || strings.Contains(v, "clusters:") || strings.Contains(v, "contexts:") {
				return v
			}
			return ""
		}
		if s := tryEnv(os.Getenv("GN_CONTROL_PLANE_KUBECONFIG")); s != "" {
			return s
		}
		if s := tryEnv(os.Getenv("KUBECONFIG")); s != "" {
			return s
		}
		if h, err := os.UserHomeDir(); err == nil {
			p := filepath.Join(h, ".guildnet", "kubeconfig")
			if b, err := os.ReadFile(p); err == nil {
				return string(b)
			}
		}
		return ""
	}

	switch cmd {
	case "init":
		if err := config.RunInitWizard(os.Stdin, os.Stdout); err != nil {
			log.Fatalf("init failed: %v", err)
		}
		fmt.Println("config written to", config.ConfigPath())
		return
	case "serve":
		// continue
	default:
		// Unknown commands fall back to serve for compatibility.
		log.Printf("warning: unknown command '%s', defaulting to serve", cmd)
		cmd = "serve"
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("invalid config: %v", err)
	}

	// Create cancellation context for the serve lifecycle; include HUP/QUIT so closing the terminal also stops the server
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer stop()

	// On Linux, ask the kernel to send us SIGTERM if the parent dies (e.g., terminal closed).
	if strings.ToLower(strings.TrimSpace(os.Getenv("GN_DISABLE_PDEATHSIG"))) != "1" {
		_ = setParentDeathSignal(syscall.SIGTERM)
	}

	// Server-first runtime: open local DB and secrets manager
	stateDir := config.StateDir()
	ldb, err := localdb.Open(stateDir)
	if err != nil {
		log.Fatalf("open local db: %v", err)
	}
	defer ldb.Close()
	// Ensure orchestration buckets
	_ = ldb.EnsureBuckets("orgs", "headscales", "namespaces", "keys", "clusters", "nodes", "credentials", "jobs", "joblogs", "audit")
	// Ensure settings buckets
	_ = settings.EnsureBucket(ldb)
	masterKey := strings.TrimSpace(os.Getenv("GUILDNET_MASTER_KEY"))
	if masterKey == "" {
		log.Printf("warning: GUILDNET_MASTER_KEY not set; secrets encryption disabled for dev")
	}
	sec, _ := secrets.New(masterKey)
	_ = sec

	// Read runtime settings
	setMgr := settings.Manager{DB: ldb}
	var tsSet settings.Tailscale
	_ = setMgr.GetTailscale(&tsSet)
	if strings.TrimSpace(tsSet.LoginServer) == "" {
		// Fallback to config.json on first run to seed settings
		tsSet = settings.Tailscale{LoginServer: cfg.LoginServer, PreauthKey: cfg.AuthKey, Hostname: cfg.Hostname}
		_ = setMgr.PutTailscale(tsSet)
	}
	// Global: migrate defaults on first run
	var gset settings.Global
	_ = setMgr.GetGlobal(&gset)
	changed := false
	if strings.TrimSpace(gset.DefaultNamespace) == "" {
		gset.DefaultNamespace = "default"
		changed = true
	}
	if strings.TrimSpace(gset.ListenLocal) == "" {
		gset.ListenLocal = cfg.ListenLocal
		changed = true
	}
	if strings.TrimSpace(gset.FrontendOrigin) == "" {
		// Seed a sensible default; single origin on 8090
		gset.FrontendOrigin = "https://127.0.0.1:8090"
		changed = true
	}
	// keep existing EmbedOperator value as-is
	if changed {
		_ = setMgr.PutGlobal(gset)
	}

	// Single-instance lock to avoid multiple hostapp processes interfering
	lockPath := filepath.Join(config.StateDir(), "hostapp.lock")
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		log.Fatalf("unable to open lock file %s: %v", lockPath, err)
	}
	// Try to acquire exclusive lock non-blocking
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		// Could not acquire; try to read pid and report
		data, _ := io.ReadAll(lockFile)
		existing := strings.TrimSpace(string(data))
		if existing == "" {
			log.Fatalf("another hostapp appears to be running (lock held) and no pid in %s", lockPath)
		}
		if pid, perr := strconv.Atoi(existing); perr == nil {
			// Check process existence
			if err := syscall.Kill(pid, 0); err == nil {
				log.Fatalf("another hostapp (pid=%d) is already running; aborting", pid)
			}
		}
		// stale lock file: attempt to acquire again
		if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			log.Fatalf("unable to acquire lock file %s: %v", lockPath, err)
		}
	}
	// Write our pid into the lock file
	_ = lockFile.Truncate(0)
	_, _ = lockFile.Seek(0, 0)
	_, _ = lockFile.WriteString(strconv.Itoa(os.Getpid()))
	// Ensure lock file is cleaned on exit
	defer func() {
		// remove pid contents and unlock
		_ = lockFile.Truncate(0)
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		_ = lockFile.Close()
		_ = os.Remove(lockPath)
	}()

	// Start tsnet from settings
	s, err := ts.StartServer(ctx, ts.Options{StateDir: config.StateDir(), Hostname: tsSet.Hostname, LoginURL: tsSet.LoginServer, AuthKey: tsSet.PreauthKey})
	if err != nil {
		log.Fatalf("tsnet start: %v", err)
	}
	tsServer := s
	// Ensure tsnet server is closed on exit to avoid lingering background activity
	defer func() {
		_ = tsServer.Close()
	}()

	mux := http.NewServeMux()

	// In-memory store (includes registry)
	mem := store.New()
	go func() {
		// Periodically prune stale agents (e.g., >2 minutes)
		t := time.NewTicker(120 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				mem.PruneAgents(2 * time.Minute)
			}
		}
	}()

	// Per-cluster registry (always on in prototype)
	reg := cluster.NewRegistry(cluster.Options{StateDir: stateDir, Resolver: kubeconfigResolver{DB: ldb, Sec: sec}})

	// In-memory hostapp site registry (discovery/federation PoC)
	hReg := hostapppkg.NewRegistry()
	joinH, leaveH, hbH := hostappapi.NewSiteHandlers(hReg)
	mux.HandleFunc("/api/site/join", joinH)
	mux.HandleFunc("/api/site/leave", leaveH)
	mux.HandleFunc("/api/site/heartbeat", hbH)

	// New orchestration API wired with dependencies and settings change hook
	deps := api.Deps{DB: ldb, Secrets: sec, Runner: nil, Registry: reg, OnSettingsChanged: func(kind string) {
		log.Printf("settings updated: %s; restarting to apply", kind)
		stop() // trigger graceful shutdown; external supervisor restarts process
	}}
	apiMux := api.Router(deps)
	mux.Handle("/api/deploy/", apiMux)
	mux.Handle("/api/jobs", apiMux)
	mux.Handle("/api/jobs/", apiMux)
	mux.Handle("/api/jobs-logs/", apiMux)
	mux.Handle("/ws/jobs", apiMux)
	// Mount API router for all /api/ paths so bootstrap and other endpoints are handled
	mux.Handle("/api/", http.StripPrefix("/api", apiMux))
	// Mount additional API groups served by router
	mux.Handle("/api/cluster/", apiMux)
	mux.Handle("/sse/cluster/", apiMux)
	// Ensure core health endpoint is reachable
	mux.Handle("/api/health", apiMux)

	// Best-effort: restore persisted published mappings in background
	go func() {
		if err := api.RestorePublishedMappings(context.Background(), deps); err != nil {
			log.Printf("api: restore published mappings failed: %v", err)
		}
		// Multi-device startup resync (PoC): mirror published services to in-cluster ConfigMap so other devices converge
		// and trigger a light CRD list to warm caches. Non-fatal on errors.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if deps.Registry != nil {
			for _, s := range deps.Registry.List() {
				inst, err := deps.Registry.Get(ctx, s.ID)
				if err != nil || inst == nil || inst.K8s == nil {
					continue
				}
				// Read persisted published services and write into a ConfigMap
				var list []localdb.PublishedService
				if err := deps.DB.ListPublished(&list); err == nil {
					if len(list) > 0 {
						_ = mirrorPublishedToConfigMap(ctx, inst.K8s, s.ID, list)
					}
				}
				// Warm-up: attempt a CRD discovery call via dynamic client (FederatedService)
				if inst.Dyn != nil {
					_, _ = inst.Dyn.Resource(schema.GroupVersionResource{Group: "guildnet.io", Version: "v1alpha1", Resource: "federatedservices"}).Namespace("").List(ctx, metav1.ListOptions{Limit: 1})
				}
			}
		}
	}()

	// health check
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// internal shutdown endpoint for graceful stop from local tooling
	mux.HandleFunc("/internal/shutdown", func(w http.ResponseWriter, r *http.Request) {
		// only accept local POST requests
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if host != "127.0.0.1" && host != "::1" && host != "localhost" {
			httpx.JSONError(w, http.StatusForbidden, "shutdown allowed from localhost only", "forbidden")
			return
		}
		go func() {
			// give caller a moment to receive response
			time.Sleep(50 * time.Millisecond)
			stop()
		}()
		httpx.JSON(w, http.StatusOK, map[string]any{"status": "shutting down"})
	})

	// Registry endpoints (minimal)
	mux.HandleFunc("/api/v1/agents/register", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var rec model.AgentRecord
		if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
			httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		if strings.TrimSpace(rec.ID) == "" || strings.TrimSpace(rec.IP) == "" {
			httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "id and ip required"})
			return
		}
		rec.LastSeen = model.NowISO()
		mem.UpsertAgent(&rec)
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("/api/v1/resolve", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		org := strings.TrimSpace(r.URL.Query().Get("org"))
		if id == "" {
			httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "id required"})
			return
		}
		if a, ok := mem.GetAgent(org, id); ok {
			resp := model.ResolveResponse{IP: a.IP, Ports: a.Ports, ExpiresAt: time.Now().Add(60 * time.Second).UTC().Format(time.RFC3339)}
			httpx.JSON(w, http.StatusOK, resp)
			return
		}
		httpx.JSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	})

	mux.HandleFunc("/api/v1/agents", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		org := strings.TrimSpace(r.URL.Query().Get("org"))
		list := mem.ListAgents(org)
		httpx.JSON(w, http.StatusOK, list)
	})

	// lightweight in-memory metrics (JSON)
	mux.HandleFunc("/api/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Snapshot any `json:"snapshot"`
		}{Snapshot: metrics.Export()})
	})

	// Kubernetes client: we no longer support implicit single-cluster mode.
	// If a control-plane kubeconfig is provided (path or raw content), use it;
	// otherwise, rely on the per-cluster Registry for actuation and leave k8s nil.
	var kcli *k8s.Client
	kubeYAML := loadKubeYAML()
	if strings.TrimSpace(kubeYAML) != "" {
		if kcClient, err := k8s.NewFromKubeconfig(ctx, kubeYAML, struct {
			APIProxyURL string
			ForceHTTP   bool
			Dial        func(ctx context.Context, network, addr string) (net.Conn, error)
		}{}); err == nil {
			kcli = kcClient
		} else {
			log.Printf("control plane k8s client init failed: %v (continuing without control-plane client)", err)
		}
	}
	var dyn dynamic.Interface
	// Optional: local port-forward manager for pods (fallback when API server service/pod proxy is unreliable)
	var pfMgr *k8s.PortForwardManager
	// Always use Workspace CRDs (legacy deployment path removed)
	if kcli != nil && kcli.Rest != nil {
		if d, derr := dynamic.NewForConfig(kcli.Rest); derr == nil {
			dyn = d
		} else {
			log.Printf("dynamic client init failed: %v", derr)
		}
		pfMgr = k8s.NewPortForwardManager(kcli.Rest, "default")
	}
	// Debug: log resolved API host for visibility
	if kcli != nil && kcli.Rest != nil {
		log.Printf("k8s: REST host resolved to %s", kcli.Rest.Host)
	}
	if kcli != nil && kcli.Rest != nil {
		log.Printf("Workspace CRD mode active (legacy paths removed)")
	} else {
		log.Printf("Kubernetes not available; CRD/operator features disabled")
	}

	// Start operator (controller-runtime) in-process only when explicitly enabled.
	// Production default: embedded operator is disabled. Enable via GN_EMBED_OPERATOR=1 environment variable.
	if kcli != nil && kcli.Rest != nil {
		if strings.TrimSpace(os.Getenv("GN_EMBED_OPERATOR")) == "1" {
			go func() {
				if err := startOperator(ctx, kcli.Rest, reg); err != nil {
					log.Printf("operator start failed: %v", err)
				}
			}()
		}
	}

	// Permission cache (prototype) – only used in CRD mode for admin/destructive actions.
	var permCache *permission.Cache
	if dyn != nil {
		permCache = permission.NewCache(dyn, "default", 10*time.Second)
	}
	defaultNS := func() string {
		var g settings.Global
		_ = setMgr.GetGlobal(&g)
		if strings.TrimSpace(g.DefaultNamespace) != "" {
			return strings.TrimSpace(g.DefaultNamespace)
		}
		return "default"
	}()

	// Default workspace ingress knobs: no implicit ingress class via env; use cluster settings per cluster when creating resources.

	// UI config (optional)
	mux.HandleFunc("/api/ui-config", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]any{"name": cfg.Name})
	})

	// Register hostapp presence (type=gateway) to local in-memory registry
	go func() {
		info, _ := ts.Info(ctx, tsServer)
		rec := &model.AgentRecord{
			ID:       cfg.Hostname,
			Org:      "", // deprecated; tenants handled per-cluster
			Hostname: cfg.Hostname,
			IP:       "",
			Ports:    map[string]int{"ui": 8090},
			Version:  "hostapp",
		}
		if info != nil && info.IP != "" {
			rec.IP = info.IP
		}
		if rec.IP == "" {
			rec.IP = "100.64.0.1"
		} // placeholder if info fails
		rec.LastSeen = model.NowISO()
		mem.UpsertAgent(rec)
		// refresh periodically
		tick := time.NewTicker(60 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				rec.LastSeen = model.NowISO()
				mem.UpsertAgent(rec)
			}
		}
	}()

	// Heartbeat poster: always-on poster that posts device capabilities periodically
	// to the local Host App API (fixed to https://127.0.0.1:8090/v1/sites/heartbeat).
	go func() {
		interval := 30 * time.Second
		if v := strings.TrimSpace(os.Getenv("GN_HEARTBEAT_INTERVAL")); v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				interval = d
			}
		}
		for _, a := range os.Args[1:] {
			if strings.HasPrefix(a, "--heartbeat-interval=") {
				if d, err := time.ParseDuration(strings.TrimPrefix(a, "--heartbeat-interval=")); err == nil {
					interval = d
				}
			}
		}
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
		// device id fallback
		deviceID := cfg.Hostname
		if hn, err := os.Hostname(); err == nil && hn != "" {
			deviceID = hn
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
				// build payload
				payload := map[string]any{"id": deviceID, "name": deviceID}
				if info, err := ts.Info(context.Background(), tsServer); err == nil && info != nil {
					ips := []string{}
					if info.IP != "" {
						ips = append(ips, info.IP)
					}
					if info.FQDN != "" {
						ips = append(ips, info.FQDN)
					}
					if len(ips) > 0 {
						payload["tailnetIPs"] = ips
					}
				}
				payload["cpuMilli"] = int64(runtime.NumCPU() * 1000)
				if mb := readMemMB(); mb > 0 {
					payload["memoryMB"] = mb
				}
				if sm := readStorageMB("/"); sm > 0 {
					payload["storageMB"] = sm
				}
				payload["vramMB"] = 0
				// list persisted clusters and post
				var clusters []map[string]any
				if err := ldb.List("clusters", &clusters); err == nil {
					// allow overriding the heartbeat target via GN_HEARTBEAT_URL
					hbURL := strings.TrimSpace(os.Getenv("GN_HEARTBEAT_URL"))
					if hbURL == "" {
						hbURL = "https://127.0.0.1:8090/api/v1/sites/heartbeat"
					}
					for _, c := range clusters {
						cid := fmt.Sprint(c["id"])
						if cid == "" {
							continue
						}
						// set canonical `clusterId` only
						payload["clusterId"] = cid
						b, _ := json.Marshal(payload)
						req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, hbURL, strings.NewReader(string(b)))
						req.Header.Set("Content-Type", "application/json")
						if resp, err := client.Do(req); err != nil {
							log.Printf("heartbeat post cluster=%s err=%v", cid, err)
						} else {
							io.Copy(io.Discard, resp.Body)
							resp.Body.Close()
						}
					}
				}
			}
		}
	}()
	log.Printf("heartbeat poster enabled (always-on)")

	// Smoke: resolve and attempt a tsnet dial to given id:port
	mux.HandleFunc("/api/v1/smoke-dial", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		port := strings.TrimSpace(r.URL.Query().Get("port"))
		if id == "" || port == "" {
			httpx.JSON(w, http.StatusBadRequest, map[string]string{"error": "id and port required"})
			return
		}
		if a, ok := mem.GetAgent("", id); ok {
			addr := a.IP + ":" + port
			ctxDial, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			c, err := ts.DialContext(ctxDial, tsServer, "tcp", addr)
			if err != nil {
				httpx.JSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
				return
			}
			_ = c.Close()
			httpx.JSON(w, http.StatusOK, map[string]string{"ok": addr})
			return
		}
		httpx.JSON(w, http.StatusNotFound, map[string]string{"error": "id not found"})
	})

	// UI handling: serve compiled UI from ui/dist with SPA fallback to index.html (no redirects)
	{
		dist := filepath.Join("ui", "dist")
		indexPath := filepath.Join(dist, "index.html")
		if fi, err := os.Stat(dist); err == nil && fi.IsDir() {
			// Favicon: serve from dist if present, else 204 to avoid 404 noise
			mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
				fav := filepath.Join(dist, "favicon.ico")
				if fi, err := os.Stat(fav); err == nil && !fi.IsDir() {
					http.ServeFile(w, r, fav)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			})

			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				// Only handle UI paths here (API/proxy matched earlier)
				// Normalize and prevent path traversal
				path := r.URL.Path
				if path == "" || path == "/" {
					// Avoid caching HTML to ensure latest hashed asset URLs are used
					w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
					http.ServeFile(w, r, indexPath)
					return
				}
				// Clean the path and ensure it stays within dist
				cleanPath := filepath.Clean(strings.TrimPrefix(path, "/"))
				full := filepath.Join(dist, cleanPath)
				// Security: ensure the full path is under dist
				if !strings.HasPrefix(full, dist+string(os.PathSeparator)) && full != dist {
					http.NotFound(w, r)
					return
				}
				// If file exists and is not a directory, serve it
				if fi, err := os.Stat(full); err == nil && !fi.IsDir() {
					// Long cache for hashed assets
					if strings.HasPrefix(cleanPath, "assets/") {
						w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
					}
					http.ServeFile(w, r, full)
					return
				}
				// SPA fallback only for non-asset paths (no dot in last segment)
				base := filepath.Base(cleanPath)
				if !strings.Contains(base, ".") {
					w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
					http.ServeFile(w, r, indexPath)
					return
				}
				// Otherwise, not found (avoid redirect loops)
				http.NotFound(w, r)
			})
			log.Printf("serving static UI from %s", dist)
		} else {
			log.Printf("ui/dist directory not found; UI will return 404 at root")
		}
	}

	// Preset deployable images (server-sourced; avoid hardcoding in UI)
	presetImages := []model.DeployImage{
		{Label: "VS Code (code-server)", Image: "codercom/code-server:4.90.3", Description: "Browser-based VS Code via code-server behind Caddy"},
		// Add more curated images here in the future.
	}

	// List deployable images
	mux.HandleFunc("/api/images", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		httpx.JSON(w, http.StatusOK, presetImages)
	})

	// Image defaults: return suggested env/ports for a given image reference
	mux.HandleFunc("/api/image-defaults", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		img := strings.TrimSpace(r.URL.Query().Get("image"))
		resp := map[string]any{}
		if img == "" {
			httpx.JSON(w, http.StatusOK, resp)
			return
		}
		// Very simple matcher; can be extended to read from config or OCI metadata.
		if strings.Contains(img, "guildnet/agent") {
			resp["ports"] = []model.Port{{Name: "http", Port: 8080}, {Name: "https", Port: 8443}}
			resp["env"] = map[string]string{"AGENT_HOST": ""}
		} else if strings.Contains(img, "codercom/code-server") || strings.Contains(img, "ghcr.io/coder/code-server") {
			resp["ports"] = []model.Port{{Name: "http", Port: 8080}}
			resp["env"] = map[string]string{"AGENT_HOST": ""}
		}
		httpx.JSON(w, http.StatusOK, resp)
	})

	// servers list (Workspace CRDs only; legacy Deployment path removed)
	mux.HandleFunc("/api/servers", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if dyn == nil { // dynamic client required now that only CRD mode exists
			httpx.JSONError(w, http.StatusInternalServerError, "dynamic client unavailable", "dyn_unavailable")
			return
		}
		gvr := schema.GroupVersionResource{Group: "guildnet.io", Version: "v1alpha1", Resource: "workspaces"}
		lst, err := dyn.Resource(gvr).Namespace(defaultNS).List(r.Context(), metav1.ListOptions{})
		if err != nil {
			// Degrade gracefully: return empty list when CRDs are not installed yet or API is not ready
			httpx.JSON(w, http.StatusOK, []any{})
			return
		}
		var out []*model.Server
		for _, item := range lst.Items {
			obj := item.Object
			meta := obj["metadata"].(map[string]any)
			spec := obj["spec"].(map[string]any)
			status, _ := obj["status"].(map[string]any)
			name := meta["name"].(string)
			image, _ := spec["image"].(string)
			phase, _ := status["phase"].(string)
			readyReplicas := int32(0)
			if rr, ok := status["readyReplicas"].(int64); ok {
				readyReplicas = int32(rr)
			}
			proxyTarget, _ := status["proxyTarget"].(string)
			ports := []model.Port{}
			if rawPorts, ok := spec["ports"].([]any); ok {
				for _, rp := range rawPorts {
					if pm, ok := rp.(map[string]any); ok {
						pnum := 0
						if pv, ok := pm["containerPort"].(int64); ok {
							pnum = int(pv)
						}
						if pnum == 0 {
							if pvf, ok := pm["containerPort"].(float64); ok {
								pnum = int(pvf)
							}
						}
						if pnum > 0 {
							ports = append(ports, model.Port{Name: strings.TrimSpace(fmt.Sprint(pm["name"])), Port: pnum})
						}
					}
				}
			} else if proxyTarget != "" { // fallback parse if spec.ports absent
				if i := strings.LastIndex(proxyTarget, ":"); i > 0 {
					var pnum int
					fmt.Sscanf(proxyTarget[i+1:], "%d", &pnum)
					if pnum > 0 {
						ports = append(ports, model.Port{Name: "main", Port: pnum})
					}
				}
			}
			statusStr := "pending"
			if phase == "Running" && readyReplicas > 0 {
				statusStr = "running"
			} else if phase == "Failed" {
				statusStr = "failed"
			}
			out = append(out, &model.Server{ID: name, Name: name, Image: image, Status: statusStr, Ports: ports, URL: ""})
		}
		httpx.JSON(w, http.StatusOK, out)
	})

	// server detail and logs (Workspace CRDs only)
	mux.HandleFunc("/api/servers/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/servers/")
		if path == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		parts := strings.Split(path, "/")
		id := parts[0]
		// CRD path only now
		if dyn == nil { // dynamic client mandatory
			httpx.JSONError(w, http.StatusInternalServerError, "dynamic client unavailable", "dyn_unavailable")
			return
		}
		gvr := schema.GroupVersionResource{Group: "guildnet.io", Version: "v1alpha1", Resource: "workspaces"}
		if len(parts) == 1 && r.Method == http.MethodDelete {
			gvr := schema.GroupVersionResource{Group: "guildnet.io", Version: "v1alpha1", Resource: "workspaces"}
			if err := dyn.Resource(gvr).Namespace(defaultNS).Delete(r.Context(), id, metav1.DeleteOptions{}); err != nil {
				httpx.JSONError(w, http.StatusNotFound, "workspace not found", "not_found")
				return
			}
			httpx.JSON(w, http.StatusOK, map[string]any{"deleted": id})
			return
		}
		if len(parts) == 1 && r.Method == http.MethodGet {
			ws, err := dyn.Resource(gvr).Namespace(defaultNS).Get(r.Context(), id, metav1.GetOptions{})
			if err != nil {
				httpx.JSONError(w, http.StatusNotFound, "workspace not found", "not_found")
				return
			}
			obj := ws.Object
			spec := obj["spec"].(map[string]any)
			status, _ := obj["status"].(map[string]any)
			image, _ := spec["image"].(string)
			phase, _ := status["phase"].(string)
			proxyTarget, _ := status["proxyTarget"].(string)
			readyReplicas := int32(0)
			if rr, ok := status["readyReplicas"].(int64); ok {
				readyReplicas = int32(rr)
			}
			statusStr := "pending"
			if phase == "Running" && readyReplicas > 0 {
				statusStr = "running"
			} else if phase == "Failed" {
				statusStr = "failed"
			}
			ports := []model.Port{}
			if rawPorts, ok := spec["ports"].([]any); ok {
				for _, rp := range rawPorts {
					if pm, ok := rp.(map[string]any); ok {
						pnum := 0
						if pv, ok := pm["containerPort"].(int64); ok {
							pnum = int(pv)
						} else if pvf, ok := pm["containerPort"].(float64); ok {
							pnum = int(pvf)
						}
						if pnum > 0 {
							ports = append(ports, model.Port{Name: strings.TrimSpace(fmt.Sprint(pm["name"])), Port: pnum})
						}
					}
				}
			} else if proxyTarget != "" {
				if i := strings.LastIndex(proxyTarget, ":"); i > 0 {
					var pnum int
					fmt.Sscanf(proxyTarget[i+1:], "%d", &pnum)
					if pnum > 0 {
						ports = append(ports, model.Port{Name: "main", Port: pnum})
					}
				}
			}
			httpx.JSON(w, http.StatusOK, &model.Server{ID: id, Name: id, Image: image, Status: statusStr, Ports: ports, URL: ""})
			return
		}
		if len(parts) == 2 && parts[1] == "logs" && r.Method == http.MethodGet {
			// list pods by label guildnet.io/workspace=<id>
			pods, err := kcli.K.CoreV1().Pods(defaultNS).List(r.Context(), metav1.ListOptions{LabelSelector: fmt.Sprintf("guildnet.io/workspace=%s", id)})
			if err != nil || len(pods.Items) == 0 {
				httpx.JSONError(w, http.StatusNotFound, "no pods for workspace", "no_pods")
				return
			}
			q := r.URL.Query()
			level := q.Get("level")
			if level == "" {
				level = "info"
			}
			limit := 200
			if v := q.Get("limit"); v != "" {
				fmt.Sscanf(v, "%d", &limit)
			}
			// Sort pods: ready first
			readyPods := []corev1.Pod{}
			unreadyPods := []corev1.Pod{}
			for _, p := range pods.Items {
				isReady := false
				for _, c := range p.Status.Conditions {
					if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
						isReady = true
						break
					}
				}
				if isReady {
					readyPods = append(readyPods, p)
				} else {
					unreadyPods = append(unreadyPods, p)
				}
			}
			ordered := append(readyPods, unreadyPods...)
			// Aggregate logs from up to N pods (cap at 5 to bound cost)
			maxPods := 5
			if len(ordered) < maxPods {
				maxPods = len(ordered)
			}
			tail := int64(limit / maxPods)
			if tail < 10 {
				tail = int64(limit)
			} // if very small limit, just pull full per pod
			out := []model.LogLine{}
			for i := 0; i < maxPods; i++ {
				p := ordered[i]
				container := ""
				if len(p.Spec.Containers) > 0 {
					container = p.Spec.Containers[0].Name
				}
				req := kcli.K.CoreV1().Pods(defaultNS).GetLogs(p.Name, &corev1.PodLogOptions{Container: container, TailLines: &tail})
				data, err := req.Do(r.Context()).Raw()
				if err != nil {
					continue
				}
				linesRaw := strings.Split(strings.TrimSpace(string(data)), "\n")
				for _, ln := range linesRaw {
					if ln != "" {
						out = append(out, model.LogLine{T: model.NowISO(), LVL: level, MSG: fmt.Sprintf("[%s] %s", p.Name, ln)})
					}
				}
			}
			// Truncate to requested limit if aggregated exceeded it
			if len(out) > limit {
				out = out[len(out)-limit:]
			}
			httpx.JSON(w, http.StatusOK, out)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	// jobs (Workspace CRD only; legacy Deployment path removed)
	mux.HandleFunc("/api/workspace-jobs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			httpx.JSONError(w, http.StatusBadRequest, "unable to read body", "bad_body")
			return
		}
		defer r.Body.Close()
		var spec model.JobSpec
		if err := json.Unmarshal(b, &spec); err != nil || spec.Image == "" {
			httpx.JSONError(w, http.StatusBadRequest, "invalid job spec", "invalid_spec", err)
			return
		}

		if dyn == nil { // dynamic client required
			httpx.JSONError(w, http.StatusInternalServerError, "dynamic client unavailable", "dyn_unavailable")
			return
		}
		gvr := schema.GroupVersionResource{Group: "guildnet.io", Version: "v1alpha1", Resource: "workspaces"}
		wsName := spec.Name
		if wsName == "" {
			wsName = dns1123Name(deriveAgentHost(spec))
		}
		baseName := wsName
		// ensure non-empty base
		if baseName == "" {
			baseName = "workspace"
		}
		// attempt up to 10 unique name generations if collisions occur
		for attempt := 0; attempt < 10; attempt++ {
			candidate := wsName
			if attempt > 0 { // append short random suffix
				// 5 hex chars from crypto/rand
				buf := make([]byte, 3)
				if _, rerr := rand.Read(buf); rerr == nil {
					sfx := hex.EncodeToString(buf)[:5]
					candidate = fmt.Sprintf("%s-%s", baseName, sfx)
				}
			}
			// name must remain <= 63 chars for DNS-1123
			if len(candidate) > 63 {
				candidate = candidate[:63]
			}
			wsName = candidate
			// probe existence
			_, gerr := dyn.Resource(gvr).Namespace(defaultNS).Get(r.Context(), wsName, metav1.GetOptions{})
			if gerr != nil {
				if apierrors.IsNotFound(gerr) {
					break // available
				}
				// on unexpected error just break and let create path surface it
				break
			}
			// exists; continue to next attempt
		}
		specMap := map[string]any{"image": spec.Image}
		if len(spec.Env) > 0 {
			var envArr []any
			for k, v := range spec.Env {
				kTrim := strings.TrimSpace(k)
				if kTrim == "" || strings.TrimSpace(v) == "" {
					continue
				}
				envArr = append(envArr, map[string]any{"name": kTrim, "value": v})
			}
			if len(envArr) > 0 {
				specMap["env"] = envArr
			}
		}
		if len(spec.Expose) > 0 {
			var portsArr []any
			for _, p := range spec.Expose {
				if p.Port > 0 {
					portsArr = append(portsArr, map[string]any{"containerPort": p.Port, "name": p.Name})
				}
			}
			if len(portsArr) > 0 {
				specMap["ports"] = portsArr
			}
		}
		obj := map[string]any{
			"apiVersion": "guildnet.io/v1alpha1",
			"kind":       "Workspace",
			"metadata":   map[string]any{"name": wsName},
			"spec":       specMap,
		}
		if _, err := dyn.Resource(gvr).Namespace(defaultNS).Create(r.Context(), &unstructured.Unstructured{Object: obj}, metav1.CreateOptions{}); err != nil {
			if apierrors.IsAlreadyExists(err) {
				// extremely unlikely due to prior check; add one more randomized suffix and retry once
				buf := make([]byte, 3)
				if _, rerr := rand.Read(buf); rerr == nil {
					alt := fmt.Sprintf("%s-%s", baseName, hex.EncodeToString(buf)[:5])
					obj["metadata"].(map[string]any)["name"] = alt
					if _, cerr := dyn.Resource(gvr).Namespace(defaultNS).Create(r.Context(), &unstructured.Unstructured{Object: obj}, metav1.CreateOptions{}); cerr == nil {
						httpx.JSON(w, http.StatusAccepted, model.JobAccepted{ID: alt, Status: "pending"})
						return
					}
				}
			}
			// If schema warning escalates to error referencing env[0].name or value, retry without env.
			if strings.Contains(err.Error(), "env[0].name") || strings.Contains(err.Error(), "env[0].value") {
				if specSection, ok := obj["spec"].(map[string]any); ok {
					if _, had := specSection["env"]; had {
						delete(specSection, "env")
						if _, rerr := dyn.Resource(gvr).Namespace(defaultNS).Create(r.Context(), &unstructured.Unstructured{Object: obj}, metav1.CreateOptions{}); rerr == nil {
							httpx.JSON(w, http.StatusAccepted, model.JobAccepted{ID: wsName, Status: "pending"})
							return
						}
					}
				}
			}
			httpx.JSONError(w, http.StatusInternalServerError, "workspace create failed", "create_failed", err.Error())
			return
		}
		httpx.JSON(w, http.StatusAccepted, model.JobAccepted{ID: wsName, Status: "pending"})
	})

	// admin: stop all servers (delete managed workloads)
	adminStopAll := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		log.Printf("admin: stop-all requested from %s", r.RemoteAddr)
		if permCache != nil {
			if !permCache.Allow(r.Context(), permission.ActionStopAll, map[string]string{}) {
				httpx.JSONError(w, http.StatusForbidden, "permission denied", "forbidden")
				return
			}
		}
		// Workspace CRD deletion only
		if dyn == nil { // dynamic client required
			httpx.JSONError(w, http.StatusInternalServerError, "dynamic client unavailable", "dyn_unavailable")
			return
		}
		gvr := schema.GroupVersionResource{Group: "guildnet.io", Version: "v1alpha1", Resource: "workspaces"}
		lst, err := dyn.Resource(gvr).Namespace(defaultNS).List(r.Context(), metav1.ListOptions{})
		if err != nil {
			httpx.JSONError(w, http.StatusInternalServerError, "list workspaces failed", "list_failed", err.Error())
			return
		}
		deleted := []string{}
		for _, item := range lst.Items {
			name := item.GetName()
			if err := dyn.Resource(gvr).Namespace(defaultNS).Delete(r.Context(), name, metav1.DeleteOptions{}); err == nil {
				deleted = append(deleted, name)
			}
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"deleted": deleted})
	}
	mux.HandleFunc("/api/admin/stop-all", adminStopAll)
	mux.HandleFunc("/api/admin/stop-all/", adminStopAll)
	// Also expose a flat path without the /admin prefix to avoid any ServeMux edge cases in dev
	mux.HandleFunc("/api/stop-all", adminStopAll)
	mux.HandleFunc("/api/stop-all/", adminStopAll)
	mux.HandleFunc("/api/admin/", func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/api/admin/") {
		case "stop-all", "stop-all/":
			adminStopAll(w, r)
			return
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	// logs SSE
	mux.HandleFunc("/sse/logs", func(w http.ResponseWriter, r *http.Request) {
		// Panic guard to surface 500s with context
		defer func(start time.Time) {
			if rec := recover(); rec != nil {
				log.Printf("sse/logs panic: target=%s level=%s remote=%s err=%v duration=%s", r.URL.Query().Get("target"), r.URL.Query().Get("level"), r.RemoteAddr, rec, time.Since(start))
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}(time.Now())

		q := r.URL.Query()
		id := q.Get("target")
		level := q.Get("level")
		if level == "" {
			level = "info"
		}
		tail := 200
		if v := q.Get("tail"); v != "" {
			fmt.Sscanf(v, "%d", &tail)
		}

		// Validate target exists (CRD-aware)
		if strings.TrimSpace(id) == "" {
			httpx.JSONError(w, http.StatusBadRequest, "missing target", "missing_target")
			return
		}
		if dyn != nil {
			gvr := schema.GroupVersionResource{Group: "guildnet.io", Version: "v1alpha1", Resource: "workspaces"}
			if _, err := dyn.Resource(gvr).Namespace(defaultNS).Get(r.Context(), id, metav1.GetOptions{}); err != nil {
				httpx.JSONError(w, http.StatusNotFound, "unknown target", "not_found")
				return
			}
		} else {
			if _, err := kcli.GetServer(r.Context(), defaultNS, id); err != nil {
				httpx.JSONError(w, http.StatusNotFound, "unknown target", "not_found")
				return
			}
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			httpx.JSONError(w, http.StatusInternalServerError, "streaming unsupported", "stream_unsupported")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		log.Printf("sse/logs open: target=%s level=%s tail=%d from %s", id, level, tail, r.RemoteAddr)
		enc := json.NewEncoder(w)

		// send tail first (best effort) by reading pods matching the Workspace label
		func() {
			defer func() { recover() }() // keep SSE alive on tail errors
			pods, err := kcli.K.CoreV1().Pods(defaultNS).List(r.Context(), metav1.ListOptions{LabelSelector: fmt.Sprintf("guildnet.io/workspace=%s", id)})
			if err != nil || len(pods.Items) == 0 {
				return
			}
			// Sort pods: ready first
			readyPods := []corev1.Pod{}
			unreadyPods := []corev1.Pod{}
			for _, p := range pods.Items {
				isReady := false
				for _, c := range p.Status.Conditions {
					if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
						isReady = true
						break
					}
				}
				if isReady {
					readyPods = append(readyPods, p)
				} else {
					unreadyPods = append(unreadyPods, p)
				}
			}
			ordered := append(readyPods, unreadyPods...)
			maxPods := 5
			if len(ordered) < maxPods {
				maxPods = len(ordered)
			}
			tailPer := int64(tail)
			if maxPods > 1 {
				tp := int64(tail / maxPods)
				if tp >= 10 {
					tailPer = tp
				}
			}
			for i := 0; i < maxPods; i++ {
				p := ordered[i]
				container := ""
				if len(p.Spec.Containers) > 0 {
					container = p.Spec.Containers[0].Name
				}
				req := kcli.K.CoreV1().Pods(defaultNS).GetLogs(p.Name, &corev1.PodLogOptions{Container: container, TailLines: &tailPer})
				data, err := req.Do(r.Context()).Raw()
				if err != nil {
					continue
				}
				for _, ln := range strings.Split(strings.TrimSpace(string(data)), "\n") {
					if ln == "" {
						continue
					}
					if _, err := w.Write([]byte("data: ")); err != nil {
						return
					}
					if err := enc.Encode(model.LogLine{T: model.NowISO(), LVL: level, MSG: fmt.Sprintf("[%s] %s", p.Name, ln)}); err != nil {
						return
					}
					if _, err := w.Write([]byte("\n")); err != nil {
						return
					}
					flusher.Flush()
				}
			}
		}()

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		// For now, no live watch wired; send heartbeats periodically
		heartbeat := time.NewTicker(20 * time.Second)
		defer heartbeat.Stop()
		for {
			select {
			case <-ctx.Done():
				log.Printf("sse/logs close: target=%s level=%s from=%s reason=context-done", id, level, r.RemoteAddr)
				return
			case <-heartbeat.C:
				if _, err := w.Write([]byte(": ping\n\n")); err != nil {
					log.Printf("sse/logs heartbeat write error: %v", err)
					return
				}
				flusher.Flush()
			}
		}
	})

	// proxy handler (CRD-aware resolution)
	proxyHandler := proxy.NewReverseProxy(proxy.Options{
		MaxBody: 10 * 1024 * 1024,
		Timeout: 30 * time.Second,
		Dial: func(ctx context.Context, network, address string) (any, error) {
			// For loopback targets in local dev, bypass tsnet and dial OS loopback directly.
			host, _, err := net.SplitHostPort(address)
			if err == nil {
				if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
					var d net.Dialer
					return d.DialContext(ctx, network, address)
				}
				if strings.EqualFold(host, "localhost") {
					var d net.Dialer
					return d.DialContext(ctx, network, address)
				}
			}
			return ts.DialContext(ctx, tsServer, network, address)
		},
		Logger: httpx.Logger(),
		ResolveServer: func(ctx context.Context, serverID string, subPath string) (string, string, string, error) {
			if dyn != nil {
				gvr := schema.GroupVersionResource{Group: "guildnet.io", Version: "v1alpha1", Resource: "workspaces"}
				if ws, err := dyn.Resource(gvr).Namespace(defaultNS).Get(ctx, serverID, metav1.GetOptions{}); err == nil {
					if status, ok := ws.Object["status"].(map[string]any); ok {
						if pt, ok := status["proxyTarget"].(string); ok && pt != "" {
							if i := strings.Index(pt, "://"); i > 0 {
								sch := pt[:i]
								rest := pt[i+3:]
								return sch, rest, subPath, nil
							}
						}
					}
				}
			}
			host, port, https, err := kcli.ResolveServiceAddress(ctx, defaultNS, serverID)
			if err != nil {
				return "", "", "", err
			}
			sch := "http"
			if https {
				sch = "https"
			}
			return sch, fmt.Sprintf("%s:%d", host, port), subPath, nil
		},
		APIProxy: func() (http.RoundTripper, func(req *http.Request, scheme, hostport, subPath string), bool) {
			// API proxy availability is determined by k8s client config already built; no HOSTAPP_* env checks here.
			cfg := kcli.Config()
			if cfg == nil {
				return nil, nil, false
			}
			rt, err := restTransport(cfg)
			if err != nil {
				return nil, nil, false
			}
			set := func(req *http.Request, scheme, hostport, subPath string) {
				// If targeting local PF (127.0.0.1), do not rewrite to API proxy
				hn := req.URL.Hostname()
				if hn == "127.0.0.1" || strings.EqualFold(hn, "localhost") {
					return
				}
				baseURL, _ := url.Parse(cfg.Host)
				if baseURL == nil {
					baseURL = &url.URL{Scheme: "https"}
				}
				if baseURL.Scheme == "" {
					baseURL.Scheme = "https"
				}
				req.URL.Scheme = baseURL.Scheme
				req.URL.Host = baseURL.Host
				req.Host = req.URL.Host
				// Extract service ID and port
				sid := strings.TrimSpace(req.Header.Get("X-Guild-Server-ID"))
				_, portStr, err := net.SplitHostPort(hostport)
				if err != nil || portStr == "" {
					parts := strings.Split(hostport, ":")
					if len(parts) > 1 {
						portStr = parts[len(parts)-1]
					} else {
						portStr = "80"
					}
				}
				// Pre-resolve ClusterIP:port for fallback if PF fails
				fallbackHost := ""
				fallbackScheme := "http"
				if sid != "" {
					if ip, pnum, isHTTPS, rerr := kcli.ResolveServiceAddress(context.Background(), defaultNS, sid); rerr == nil {
						fallbackHost = fmt.Sprintf("%s:%d", ip, pnum)
						if isHTTPS {
							fallbackScheme = "https"
						} else {
							fallbackScheme = "http"
						}
					}
				}
				// Prefer pod proxy when header provided
				preferPod := strings.TrimSpace(req.Header.Get("X-Guild-Prefer-Pod")) != ""
				usePF := strings.TrimSpace(req.Header.Get("X-Guild-Use-PortForward")) != ""
				if preferPod && sid != "" {
					// Discover pod behind service
					ns := defaultNS
					podName := ""
					if svc, err := kcli.K.CoreV1().Services(ns).Get(context.Background(), sid, metav1.GetOptions{}); err == nil && svc != nil && len(svc.Spec.Selector) > 0 {
						var selParts []string
						for k, v := range svc.Spec.Selector {
							selParts = append(selParts, fmt.Sprintf("%s=%s", k, v))
						}
						selector := strings.Join(selParts, ",")
						if pods, err := kcli.K.CoreV1().Pods(ns).List(context.Background(), metav1.ListOptions{LabelSelector: selector}); err == nil && len(pods.Items) > 0 {
							pick := -1
							for i, pod := range pods.Items {
								if pod.Status.Phase == corev1.PodRunning {
									ready := false
									for _, c := range pod.Status.Conditions {
										if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
											ready = true
											break
										}
									}
									if ready {
										pick = i
										break
									}
								}
							}
							if pick == -1 {
								for i, pod := range pods.Items {
									if pod.Status.Phase == corev1.PodRunning {
										pick = i
										break
									}
								}
							}
							if pick == -1 {
								pick = 0
							}
							podName = pods.Items[pick].Name
						}
					}
					if podName != "" {
						if usePF && pfMgr != nil {
							pnum := 80
							if n, err := fmt.Sscanf(portStr, "%d", &pnum); n == 0 || err != nil {
								pnum = 8080
							}
							log.Printf("proxy: attempting port-forward ns=%s pod=%s port=%d sid=%s", defaultNS, podName, pnum, sid)
							if lp, err := pfMgr.Ensure(context.Background(), defaultNS, podName, pnum); err == nil && lp > 0 {
								log.Printf("proxy: using port-forward localPort=%d -> %s:%d", lp, podName, pnum)
								req.URL.Scheme = "http"
								req.URL.Host = fmt.Sprintf("127.0.0.1:%d", lp)
								req.Host = req.URL.Host
								if fallbackHost != "" {
									req.Header.Set("X-Guild-Fallback-Hostport", fallbackHost)
									req.Header.Set("X-Guild-Fallback-Scheme", fallbackScheme)
								}
								req.Header.Del("X-Guild-Server-ID")
								req.URL.Path = singleJoiningSlash("", subPath)
								return
							}
							log.Printf("proxy: port-forward failed, falling back to pod proxy ns=%s pod=%s err=%v", defaultNS, podName, err)
						}
						if usePF && fallbackHost != "" {
							log.Printf("proxy: PF unavailable; trying direct ClusterIP %s for sid=%s", fallbackHost, sid)
							req.URL.Scheme = fallbackScheme
							req.URL.Host = fallbackHost
							req.Host = fallbackHost
							req.URL.Path = singleJoiningSlash("", subPath)
							return
						}
						proto := "http"
						if strings.EqualFold(scheme, "https") {
							proto = "https"
						}
						basePath := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s:%s:%s/proxy", defaultNS, proto, podName, portStr)
						fullBase := singleJoiningSlash(strings.TrimSuffix(baseURL.Path, "/"), basePath)
						req.URL.Path = singleJoiningSlash("", fullBase) + subPath
						return
					}
				}
				// Service proxy
				if strings.EqualFold(scheme, "https") {
					req.URL.Path = "/api/v1/namespaces/" + defaultNS + "/services/https:" + sid + ":" + portStr + "/proxy"
				} else {
					req.URL.Path = "/api/v1/namespaces/" + defaultNS + "/services/http:" + sid + ":" + portStr + "/proxy"
				}
				req.URL.Path = singleJoiningSlash("", req.URL.Path) + subPath
			}
			return rt, set, true
		},
	})
	// Lightweight debug endpoint to check routing without hitting upstream
	mux.HandleFunc("/api/proxy-debug", func(w http.ResponseWriter, r *http.Request) {
		// Echo common fields for quick diagnosis
		q := r.URL.Query()
		server := q.Get("server")
		sub := q.Get("path")
		if sub == "" {
			sub = "/"
		}
		httpx.JSON(w, 200, map[string]any{
			"server": server,
			"path":   sub,
			"rid":    r.Header.Get("X-Request-Id"),
		})
	})
	mux.Handle("/proxy", proxyHandler)
	mux.Handle("/proxy/", proxyHandler)

	// Resolve listen address: LISTEN_LOCAL env > settings.Global.ListenLocal > default
	listenAddr := strings.TrimSpace(os.Getenv("LISTEN_LOCAL"))
	if listenAddr == "" {
		var g settings.Global
		_ = setMgr.GetGlobal(&g)
		if v := strings.TrimSpace(g.ListenLocal); v != "" {
			listenAddr = v
		}
	}
	if listenAddr == "" {
		listenAddr = "127.0.0.1:8090"
	}

	// Wrap with middleware (logging, request id, CORS)
	corsOrigin := func() string {
		var g settings.Global
		_ = setMgr.GetGlobal(&g)
		if strings.TrimSpace(g.FrontendOrigin) != "" {
			return strings.TrimSpace(g.FrontendOrigin)
		}
		// Default dev origin follows listen address
		host, port, err := net.SplitHostPort(listenAddr)
		if err == nil {
			// When bound to localhost or 127.0.0.1, use that; otherwise use host:port directly
			if host == "" {
				host = "127.0.0.1"
			}
			return "https://" + net.JoinHostPort(host, port)
		}
		return "https://127.0.0.1:8090"
	}()
	handler := httpx.RequestID(httpx.Logging(httpx.CORS(corsOrigin)(mux)))

	// Certs: prefer repo CA-signed ./certs/server.crt|server.key, then ./certs/dev.crt|dev.key; else use ~/.guildnet/state/certs
	var certFile, keyFile string
	if _, err := os.Stat(filepath.Join("certs", "server.crt")); err == nil {
		certFile = filepath.Join("certs", "server.crt")
		keyFile = filepath.Join("certs", "server.key")
		log.Printf("using repo server certs: %s", certFile)
	} else if _, err := os.Stat(filepath.Join("certs", "dev.crt")); err == nil {
		certFile = filepath.Join("certs", "dev.crt")
		keyFile = filepath.Join("certs", "dev.key")
		log.Printf("using repo dev certs: %s", certFile)
	} else {
		certDir := filepath.Join(config.StateDir(), "certs")
		certFile = filepath.Join(certDir, "server.crt")
		keyFile = filepath.Join(certDir, "server.key")
		if err := ensureSelfSigned(certDir, certFile, keyFile); err != nil {
			log.Fatalf("tls cert: %v", err)
		}
	}

	// local server (TLS only)
	localSrv := &http.Server{
		Addr:         listenAddr,
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	// Force HTTP/1.1 on the local listener to avoid HTTP/2 related TLS/internal errors
	// which have been observed in development when clients and the server disagree
	// on ALPN or when intermediates mishandle h2 framing. This keeps local dev
	// interactions (curl, scripts) stable while still allowing tsnet :443 to
	// serve with its default ALPN behavior.
	localSrv.TLSConfig = &tls.Config{NextProtos: []string{"http/1.1"}}
	var v6Srv *http.Server
	// Pre-bind local listener and hard-fail if unavailable (no fallback)
	bindAddr := listenAddr
	lnLocal, err := net.Listen("tcp", bindAddr)
	if err != nil {
		log.Fatalf("local listen failed on %s: %v", bindAddr, err)
	}
	// Also prepare an IPv6 loopback listener on the same port when using localhost/127.0.0.1
	var lnLocalV6 net.Listener
	if host, port, err := net.SplitHostPort(bindAddr); err == nil {
		if host == "127.0.0.1" || strings.EqualFold(host, "localhost") {
			v6Srv = &http.Server{
				Addr:         net.JoinHostPort("::1", port),
				Handler:      handler,
				ReadTimeout:  10 * time.Second,
				WriteTimeout: 10 * time.Second,
				IdleTimeout:  60 * time.Second,
			}
			if l6, e6 := net.Listen("tcp", v6Srv.Addr); e6 == nil {
				lnLocalV6 = l6
			}
		}
	}

	// tsnet server remains on :443
	var tsSrv *http.Server
	var ln net.Listener
	{
		var err error
		ln, err = ts.Listen(ctx, tsServer, "tcp", ":443")
		if err != nil {
			log.Fatalf("tsnet listen: %v", err)
		}
		// do not defer ln.Close() here; close explicitly on shutdown to guarantee timely teardown
		tsSrv = &http.Server{
			Handler:      handler,
			ReadTimeout:  10 * time.Second,
			WriteTimeout: 10 * time.Second,
			IdleTimeout:  60 * time.Second,
		}
	}

	errCh := make(chan error, 3)
	go func() { errCh <- localSrv.ServeTLS(lnLocal, certFile, keyFile) }()
	if v6Srv != nil && lnLocalV6 != nil {
		go func() { errCh <- v6Srv.ServeTLS(lnLocalV6, certFile, keyFile) }()
	}
	go func() { errCh <- tsSrv.ServeTLS(ln, certFile, keyFile) }()
	log.Printf("serving TLS on local %s and tailscale listener :443", bindAddr)

	select {
	case <-ctx.Done():
		log.Printf("shutdown: signal received, closing listeners and servers")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// First, stop accepting new connections by closing listeners
		_ = lnLocal.Close()
		if lnLocalV6 != nil {
			_ = lnLocalV6.Close()
		}
		// then attempt graceful shutdown of HTTP servers
		if err := localSrv.Shutdown(shutdownCtx); err != nil {
			log.Printf("localSrv.Shutdown error: %v", err)
		}
		if v6Srv != nil {
			if err := v6Srv.Shutdown(shutdownCtx); err != nil {
				log.Printf("v6Srv.Shutdown error: %v", err)
			}
		}
		if err := tsSrv.Shutdown(shutdownCtx); err != nil {
			log.Printf("tsSrv.Shutdown error: %v", err)
		}
		// Close tsnet server explicitly and the ts listener
		if err := tsServer.Close(); err != nil {
			log.Printf("tsServer.Close error: %v", err)
		}
		if ln != nil {
			_ = ln.Close()
		}
		// Give a small grace period to let goroutines exit, then force exit to avoid lingering backgrounds
		time.Sleep(200 * time.Millisecond)
		log.Printf("shutdown: complete, exiting")
		os.Exit(0)
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}
}

// restTransport builds an http.RoundTripper from a kube rest.Config
// and forces HTTP/1.1 when talking to the API server to avoid sporadic
// HTTP/2 INTERNAL_ERROR on the /services/.../proxy endpoints.
func restTransport(cfg *rest.Config) (http.RoundTripper, error) {
	// Build TLS config from rest.Config
	tlsConfig, err := rest.TLSConfigFor(cfg)
	if err != nil {
		return nil, err
	}
	if tlsConfig == nil {
		tlsConfig = &tls.Config{}
	}
	// Force HTTP/1.1 by disabling HTTP/2 via NextProtos and ForceAttemptHTTP2
	tlsConfig.NextProtos = []string{"http/1.1"}
	base := &http.Transport{
		Proxy:              http.ProxyFromEnvironment,
		TLSClientConfig:    tlsConfig,
		ForceAttemptHTTP2:  false,
		DisableKeepAlives:  true,
		MaxIdleConns:       100,
		IdleConnTimeout:    90 * time.Second,
		DisableCompression: false,
	}
	// Wrap with client-go auth/impersonation handlers
	return rest.HTTPWrappersForConfig(cfg, base)
}

// join path helper
func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}
