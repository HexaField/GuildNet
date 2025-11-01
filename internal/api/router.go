package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"nhooyr.io/websocket"

	"github.com/your/module/internal/cluster"
	"github.com/your/module/internal/httpx"
	"github.com/your/module/internal/jobs"
	"github.com/your/module/internal/localdb"
	"github.com/your/module/internal/orch"
	"github.com/your/module/internal/proxy"
	"github.com/your/module/internal/secrets"

	// New settings
	"github.com/your/module/internal/settings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	intstr "k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/pointer"
)

// dns1123Name converts a string to a DNS-1123 compliant name for resource names.
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
		}
	}
	res := strings.Trim(b.String(), "-")
	for strings.Contains(res, "--") {
		res = strings.ReplaceAll(res, "--", "-")
	}
	if res == "" {
		res = "item"
	}
	return res
}

// publishedListener holds metadata for an on-demand published listener.
type publishedListener struct {
	clusterID string
	service   string
	addr      string // tsnet listener address (host:port or :port)
	ln        net.Listener
	addedAt   time.Time
}

var (
	// publishedMap stores active published listeners keyed by clusterID+service
	publishedMap   = map[string]*publishedListener{}
	publishedMapMu sync.Mutex
)

// clusterPublishedConfigMapName returns the namespaced name where published mappings are mirrored.
// We use a single ConfigMap per cluster under guildnet-system namespace with key "published.json".
func clusterPublishedConfigMapName(clusterID string) (namespace, name string) {
	ns := "guildnet-system"
	// keep name deterministic per cluster id
	name = fmt.Sprintf("published-%s", dns1123Name(clusterID))
	return ns, name
}

// Deps are runtime dependencies for the orchestration API.
type Deps struct {
	DB      *localdb.DB
	Secrets *secrets.Manager
	Runner  *jobs.Runner
	Token   string // optional bearer token for mutating endpoints
	// Optional callback to trigger host restart/reload when certain settings change
	OnSettingsChanged func(kind string)
	// Optional per-cluster registry for isolation
	Registry *cluster.Registry
}

func (d Deps) ensure() Deps {
	db := d.DB
	dd := d
	if dd.Runner == nil {
		persist := jobs.LocalPersist{DB: db}
		r := jobs.New(jobs.WithPersist(persist))
		dd.Runner = r
	}
	return dd
}

// Router wires the orchestration API endpoints.
func Router(deps Deps) *http.ServeMux {
	deps = deps.ensure()
	mux := http.NewServeMux()

	// Authorization helper for mutating endpoints
	authOK := func(w http.ResponseWriter, r *http.Request) bool {
		// Allow all GETs; guard mutating methods
		if r.Method == http.MethodGet {
			return true
		}
		if r.Method == http.MethodOptions {
			return true
		}
		tok := strings.TrimSpace(deps.Token)
		if tok == "" {
			// No token set: allow only loopback clients
			host, _, _ := net.SplitHostPort(r.RemoteAddr)
			ip := net.ParseIP(host)
			if ip != nil && (ip.IsLoopback() || host == "127.0.0.1" || host == "::1") {
				return true
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return false
		}
		authz := r.Header.Get("Authorization")
		if strings.HasPrefix(strings.ToLower(authz), "bearer ") && strings.TrimSpace(authz[7:]) == tok {
			return true
		}
		if r.Header.Get("X-API-Token") == tok {
			return true
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}

	// Settings manager
	setMgr := settings.Manager{DB: deps.DB}

	// Bootstrap endpoint: accept a subset of guildnet.config and persist.
	mux.HandleFunc("/bootstrap", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Tailscale *settings.Tailscale `json:"tailscale"`
			Cluster   *struct {
				Kubeconfig         string `json:"kubeconfig"`
				Name               string `json:"name,omitempty"`
				Namespace          string `json:"namespace,omitempty"`
				APIProxyURL        string `json:"api_proxy_url,omitempty"`
				APIProxyForceHTTP  bool   `json:"api_proxy_force_http,omitempty"`
				DisableAPIProxy    bool   `json:"disable_api_proxy,omitempty"`
				PreferPodProxy     bool   `json:"prefer_pod_proxy,omitempty"`
				UsePortForward     bool   `json:"use_port_forward,omitempty"`
				IngressDomain      string `json:"ingress_domain,omitempty"`
				IngressClassName   string `json:"ingress_class_name,omitempty"`
				WorkspaceTLSSecret string `json:"workspace_tls_secret,omitempty"`
				CertManagerIssuer  string `json:"cert_manager_issuer,omitempty"`
				IngressAuthURL     string `json:"ingress_auth_url,omitempty"`
				IngressAuthSignin  string `json:"ingress_auth_signin,omitempty"`
				ImagePullSecret    string `json:"image_pull_secret,omitempty"`
				OrgID              string `json:"org_id,omitempty"`
			} `json:"cluster"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Tailscale != nil {
			_ = setMgr.PutTailscale(*body.Tailscale)
		}
		// If kubeconfig provided, compute deterministic id from kubeconfig and upsert cluster record.
		if body.Cluster != nil && strings.TrimSpace(body.Cluster.Kubeconfig) != "" && deps.DB != nil {
			// Compute deterministic id from kubeconfig to ensure a single source-of-truth per control-plane.
			detID, derr := cluster.DeterministicIDFromKubeconfig(body.Cluster.Kubeconfig)
			id := detID
			if derr != nil || strings.TrimSpace(id) == "" {
				// fallback to a uuid if deterministic computation failed for unexpected reasons
				id = uuid.NewString()
			}
			name := body.Cluster.Name
			if strings.TrimSpace(name) == "" {
				name = id
			}
			rec := map[string]any{"id": id, "name": name, "state": "imported"}
			// Upsert cluster record by deterministic id
			_ = deps.DB.Put("clusters", id, rec)
			// Store kubeconfig under deterministic id key
			_ = deps.DB.Put("credentials", fmt.Sprintf("cl:%s:kubeconfig", id), map[string]any{"value": body.Cluster.Kubeconfig})
			// Attempt to pre-warm per-cluster clients via registry (if available).
			// If pre-warm fails, remove persisted records and return an error to the caller.
			if deps.Registry != nil {
				// Try to build an instance and do a lightweight connectivity check.
				inst, err := deps.Registry.Get(r.Context(), id)
				if err != nil {
					// cleanup persisted data
					_ = deps.DB.Delete("clusters", id)
					_ = deps.DB.Delete("credentials", fmt.Sprintf("cl:%s:kubeconfig", id))
					httpx.JSONError(w, http.StatusUnprocessableEntity, "cluster connect failed", "cluster_connect", err.Error())
					return
				}
				// perform a quick API connectivity check (server version) with a short timeout
				checkCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
				defer cancel()
				if inst == nil || inst.K8s == nil || inst.K8s.K == nil {
					// cleanup persisted data
					_ = deps.DB.Delete("clusters", id)
					_ = deps.DB.Delete("credentials", fmt.Sprintf("cl:%s:kubeconfig", id))
					httpx.JSONError(w, http.StatusUnprocessableEntity, "cluster client initialization failed", "cluster_client", "client not initialized")
					return
				}
				// quick API call to ensure cluster is reachable: list namespaces with limit=1
				if _, err := inst.K8s.K.CoreV1().Namespaces().List(checkCtx, metav1.ListOptions{Limit: 1}); err != nil {
					_ = deps.DB.Delete("clusters", id)
					_ = deps.DB.Delete("credentials", fmt.Sprintf("cl:%s:kubeconfig", id))
					httpx.JSONError(w, http.StatusUnprocessableEntity, "cluster connect failed", "cluster_connect", err.Error())
					return
				}
				// Attempt to pre-warm RethinkDB (cluster DB) so DB endpoints respond quickly.
				// Best-effort: Do not fail bootstrap if RDB is not yet reachable (e.g., local clusters without LB).
				rdbCtx, rdbCancel := context.WithTimeout(r.Context(), 10*time.Second)
				defer rdbCancel()
				if err := inst.EnsureRDB(rdbCtx, "", "", ""); err != nil {
					log.Printf("bootstrap: RDB pre-warm skipped for cluster=%s: %v", id, err)
				}
			}
			// Persist per-cluster settings if provided
			cs := settings.Cluster{
				Name:               body.Cluster.Name,
				Namespace:          body.Cluster.Namespace,
				APIProxyURL:        body.Cluster.APIProxyURL,
				APIProxyForceHTTP:  body.Cluster.APIProxyForceHTTP,
				DisableAPIProxy:    body.Cluster.DisableAPIProxy,
				PreferPodProxy:     body.Cluster.PreferPodProxy,
				UsePortForward:     body.Cluster.UsePortForward,
				IngressDomain:      body.Cluster.IngressDomain,
				IngressClassName:   body.Cluster.IngressClassName,
				WorkspaceTLSSecret: body.Cluster.WorkspaceTLSSecret,
				CertManagerIssuer:  body.Cluster.CertManagerIssuer,
				IngressAuthURL:     body.Cluster.IngressAuthURL,
				IngressAuthSignin:  body.Cluster.IngressAuthSignin,
				ImagePullSecret:    body.Cluster.ImagePullSecret,
				OrgID:              body.Cluster.OrgID,
			}
			_ = setMgr.PutCluster(id, cs)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id})
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	// Settings CRUD (tailscale, database)
	mux.HandleFunc("/settings/tailscale", func(w http.ResponseWriter, r *http.Request) {
		if deps.DB == nil {
			httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"error": "unavailable"})
			return
		}
		if r.Method == http.MethodGet {
			var ts settings.Tailscale
			_ = setMgr.GetTailscale(&ts)
			_ = json.NewEncoder(w).Encode(ts)
			return
		}
		if r.Method == http.MethodPut {
			var ts settings.Tailscale
			_ = json.NewDecoder(r.Body).Decode(&ts)
			_ = setMgr.PutTailscale(ts)
			if deps.OnSettingsChanged != nil {
				deps.OnSettingsChanged("tailscale")
			}
			httpx.JSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	})

	// (published-services handling merged into per-cluster handler below)
	mux.HandleFunc("/settings/database", func(w http.ResponseWriter, r *http.Request) {
		if deps.DB == nil {
			httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"error": "unavailable"})
			return
		}
		if r.Method == http.MethodGet {
			var d settings.Database
			_ = setMgr.GetDatabase(&d)
			_ = json.NewEncoder(w).Encode(d)
			return
		}
		if r.Method == http.MethodPut {
			var d settings.Database
			_ = json.NewDecoder(r.Body).Decode(&d)
			_ = setMgr.PutDatabase(d)
			if deps.OnSettingsChanged != nil {
				deps.OnSettingsChanged("database")
			}
			// No-op: global DB manager removed in prototype
			httpx.JSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	})

	// Global settings CRUD
	mux.HandleFunc("/settings/global", func(w http.ResponseWriter, r *http.Request) {
		if deps.DB == nil {
			httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"error": "unavailable"})
			return
		}
		if r.Method == http.MethodGet {
			var g settings.Global
			_ = setMgr.GetGlobal(&g)
			_ = json.NewEncoder(w).Encode(g)
			return
		}
		if r.Method == http.MethodPut {
			var g settings.Global
			_ = json.NewDecoder(r.Body).Decode(&g)
			_ = setMgr.PutGlobal(g)
			if deps.OnSettingsChanged != nil {
				deps.OnSettingsChanged("global")
			}
			httpx.JSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	})

	// Per-cluster settings CRUD
	mux.HandleFunc("/settings/cluster/", func(w http.ResponseWriter, r *http.Request) {
		if deps.DB == nil {
			httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"error": "unavailable"})
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/settings/cluster/")
		if strings.TrimSpace(id) == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// Always use per-cluster DB via registry
		if deps.Registry == nil {
			httpx.JSONError(w, http.StatusServiceUnavailable, "registry not available", "no_registry")
			return
		}
		inst, err := deps.Registry.Get(r.Context(), id)
		if err != nil || inst == nil {
			httpx.JSONError(w, http.StatusNotFound, "cluster not found", "no_cluster")
			return
		}
		sm := settings.Manager{DB: inst.DB}
		if r.Method == http.MethodGet {
			var cs settings.Cluster
			_ = sm.GetCluster(id, &cs)
			// Debug: log raw presence for troubleshooting unexpected empty results
			if inst.DB != nil {
				var raw map[string]any
				if err := inst.DB.Get("cluster-settings", id, &raw); err != nil {
					log.Printf("settings: cluster GET id=%s raw=not_found err=%v", id, err)
				} else {
					log.Printf("settings: cluster GET id=%s raw_keys=%d", id, len(raw))
				}
			}
			_ = json.NewEncoder(w).Encode(cs)
			return
		}
		if r.Method == http.MethodPut {
			var cs settings.Cluster
			_ = json.NewDecoder(r.Body).Decode(&cs)
			if err := sm.PutCluster(id, cs); err != nil {
				log.Printf("settings: cluster PUT failed id=%s err=%v", id, err)
			} else {
				// Debug: confirm write by reading back raw record size
				if inst.DB != nil {
					var raw map[string]any
					if err := inst.DB.Get("cluster-settings", id, &raw); err == nil {
						log.Printf("settings: cluster PUT ok id=%s raw_keys=%d", id, len(raw))
					}
				}
			}
			// Persist cluster settings and notify runtime hooks
			if deps.OnSettingsChanged != nil {
				deps.OnSettingsChanged("cluster:" + id)
			}
			// Also write a cluster-scoped ConfigMap so in-cluster controllers (operator)
			// can pick up runtime preferences without access to host localdb.
			if inst.K8s != nil {
				cm := map[string]string{"workspace_lb_enabled": fmt.Sprintf("%v", cs.WorkspaceLBEnabled)}
				ns := "guildnet-system"
				// Ensure namespace exists
				_, _ = inst.K8s.K.CoreV1().Namespaces().Get(r.Context(), ns, metav1.GetOptions{})
				cfg := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Name: "guildnet-cluster-settings", Namespace: ns},
					Data:       cm,
				}
				if _, err := inst.K8s.K.CoreV1().ConfigMaps(ns).Get(r.Context(), "guildnet-cluster-settings", metav1.GetOptions{}); err == nil {
					_, _ = inst.K8s.K.CoreV1().ConfigMaps(ns).Update(r.Context(), cfg, metav1.UpdateOptions{})
				} else {
					_, _ = inst.K8s.K.CoreV1().ConfigMaps(ns).Create(r.Context(), cfg, metav1.CreateOptions{})
				}
			}
			httpx.JSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	})

	// Jobs: list and detail
	mux.HandleFunc("/api/jobs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(deps.Runner.List())
			return
		}
		if r.Method == http.MethodPost {
			if !authOK(w, r) {
				return
			}
			// Generic submit path: { kind, spec }
			var req struct {
				Kind string         `json:"kind"`
				Spec map[string]any `json:"spec"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if strings.TrimSpace(req.Kind) == "" {
				http.Error(w, "missing kind", http.StatusBadRequest)
				return
			}
			h := orch.HandlerFor(req.Kind, orch.Deps{DB: deps.DB, Secrets: deps.Secrets})
			jobID, _ := deps.Runner.Submit(req.Kind, req.Spec, h)
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"jobId": jobID})
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	mux.HandleFunc("/api/jobs/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/jobs/")
		if id == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodGet {
			rec := deps.Runner.Get(id)
			if rec == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(rec)
			return
		}
		if r.Method == http.MethodPost {
			if !authOK(w, r) {
				return
			}
			action := strings.TrimSpace(r.URL.Query().Get("action"))
			if action == "cancel" {
				deps.Runner.Cancel(id)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"ok":true}`))
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	// Job logs (raw NDJSON)
	mux.HandleFunc("/api/jobs-logs/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/jobs-logs/")
		if id == "" || deps.DB == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		b, _ := deps.DB.ReadLog("joblogs", id)
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(b)
	})
	// WS: /ws/jobs?id=...
	mux.HandleFunc("/ws/jobs", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if strings.TrimSpace(id) == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		ctx := r.Context()
		ch, cancel := deps.Runner.SubscribeLogs(id)
		defer cancel()
		go func() {
			<-ctx.Done()
			_ = c.Close(websocket.StatusNormalClosure, "bye")
		}()
		for e := range ch {
			b, _ := json.Marshal(e)
			if werr := c.Write(ctx, websocket.MessageText, b); werr != nil {
				break
			}
		}
	})

	// Audit list
	mux.HandleFunc("/api/audit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if deps.DB == nil {
			_ = json.NewEncoder(w).Encode([]any{})
			return
		}
		var items []map[string]any
		_ = deps.DB.List("audit", &items)
		_ = json.NewEncoder(w).Encode(items)
	})

	// Health summary
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := map[string]any{"headscale": []any{}, "clusters": []any{}}
		if deps.DB != nil {
			var hs []map[string]any
			_ = deps.DB.List("headscales", &hs)
			arrHS := make([]any, 0, len(hs))
			for _, h := range hs {
				id := fmt.Sprint(h["id"])
				endpoint := fmt.Sprint(h["endpoint"])
				st := map[string]any{"id": id, "status": "unknown"}
				if s, err := headscaleHealth(endpoint); err == nil {
					st["status"] = s
				}
				arrHS = append(arrHS, st)
			}
			resp["headscale"] = arrHS
			var cls []map[string]any
			_ = deps.DB.List("clusters", &cls)
			arrCL := make([]any, 0, len(cls))
			for _, c := range cls {
				id := fmt.Sprint(c["id"])
				name := fmt.Sprint(c["name"]) // include name for UI
				kc, ok := readClusterKubeconfig(deps.DB, deps.Secrets, id)
				st := map[string]any{"id": id, "name": name, "status": "unknown"}
				if !ok {
					st["code"] = "no_kubeconfig"
				} else {
					// Prefer registry-provided client (tsnet Dial) if available
					usedRegistry := false
					if deps.Registry != nil {
						if inst, err := deps.Registry.Get(r.Context(), id); err == nil && inst != nil && inst.K8s != nil {
							cfg2 := inst.K8s.Config()
							if err2 := healthyCluster(cfg2); err2 == nil {
								st["status"] = "ok"
								usedRegistry = true
							}
						}
					}
					if !usedRegistry {
						if cfg, err := kubeconfigFrom(kc); err == nil {
							// Apply per-cluster overrides and fallback to local proxy
							applyClusterAPIProxy(cfg, setMgr, id)
							if err := healthyCluster(cfg); err == nil {
								st["status"] = "ok"
							} else {
								st["status"] = "error"
								st["code"] = "cluster_unreachable"
								st["error"] = err.Error()
							}
						} else {
							st["status"] = "error"
							st["code"] = "bad_kubeconfig"
							st["error"] = err.Error()
						}
					}
				}
				arrCL = append(arrCL, st)
			}
			resp["clusters"] = arrCL
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	// Headscale
	mux.HandleFunc("/api/deploy/headscale", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			var items []map[string]any
			if deps.DB != nil {
				_ = deps.DB.List("headscales", &items)
			}
			_ = json.NewEncoder(w).Encode(items)
			return
		case http.MethodPost:
			if !authOK(w, r) {
				return
			}
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			name := strings.TrimSpace(fmt.Sprint(req["name"]))
			if name == "" {
				name = fmt.Sprintf("hs-%s", uuid.NewString()[:8])
			}
			id := uuid.NewString()
			rec := map[string]any{
				"id":        id,
				"name":      name,
				"type":      "managed",
				"state":     "creating",
				"createdAt": time.Now().UTC().Format(time.RFC3339),
			}
			if deps.DB != nil {
				_ = deps.DB.Put("headscales", id, rec)
			}
			h := orch.HandlerFor("headscale.create", orch.Deps{DB: deps.DB, Secrets: deps.Secrets})
			jobID, _ := deps.Runner.Submit("headscale.create", rec, h)
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "jobId": jobID})
			return
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/deploy/headscale/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/deploy/headscale/")
		if id == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodGet {
			var rec map[string]any
			if deps.DB == nil || deps.DB.Get("headscales", id, &rec) != nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(rec)
			return
		}
		if r.Method == http.MethodDelete {
			if !authOK(w, r) {
				return
			}
			if deps.DB != nil {
				_ = deps.DB.Delete("headscales", id)
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"deleted": id})
			return
		}
		if r.Method == http.MethodPost {
			if !authOK(w, r) {
				return
			}
			action := strings.TrimSpace(r.URL.Query().Get("action"))
			if action == "" {
				action = strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/deploy/headscale/"+id), "/")
			}
			// Special sub-actions for MVP: endpoint, preauth-key, health
			if action == "endpoint" {
				var body struct {
					Endpoint string `json:"endpoint"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body.Endpoint == "" {
					http.Error(w, "missing endpoint", http.StatusBadRequest)
					return
				}
				var rec map[string]any
				if deps.DB == nil || deps.DB.Get("headscales", id, &rec) != nil {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				rec["endpoint"] = body.Endpoint
				rec["updatedAt"] = time.Now().UTC().Format(time.RFC3339)
				_ = deps.DB.Put("headscales", id, rec)
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
				return
			}
			if action == "preauth-key" {
				var body struct {
					Value string `json:"value"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				if strings.TrimSpace(body.Value) == "" {
					http.Error(w, "missing value", http.StatusBadRequest)
					return
				}
				enc := body.Value
				if deps.Secrets != nil {
					if v, err := deps.Secrets.Encrypt(body.Value); err == nil {
						enc = v
					}
				}
				cred := map[string]any{
					"id":        uuid.NewString(),
					"scopeType": "headscale",
					"scopeId":   id,
					"kind":      "headscale.preauth",
					"value":     enc,
					"rotatedAt": time.Now().UTC().Format(time.RFC3339),
				}
				if deps.DB != nil {
					_ = deps.DB.Put("credentials", fmt.Sprintf("hs:%s:preauth", id), cred)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
				return
			}
			if action == "health" {
				var rec map[string]any
				if deps.DB == nil || deps.DB.Get("headscales", id, &rec) != nil {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				endpoint := fmt.Sprint(rec["endpoint"])
				status := map[string]any{"status": "unknown"}
				if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
					addr := u.Host
					if !strings.Contains(addr, ":") {
						if u.Scheme == "https" {
							addr = addr + ":443"
						} else {
							addr = addr + ":80"
						}
					}
					c, err := net.DialTimeout("tcp", addr, 1*time.Second)
					if err == nil {
						_ = c.Close()
						status["status"] = "ok"
					} else {
						status["error"] = err.Error()
					}
				}
				_ = json.NewEncoder(w).Encode(status)
				return
			}
			kind := "headscale." + action
			h := orch.HandlerFor(kind, orch.Deps{DB: deps.DB, Secrets: deps.Secrets})
			jobID, _ := deps.Runner.Submit(kind, map[string]string{"id": id}, h)
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"jobId": jobID})
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	})

	// Clusters
	mux.HandleFunc("/api/deploy/clusters", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			var items []map[string]any
			if deps.DB != nil {
				_ = deps.DB.List("clusters", &items)
				// For each cluster, prefer the human-friendly name from per-cluster settings
				for _, it := range items {
					// try to read per-cluster settings
					if idv, ok := it["id"]; ok {
						id := fmt.Sprint(idv)
						var cs settings.Cluster
						if err := setMgr.GetCluster(id, &cs); err == nil {
							if strings.TrimSpace(cs.Name) != "" {
								// prefer settings name over stored record name
								it["name"] = cs.Name
							}
						}
					}
				}
			}
			_ = json.NewEncoder(w).Encode(items)
			return
		case http.MethodPost:
			if !authOK(w, r) {
				return
			}
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			name := strings.TrimSpace(fmt.Sprint(req["name"]))
			if name == "" {
				name = fmt.Sprintf("cluster-%s", uuid.NewString()[:8])
			}
			id := uuid.NewString()
			rec := map[string]any{
				"id":        id,
				"name":      name,
				"state":     "creating",
				"createdAt": time.Now().UTC().Format(time.RFC3339),
			}
			if deps.DB != nil {
				_ = deps.DB.Put("clusters", id, rec)
			}
			h := orch.HandlerFor("cluster.create", orch.Deps{DB: deps.DB, Secrets: deps.Secrets})
			jobID, _ := deps.Runner.Submit("cluster.create", rec, h)
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "jobId": jobID})
			return
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/deploy/clusters/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/deploy/clusters/")
		if id == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodGet {
			var rec map[string]any
			if deps.DB == nil || deps.DB.Get("clusters", id, &rec) != nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(rec)
			return
		}
		if r.Method == http.MethodDelete {
			if !authOK(w, r) {
				return
			}
			if deps.DB != nil {
				_ = deps.DB.Delete("clusters", id)
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"deleted": id})
			return
		}
		if r.Method == http.MethodPost {
			if !authOK(w, r) {
				return
			}
			action := strings.TrimSpace(r.URL.Query().Get("action"))
			if action == "" {
				action = strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/deploy/clusters/"+id), "/")
			}
			if action == "attach-kubeconfig" {
				var body struct {
					Kubeconfig string `json:"kubeconfig"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				if strings.TrimSpace(body.Kubeconfig) == "" {
					httpx.JSONError(w, http.StatusBadRequest, "missing kubeconfig", "missing_kubeconfig")
					return
				}
				// Validate kubeconfig before storing
				if _, err := kubeconfigFrom(body.Kubeconfig); err != nil {
					httpx.JSONError(w, http.StatusBadRequest, "invalid kubeconfig", "bad_kubeconfig", err.Error())
					return
				}
				// Compute deterministic cluster id from kubeconfig so the same cluster
				// yields the same id on every device.
				detID, err := cluster.DeterministicIDFromKubeconfig(body.Kubeconfig)
				if err == nil && detID != "" {
					id = detID
				}
				// Ensure a cluster record exists for this deterministic id so that UIs/agents
				// can reference the same cluster across devices even when bootstrap wasn't used.
				if deps.DB != nil {
					var rec map[string]any
					if derr := deps.DB.Get("clusters", id, &rec); derr != nil || len(rec) == 0 {
						rec = map[string]any{
							"id":        id,
							"name":      id,
							"state":     "imported",
							"createdAt": time.Now().UTC().Format(time.RFC3339),
						}
						_ = deps.DB.Put("clusters", id, rec)
					}
				}
				enc := body.Kubeconfig
				encrypted := false
				if deps.Secrets != nil {
					if v, err := deps.Secrets.Encrypt(body.Kubeconfig); err == nil {
						enc = v
						encrypted = true
					}
				}
				cred := map[string]any{
					"id":        uuid.NewString(),
					"scopeType": "cluster",
					"scopeId":   id,
					"kind":      "cluster.kubeconfig",
					"value":     enc,
					"encrypted": encrypted,
					"rotatedAt": time.Now().UTC().Format(time.RFC3339),
				}
				if deps.DB != nil {
					_ = deps.DB.Put("credentials", fmt.Sprintf("cl:%s:kubeconfig", id), cred)
				}
				// Mark cluster ready if reachable
				if cfg, err := kubeconfigFrom(body.Kubeconfig); err == nil {
					if healthyCluster(cfg) == nil {
						var rec map[string]any
						if deps.DB.Get("clusters", id, &rec) == nil {
							rec["state"] = "ready"
							rec["updatedAt"] = time.Now().UTC().Format(time.RFC3339)
							_ = deps.DB.Put("clusters", id, rec)
						}
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "id": id})
				return
			}

			if action == "join-config" {
				// Build a join config JSON similar to scripts/generate_join_config.sh output.
				out := map[string]any{}
				out["version"] = 2
				out["created_at"] = time.Now().UTC().Format(time.RFC3339)
				out["creator"] = map[string]any{"host": r.Host, "user": ""}

				// Hostapp/ui base URL: prefer X-Forwarded-Proto/Host if present, otherwise infer from request
				scheme := "http"
				if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
					scheme = "https"
				}
				host := r.Host
				if host == "" {
					host = "127.0.0.1:8090"
				}
				ui := map[string]any{"vite_api_base": fmt.Sprintf("%s://%s", scheme, host)}
				out["ui"] = ui
				out["hostapp"] = map[string]any{"url": fmt.Sprintf("%s://%s", scheme, host)}

				// Include CAPem if available from repo certs/server.crt
				if data, err := os.ReadFile("certs/server.crt"); err == nil {
					out["ui"].(map[string]any)["ca_pem"] = string(data)
					out["hostapp"].(map[string]any)["ca_pem"] = string(data)
				}

				// Cluster section
				clusterRec := map[string]any{"name": "", "kubeconfig": "", "notes": ""}
				if kc, ok := readClusterKubeconfig(deps.DB, deps.Secrets, id); ok {
					clusterRec["kubeconfig"] = kc
				}
				// include per-cluster settings
				var cs settings.Cluster
				_ = setMgr.GetCluster(id, &cs)
				if cs.Name != "" {
					clusterRec["name"] = cs.Name
				}
				if cs.Namespace != "" {
					clusterRec["namespace"] = cs.Namespace
				}
				if cs.APIProxyURL != "" {
					clusterRec["api_proxy_url"] = cs.APIProxyURL
				}
				if cs.APIProxyForceHTTP {
					clusterRec["api_proxy_force_http"] = true
				}
				if cs.DisableAPIProxy {
					clusterRec["disable_api_proxy"] = true
				}
				if cs.PreferPodProxy {
					clusterRec["prefer_pod_proxy"] = true
				}
				if cs.UsePortForward {
					clusterRec["use_port_forward"] = true
				}
				if cs.IngressDomain != "" {
					clusterRec["ingress_domain"] = cs.IngressDomain
				}
				if cs.IngressClassName != "" {
					clusterRec["ingress_class_name"] = cs.IngressClassName
				}
				if cs.WorkspaceTLSSecret != "" {
					clusterRec["workspace_tls_secret"] = cs.WorkspaceTLSSecret
				}
				if cs.CertManagerIssuer != "" {
					clusterRec["cert_manager_issuer"] = cs.CertManagerIssuer
				}
				// Expose canonical cluster id and include detailed cluster object for compatibility
				clusterRec["id"] = id
				out["cluster"] = clusterRec
				out["clusterId"] = id

				// Tailscale hints
				var ts settings.Tailscale
				_ = setMgr.GetTailscale(&ts)
				tails := map[string]any{"login_server": ts.LoginServer, "preauth_key": ts.PreauthKey, "hostname": ts.Hostname}
				// If per-cluster TS client auth key stored in credentials, include it
				if deps.DB != nil {
					var cred map[string]any
					if deps.DB.Get("credentials", fmt.Sprintf("cl:%s:ts_client_auth", id), &cred) == nil {
						if v, ok := cred["value"].(string); ok && strings.TrimSpace(v) != "" {
							tails["preauth_key"] = v
						}
					}
				}
				out["tailscale"] = tails

				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(out)
				return
			}
			if action == "health" {
				// Check and report reachability of this cluster
				kc, ok := readClusterKubeconfig(deps.DB, deps.Secrets, id)
				if !ok {
					_ = json.NewEncoder(w).Encode(map[string]any{"status": "unknown", "code": "no_kubeconfig"})
					return
				}
				cfg, err := kubeconfigFrom(kc)
				if err != nil {
					_ = json.NewEncoder(w).Encode(map[string]any{"status": "unknown", "code": "bad_kubeconfig", "error": err.Error()})
					return
				}
				// Apply per-cluster overrides (no local-proxy fallbacks)
				applyClusterAPIProxy(cfg, setMgr, id)
				if err := healthyCluster(cfg); err == nil {
					_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "code": "cluster_unreachable"})
				return
			}
			if action == "kubeconfig" {
				kc, ok := readClusterKubeconfig(deps.DB, deps.Secrets, id)
				if !ok {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/x-yaml")
				_, _ = io.WriteString(w, kc)
				return
			}
			kind := "cluster." + action
			h := orch.HandlerFor(kind, orch.Deps{DB: deps.DB, Secrets: deps.Secrets})
			jobID, _ := deps.Runner.Submit(kind, map[string]string{"id": id}, h)
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"jobId": jobID})
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	})

	// UI config for runtime overrides
	mux.HandleFunc("/ui-config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{})
	})

	// Per-cluster scoped APIs: /api/cluster/:id/servers, /workspaces, etc.
	mux.HandleFunc("/api/cluster/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/cluster/")
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) == 0 || parts[0] == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// Resolve well-known aliases (e.g., "default") when possible so
		// out-of-the-box scripts can target a stable id without knowing the
		// deterministic cluster ID. If a single cluster record exists, map to it.
		clusterID := resolveClusterIDAlias(deps.DB, parts[0])
		// Special-case: published-services endpoints
		if len(parts) >= 2 && parts[1] == "published-services" {
			// GET /api/cluster/{id}/published-services
			if r.Method == http.MethodGet && len(parts) == 2 {
				var list []localdb.PublishedService
				if deps.DB != nil {
					if err := deps.DB.ListPublished(&list); err != nil {
						log.Printf("api: list published services db error: %v", err)
						httpx.JSONError(w, http.StatusInternalServerError, "db_error", "list_published", err.Error())
						return
					}
				}
				out := []localdb.PublishedService{}
				for _, p := range list {
					if p.ClusterID == clusterID {
						out = append(out, p)
					}
				}
				_ = json.NewEncoder(w).Encode(out)
				return
			}
			// DELETE /api/cluster/{id}/published-services/{service}
			if r.Method == http.MethodDelete && len(parts) >= 3 {
				service := parts[2]
				key := clusterID + ":" + service
				publishedMapMu.Lock()
				pl, ok := publishedMap[key]
				if ok && pl != nil {
					_ = pl.ln.Close()
					publishedMapMu.Unlock()
					w.WriteHeader(http.StatusOK)
					return
				}
				publishedMapMu.Unlock()
				if deps.DB != nil {
					if err := deps.DB.DeletePublished(key); err != nil {
						log.Printf("api: delete published db error key=%s err=%v", key, err)
						httpx.JSONError(w, http.StatusInternalServerError, "db_error", "delete_published", err.Error())
						return
					}
				}
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		// Quick overview endpoint: /api/cluster/{id}/overview
		if len(parts) >= 2 && parts[1] == "overview" {
			if r.Method != http.MethodGet {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			out := map[string]any{"clusterId": clusterID}
			// cluster record from host DB if present
			if deps.DB != nil {
				var crec map[string]any
				if err := deps.DB.Get("clusters", clusterID, &crec); err == nil && len(crec) > 0 {
					out["record"] = crec
				}
			}
			// devices from per-cluster DB when Instance available
			if deps.Registry != nil {
				if inst, err := deps.Registry.Get(r.Context(), clusterID); err == nil && inst != nil && inst.DB != nil {
					var devices []map[string]any
					if err := inst.DB.List("devices", &devices); err == nil {
						out["sites"] = devices
					}
					// federated services via dynamic client when available
					if inst.Dyn != nil {
						gvr := schema.GroupVersionResource{Group: "guildnet.io", Version: "v1alpha1", Resource: "federatedservices"}
						if ulist, err := inst.Dyn.Resource(gvr).Namespace(metav1.NamespaceAll).List(r.Context(), metav1.ListOptions{}); err == nil {
							outFS := []map[string]any{}
							for _, it := range ulist.Items {
								outFS = append(outFS, map[string]any{"namespace": it.GetNamespace(), "name": it.GetName()})
							}
							out["federatedServices"] = outFS
						}
					}
				}
			}
			_ = json.NewEncoder(w).Encode(out)
			return
		}
		// Quick status endpoint for UI: /api/cluster/{id}/status
		if len(parts) >= 2 && parts[1] == "status" && r.Method == http.MethodGet {
			st, err := clusterLocalStatus(r.Context(), deps, clusterID)
			if err != nil {
				httpx.JSONError(w, http.StatusInternalServerError, "status error", "status_error", err.Error())
				return
			}
			httpx.JSON(w, http.StatusOK, st)
			return
		}
		// Optionally resolve via per-cluster registry
		var (
			cfg         *rest.Config
			restCfg     *rest.Config
			cli         kubernetes.Interface
			dyn         dynamic.Interface
			cs          settings.Cluster
			setMgrLocal settings.Manager
			regInst     *cluster.Instance
		)
		setMgrLocal = setMgr
		if deps.Registry != nil {
			if inst, err := deps.Registry.Get(r.Context(), clusterID); err == nil && inst != nil {
				// capture registry instance for possible port-forward / ts publish
				regInst = inst
				// Use per-cluster DB for settings
				setMgrLocal = settings.Manager{DB: inst.DB}
				// Build clients from instance
				if inst.K8s != nil {
					cfg = inst.K8s.Config()
					restCfg = cfg
				}
				// Apply proxy overrides
				applyClusterAPIProxy(cfg, setMgrLocal, clusterID)
				// Clients
				// Reuse cached client if present; otherwise build only when we have a rest.Config.
				if inst.K8s != nil && inst.K8s.K != nil {
					cli = inst.K8s.K
				} else if cfg != nil {
					if c, e := kubernetes.NewForConfig(cfg); e == nil {
						cli = c
					} else {
						log.Printf("cluster: registry NewForConfig failed (will fallback) id=%s err=%v", clusterID, e)
					}
				}
				// Reuse cached dynamic client if available; otherwise build only when we have a rest.Config.
				if inst.Dyn != nil {
					dyn = inst.Dyn
				} else if cfg != nil {
					if d, e := dynamic.NewForConfig(cfg); e == nil {
						dyn = d
					} else {
						log.Printf("cluster: registry dynamic.NewForConfig failed (will fallback) id=%s err=%v", clusterID, e)
					}
				}
			}
		}
		// If registry didn't provide clients, attempt a best-effort fallback by
		// reading the kubeconfig from the main DB and building clients directly.
		if cli == nil || dyn == nil {
			log.Printf("cluster: clients not provided by registry for id=%s; attempting fallback", clusterID)
			// Try main DB kubeconfig (legacy path)
			if deps.DB != nil {
				if kc, ok := readClusterKubeconfig(deps.DB, deps.Secrets, clusterID); ok {
					log.Printf("cluster: found kubeconfig in main DB for id=%s; trying to build clients", clusterID)
					if cfg2, err := kubeconfigFrom(kc); err == nil && cfg2 != nil {
						// apply any per-cluster API proxy overrides (uses main DB for settings)
						applyClusterAPIProxy(cfg2, setMgrLocal, clusterID)
						if c, e := kubernetes.NewForConfig(cfg2); e == nil {
							cli = c
							log.Printf("cluster: built kubernetes client from main kubeconfig for id=%s", clusterID)
						}
						if d, e := dynamic.NewForConfig(cfg2); e == nil {
							dyn = d
							log.Printf("cluster: built dynamic client from main kubeconfig for id=%s", clusterID)
						}
						// remember effective rest config for downstream transports
						restCfg = cfg2
						// Strict mode: no local proxy fallbacks; require direct connectivity
					}
				}
			}
		}
		// If we still don't have clients, be tolerant for read-only endpoints
		// but for mutating operations (workspace create) return an explicit
		// error so callers know to attach a kubeconfig or wait.
		if cli == nil || dyn == nil {
			// For GET/list/servers endpoints we return an empty list (keeps UI usable).
			if r.Method == http.MethodGet {
				httpx.JSON(w, http.StatusOK, []any{})
				return
			}
			// For mutating requests return 503 with a helpful message
			httpx.JSONError(w, http.StatusServiceUnavailable, "kubernetes clients not available for cluster; attach kubeconfig or wait for cluster initialization", "no_k8s_clients", "kube clients not available for cluster id")
			log.Printf("cluster: mutating request but kube clients missing for id=%s", clusterID)
			return
		}
		// Fetch per-cluster settings to derive default namespace using (possibly) per-cluster DB
		_ = setMgrLocal.GetCluster(clusterID, &cs)
		defaultNS := strings.TrimSpace(cs.Namespace)
		if defaultNS == "" {
			defaultNS = "default"
		}
		// Proxy: /api/cluster/{id}/proxy/server/{name}/...
		if len(parts) >= 3 && parts[1] == "proxy" && parts[2] == "server" {
			if len(parts) < 4 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			name := parts[3]
			restPath := "/"
			if len(parts) > 4 {
				restPath = "/" + strings.Join(parts[4:], "/")
			}
			// Determine service port (first port as default)
			port := 0
			if svc, err := cli.CoreV1().Services(defaultNS).Get(r.Context(), name, metav1.GetOptions{}); err == nil {
				if len(svc.Spec.Ports) > 0 {
					port = int(svc.Spec.Ports[0].Port)
				}
			}
			if port == 0 {
				port = 80
			}
			// Check endpoints to decide whether to prefer port-forward fallback
			preferPF := cs.PreferPodProxy || cs.UsePortForward
			endpointsMissing := false
			if !preferPF {
				if eps, err := cli.CoreV1().Endpoints(defaultNS).Get(r.Context(), name, metav1.GetOptions{}); err != nil || eps == nil || len(eps.Subsets) == 0 {
					endpointsMissing = true
				}
			}
			// Build API transport to kube-apiserver when restCfg available; otherwise skip API proxy path
			var (
				rt      http.RoundTripper
				apihost *url.URL
			)
			if restCfg != nil {
				if t, err := rest.TransportFor(restCfg); err == nil {
					rt = t
					apihost, _ = url.Parse(restCfg.Host)
				} else {
					log.Printf("cluster: k8s transport build failed for cluster=%s err=%v", clusterID, err)
				}
			} else {
				log.Printf("cluster: rest config is nil for cluster=%s; skipping API proxy transport", clusterID)
			}
			// If endpoints are missing or cluster prefers pod proxy, try to port-forward and publish via tsnet
			if (preferPF || endpointsMissing) && regInst != nil && regInst.PF != nil {
				// If the kube API host is not reachable directly, but we have a tsnet connector,
				// use a tsnet-backed transport so port-forward and pod listing work even when
				// cfg.Host points at localhost or a non-routable address.
				if restCfg != nil && regInst.TS != nil {
					// quick probe
					host := strings.TrimPrefix(restCfg.Host, "https://")
					host = strings.TrimPrefix(host, "http://")
					// strip path
					if idx := strings.Index(host, "/"); idx >= 0 {
						host = host[:idx]
					}
					dialer := func() error {
						d := net.Dialer{Timeout: 1500 * time.Millisecond}
						conn, err := d.DialContext(r.Context(), "tcp", host)
						if err != nil {
							return err
						}
						_ = conn.Close()
						return nil
					}
					if err := dialer(); err != nil {
						log.Printf("cluster: kube api host %s unreachable directly; using tsnet transport for cluster=%s", host, clusterID)
						// create an http.Transport via tsnet connector for downstream use; replace rt if possible
						if rt != nil {
							if ht, ok := rt.(*http.Transport); ok {
								// wrap transport with tsnet dial
								tt := regInst.TS.HTTPTransport(ht)
								rt = tt
							} else {
								// create a minimal transport
								tt := regInst.TS.HTTPTransport(nil)
								rt = tt
							}
						}
					}
				}
				// Build a label selector from the Service's spec.selector if present
				selector := ""
				if svc, err := cli.CoreV1().Services(defaultNS).Get(r.Context(), name, metav1.GetOptions{}); err == nil {
					if len(svc.Spec.Selector) > 0 {
						// Build a comma-separated label selector
						parts := []string{}
						for k, v := range svc.Spec.Selector {
							parts = append(parts, fmt.Sprintf("%s=%s", k, v))
						}
						selector = strings.Join(parts, ",")
					}
				}
				if selector == "" {
					// Fallback heuristic: try app=<serviceName>
					selector = fmt.Sprintf("app=%s", name)
				}
				log.Printf("cluster: trying port-forward fallback cluster=%s service=%s selector=%s", clusterID, name, selector)
				pods, err := cli.CoreV1().Pods(defaultNS).List(r.Context(), metav1.ListOptions{LabelSelector: selector})
				if err == nil && len(pods.Items) > 0 {
					// Prefer ready pods if possible
					podName := pods.Items[0].Name
					for _, p := range pods.Items {
						for _, cs := range p.Status.ContainerStatuses {
							if cs.Ready {
								podName = p.Name
								break
							}
						}
					}
					log.Printf("cluster: selected pod %s for service %s (cluster=%s)", podName, name, clusterID)
					lp, err := regInst.PF.Ensure(r.Context(), defaultNS, podName, port)
					if err == nil && lp > 0 {
						log.Printf("cluster: started port-forward cluster=%s pod=%s localPort=%d", clusterID, podName, lp)
						// If tsnet connector available, publish the local port so tailnet nodes can reach it
						if regInst.TS != nil {
							key := clusterID + ":" + name
							publishedMapMu.Lock()
							pl, exists := publishedMap[key]
							if exists && pl != nil {
								// already published; reuse
								log.Printf("cluster: reuse existing published listener for cluster=%s service=%s", clusterID, name)
							} else {
								ln, lerr := regInst.TS.Listen("tcp", fmt.Sprintf(":%d", lp))
								if lerr != nil {
									publishedMapMu.Unlock()
									log.Printf("cluster: ts publish listen failed cluster=%s port=%d err=%v", clusterID, lp, lerr)
								} else {
									pl = &publishedListener{clusterID: clusterID, service: name, addr: ln.Addr().String(), ln: ln, addedAt: time.Now()}
									publishedMap[key] = pl
									// persist mapping
									if deps.DB != nil {
										ps := localdb.PublishedService{ClusterID: clusterID, Service: name, Addr: pl.addr, AddedAt: pl.addedAt}
										if err := deps.DB.SavePublished(key, ps); err != nil {
											log.Printf("cluster: failed to persist published mapping key=%s err=%v", key, err)
										}
									}
									publishedMapMu.Unlock()
									log.Printf("cluster: published port %d via tsnet for cluster=%s service=%s addr=%s", lp, clusterID, name, pl.addr)
									// accept loop
									go func(pl *publishedListener, lp int) {
										defer func() {
											pl.ln.Close()
											publishedMapMu.Lock()
											delete(publishedMap, key)
											publishedMapMu.Unlock()
											// remove persisted mapping
											if deps.DB != nil {
												if err := deps.DB.DeletePublished(key); err != nil {
													log.Printf("cluster: failed to delete persisted published mapping key=%s err=%v", key, err)
												}
											}
											log.Printf("cluster: published listener closed cluster=%s service=%s", clusterID, name)
										}()
										for {
											conn, err := pl.ln.Accept()
											if err != nil {
												log.Printf("cluster: tsnet accept error cluster=%s err=%v", clusterID, err)
												return
											}
											// Proxy accepted tsnet connection to local loopback port
											go func(c net.Conn, lp int) {
												defer c.Close()
												dst, dErr := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", lp))
												if dErr != nil {
													log.Printf("cluster: ts proxy dial failed lp=%d err=%v", lp, dErr)
													return
												}
												defer dst.Close()
												// bidirectional copy
												go func() { _, _ = io.Copy(dst, c); _ = dst.Close() }()
												_, _ = io.Copy(c, dst)
											}(conn, lp)
										}
									}(pl, lp)
								}
							}
							if exists {
								publishedMapMu.Unlock()
							}
						}
						// Rewrite target to local loopback address and skip API proxy
						r2 := r.Clone(r.Context())
						r2.URL = new(url.URL)
						*r2.URL = *r.URL
						r2.URL.Path = "/"
						// Use direct local target
						p2 := proxy.NewReverseProxy(proxy.Options{
							Timeout: 60 * time.Second,
							Dial: func(ctx context.Context, network, address string) (any, error) {
								// Connect to local loopback
								return net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", lp))
							},
						})
						// Ensure forwarded prefix header remains so iframe rewriting works
						if r2.Header == nil {
							r2.Header = make(http.Header)
						}
						r2.Header.Set("X-Forwarded-Prefix", "/api/cluster/"+clusterID+"/proxy/server/"+name)
						p2.ServeHTTP(w, r2)
						return
					}
				} else {
					log.Printf("cluster: no pods found for selector=%s service=%s cluster=%s err=%v", selector, name, clusterID, err)
				}
			}

			opts := proxy.Options{
				Timeout: 60 * time.Second,
				// Enable logging for cluster-scoped proxy so we can capture upstream headers and transport errors
				Logger: httpx.Logger(),
				ResolveServer: func(ctx context.Context, serverID string, subPath string) (string, string, string, error) {
					// Explicitly include http: scheme segment for kube API service proxy
					p := "/api/v1/namespaces/" + defaultNS + "/services/http:" + name + ":" + fmt.Sprintf("%d", port) + "/proxy" + subPath
					return "http", "", p, nil
				},
			}
			// Only enable API proxying when we have a valid transport and API host
			if rt != nil && apihost != nil {
				opts.APIProxy = func() (http.RoundTripper, func(req *http.Request, scheme, hostport, p string), bool) {
					return rt, func(req *http.Request, scheme, hostport, pth string) {
						// Honor any base path present on the API host (env override or kubeconfig)
						basePath := strings.TrimSuffix(apihost.Path, "/")
						req.URL.Scheme = apihost.Scheme
						req.URL.Host = apihost.Host
						req.Host = apihost.Host
						req.URL.Path = basePath + pth
					}, true
				}
			}
			rp := proxy.NewReverseProxy(opts)
			// Rewrite path to start at /proxy/server/{name}/...
			r2 := r.Clone(r.Context())
			r2.URL = new(url.URL)
			*r2.URL = *r.URL
			r2.URL.Path = "/proxy/server/" + url.PathEscape(name) + restPath
			// Preserve outer prefix for iframe-safe rewriting (Location, cookies)
			if r2.Header == nil {
				r2.Header = make(http.Header)
			}
			r2.Header.Set("X-Forwarded-Prefix", "/api/cluster/"+clusterID+"/proxy/server/"+name)
			rp.ServeHTTP(w, r2)
			return
		}
		if len(parts) == 2 && parts[1] == "servers" {
			if r.Method != http.MethodGet {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			gvr := schema.GroupVersionResource{Group: "guildnet.io", Version: "v1alpha1", Resource: "workspaces"}
			lst, err := dyn.Resource(gvr).Namespace(defaultNS).List(r.Context(), metav1.ListOptions{})
			if err != nil {
				httpx.JSON(w, http.StatusOK, []any{})
				return
			}
			// Build a quick lookup of device participants to enrich with machine identity.
			// Key by lowercased device name/id for loose matching against node names.
			deviceMap := map[string]struct {
				name string
				ips  []string
			}{}
			if dyn != nil {
				if ulist, err := dyn.Resource(schema.GroupVersionResource{Group: "guildnet.io", Version: "v1alpha1", Resource: "deviceparticipants"}).Namespace("guildnet-system").List(r.Context(), metav1.ListOptions{}); err == nil {
					for _, it := range ulist.Items {
						m := it.Object
						nameKey := strings.ToLower(strings.TrimSpace(it.GetName()))
						// spec.name is the human hostname when provided
						if sp, ok := m["spec"].(map[string]any); ok {
							if v, ok2 := sp["name"]; ok2 {
								if s := strings.ToLower(strings.TrimSpace(fmt.Sprint(v))); s != "" {
									nameKey = s
								}
							}
							ips := []string{}
							if v, ok2 := sp["tailnetIPs"]; ok2 {
								if arr, ok3 := v.([]any); ok3 {
									for _, a := range arr {
										ips = append(ips, strings.TrimSpace(fmt.Sprint(a)))
									}
								}
							}
							deviceMap[nameKey] = struct {
								name string
								ips  []string
							}{name: strings.TrimSpace(fmt.Sprint(sp["name"])), ips: ips}
						}
					}
				}
			}
			// map to Server model (local, keep fields minimal)
			type Port struct {
				Name string `json:"name,omitempty"`
				Port int    `json:"port"`
			}
			type Server struct {
				ID          string   `json:"id"`
				Name        string   `json:"name"`
				Image       string   `json:"image"`
				Status      string   `json:"status"`
				Ports       []Port   `json:"ports"`
				Node        string   `json:"node,omitempty"`
				MachineName string   `json:"machineName,omitempty"`
				TailnetIPs  []string `json:"tailnetIPs,omitempty"`
			}
			out := []Server{}
			for _, item := range lst.Items {
				obj := item.Object
				meta := obj["metadata"].(map[string]any)
				spec := obj["spec"].(map[string]any)
				status, _ := obj["status"].(map[string]any)
				name := fmt.Sprint(meta["name"])
				image := fmt.Sprint(spec["image"])
				phase, _ := status["phase"].(string)
				readyReplicas := 0
				if rr, ok := status["readyReplicas"].(int64); ok {
					readyReplicas = int(rr)
				}
				st := "pending"
				if phase == "Running" && readyReplicas > 0 {
					st = "running"
				} else if phase == "Failed" {
					st = "failed"
				}
				ports := []Port{}
				if raw, ok := spec["ports"].([]any); ok {
					for _, rp := range raw {
						if pm, ok := rp.(map[string]any); ok {
							pnum := 0
							if pv, ok := pm["containerPort"].(int64); ok {
								pnum = int(pv)
							} else if pvf, ok := pm["containerPort"].(float64); ok {
								pnum = int(pvf)
							}
							if pnum > 0 {
								ports = append(ports, Port{Name: strings.TrimSpace(fmt.Sprint(pm["name"])), Port: pnum})
							}
						}
					}
				}
				// Attempt to determine the node/pod location for this workspace
				nodeName := ""
				if cli != nil {
					if pods, err := cli.CoreV1().Pods(defaultNS).List(r.Context(), metav1.ListOptions{LabelSelector: fmt.Sprintf("guildnet.io/workspace=%s", name)}); err == nil {
						for _, p := range pods.Items {
							if strings.TrimSpace(p.Spec.NodeName) != "" {
								nodeName = p.Spec.NodeName
								break
							}
						}
					}
				}
				machineName := ""
				ips := []string{}
				if nodeName != "" {
					if rec, ok := deviceMap[strings.ToLower(nodeName)]; ok {
						machineName = rec.name
						ips = rec.ips
					} else {
						// Fallback: use node name when no DeviceParticipant matches
						machineName = nodeName
					}
				}
				out = append(out, Server{ID: name, Name: name, Image: image, Status: st, Ports: ports, Node: nodeName, MachineName: machineName, TailnetIPs: ips})
			}
			httpx.JSON(w, http.StatusOK, out)
			return
		}
		if len(parts) >= 2 && parts[1] == "workspaces" {
			gvr := schema.GroupVersionResource{Group: "guildnet.io", Version: "v1alpha1", Resource: "workspaces"}
			if len(parts) == 2 && r.Method == http.MethodPost {
				// auth for mutating
				if r.Method != http.MethodGet {
					if deps.Token != "" || true { // enforce localhost-or-token via authOK equivalent
						// simple check mimicking authOK: allow only localhost if no token
						host, _, _ := net.SplitHostPort(r.RemoteAddr)
						ip := net.ParseIP(host)
						if strings.TrimSpace(deps.Token) != "" {
							// require header token match
							authz := r.Header.Get("Authorization")
							if !strings.HasPrefix(strings.ToLower(authz), "bearer ") || strings.TrimSpace(authz[7:]) != strings.TrimSpace(deps.Token) {
								http.Error(w, "unauthorized", http.StatusUnauthorized)
								return
							}
						} else if !(ip != nil && (ip.IsLoopback() || host == "127.0.0.1" || host == "::1")) {
							http.Error(w, "unauthorized", http.StatusUnauthorized)
							return
						}
					}
				}
				var spec map[string]any
				_ = json.NewDecoder(r.Body).Decode(&spec)
				// expect { image, name?, env?, ports?, args?, resources?, labels? }
				// Avoid fmt.Sprint on nil which prints "<nil>"; only use string when present.
				var name string
				if v, ok := spec["name"]; ok && v != nil {
					if s, ok := v.(string); ok {
						name = strings.TrimSpace(s)
					} else {
						name = strings.TrimSpace(fmt.Sprint(v))
					}
				}
				if name == "" {
					name = fmt.Sprintf("ws-%s", uuid.NewString()[:8])
				}
				// Determine the schedule target (defaults to this host's hostname) and allow override via payload.
				hn, _ := os.Hostname()
				schedule := strings.TrimSpace(hn)
				// Optional explicit override: top-level field `scheduleNode` in the request body.
				if v, ok := spec["scheduleNode"]; ok && v != nil {
					if s, ok2 := v.(string); ok2 && strings.TrimSpace(s) != "" {
						schedule = strings.TrimSpace(s)
					} else {
						// Accept non-string types too (coerce to string)
						sv := strings.TrimSpace(fmt.Sprint(v))
						if sv != "" {
							schedule = sv
						}
					}
				}
				// Also allow specifying the scheduling hint via labels (array or map) using the canonical key.
				if lv, ok := spec["labels"]; ok && lv != nil {
					// labels may arrive as []{name,value} or map[string]string
					switch lt := lv.(type) {
					case []any:
						for _, it := range lt {
							if m, okm := it.(map[string]any); okm {
								k := strings.TrimSpace(fmt.Sprint(m["name"]))
								if strings.EqualFold(k, "guildnet.io/schedule-node") {
									sv := strings.TrimSpace(fmt.Sprint(m["value"]))
									if sv != "" {
										schedule = sv
									}
								}
							}
						}
					case map[string]any:
						if v2, ok2 := lt["guildnet.io/schedule-node"]; ok2 {
							sv := strings.TrimSpace(fmt.Sprint(v2))
							if sv != "" {
								schedule = sv
							}
						}
					}
				}
				// Normalize schedule value to a DNS-1123 compliant value so it matches typical Kubernetes node names.
				if schedule != "" {
					schedule = dns1123Name(schedule)
				}
				obj := map[string]any{
					"apiVersion": "guildnet.io/v1alpha1",
					"kind":       "Workspace",
					"metadata":   map[string]any{"name": name, "labels": map[string]any{"guildnet.io/schedule-node": schedule}},
					"spec": map[string]any{
						"image":     spec["image"],
						"env":       spec["env"],
						"ports":     spec["ports"],
						"args":      spec["args"],
						"resources": spec["resources"],
						"labels":    spec["labels"],
					},
				}

				// Fallback creator: creates a Deployment and Service when the operator CRD isn't available
				doFallback := func() {
					log.Printf("workspace create: creating fallback resources cluster=%s name=%s", clusterID, name)
					specMap := obj["spec"].(map[string]any)
					image, _ := specMap["image"].(string)
					rawPorts, _ := specMap["ports"].([]any)
					ports := []int32{}
					for _, rp := range rawPorts {
						if pm, ok := rp.(map[string]any); ok {
							if pvf, ok := pm["containerPort"].(float64); ok {
								ports = append(ports, int32(pvf))
							}
							if pv, ok := pm["containerPort"].(int64); ok {
								ports = append(ports, int32(pv))
							}
						}
					}
					envVars := []corev1.EnvVar{}
					if rawEnv, ok := specMap["env"].([]any); ok {
						for _, e := range rawEnv {
							if em, ok := e.(map[string]any); ok {
								nameS, _ := em["name"].(string)
								valS, _ := em["value"].(string)
								if nameS != "" {
									envVars = append(envVars, corev1.EnvVar{Name: nameS, Value: valS})
								}
							}
						}
					}
					dep := &appsv1.Deployment{
						ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: defaultNS, Labels: map[string]string{"guildnet.io/workspace": name}},
						Spec:       appsv1.DeploymentSpec{Replicas: pointer.Int32Ptr(1), Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"guildnet.io/workspace": name}}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"guildnet.io/workspace": name}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: name, Image: image, Env: envVars}}}}},
					}
					// Honor the scheduling hint in fallback mode as well
					if strings.TrimSpace(schedule) != "" {
						if dep.Spec.Template.Spec.NodeSelector == nil {
							dep.Spec.Template.Spec.NodeSelector = map[string]string{}
						}
						dep.Spec.Template.Spec.NodeSelector["kubernetes.io/hostname"] = schedule
					}
					if len(ports) > 0 {
						for i := range dep.Spec.Template.Spec.Containers {
							for _, p := range ports {
								dep.Spec.Template.Spec.Containers[i].Ports = append(dep.Spec.Template.Spec.Containers[i].Ports, corev1.ContainerPort{ContainerPort: p})
							}
						}
					}
					svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: defaultNS, Labels: map[string]string{"guildnet.io/workspace": name}}, Spec: corev1.ServiceSpec{Selector: map[string]string{"guildnet.io/workspace": name}}}
					if len(ports) > 0 {
						for _, p := range ports {
							svc.Spec.Ports = append(svc.Spec.Ports, corev1.ServicePort{Port: p, TargetPort: intstr.FromInt(int(p))})
						}
					} else {
						svc.Spec.Ports = []corev1.ServicePort{{Port: 8080, TargetPort: intstr.FromInt(8080)}}
					}

					depCreated := false
					svcCreated := false
					var createErrs []string

					d, derr := cli.AppsV1().Deployments(defaultNS).Create(context.Background(), dep, metav1.CreateOptions{})
					if derr != nil {
						log.Printf("workspace fallback: deployment create failed cluster=%s name=%s err=%v", clusterID, name, derr)
						createErrs = append(createErrs, fmt.Sprintf("deployment:%v", derr))
					} else {
						depCreated = true
						log.Printf("workspace fallback: deployment created cluster=%s name=%s uid=%s", clusterID, name, d.GetUID())
					}

					s, serr := cli.CoreV1().Services(defaultNS).Create(context.Background(), svc, metav1.CreateOptions{})
					if serr != nil {
						log.Printf("workspace fallback: service create failed cluster=%s name=%s err=%v", clusterID, name, serr)
						createErrs = append(createErrs, fmt.Sprintf("service:%v", serr))
					} else {
						svcCreated = true
						log.Printf("workspace fallback: service created cluster=%s name=%s uid=%s clusterIP=%s", clusterID, name, s.GetUID(), s.Spec.ClusterIP)
					}

					resp := map[string]any{"id": name, "status": "Failed"}
					// Update Workspace status so callers waiting on status see Running or Failed
					if dyn != nil {
						wsu, err := dyn.Resource(gvr).Namespace(defaultNS).Get(context.Background(), name, metav1.GetOptions{})
						if err == nil {
							status := map[string]any{}
							if depCreated && svcCreated {
								status["phase"] = "Running"
								status["readyReplicas"] = int64(1)
								if len(ports) > 0 {
									status["proxyTarget"] = fmt.Sprintf("%s:%d", name, ports[0])
									resp["proxyTarget"] = fmt.Sprintf("%s:%d", name, ports[0])
								}
							} else {
								status["phase"] = "Failed"
								status["readyReplicas"] = int64(0)
								if len(createErrs) > 0 {
									status["error"] = strings.Join(createErrs, ", ")
									resp["error"] = strings.Join(createErrs, ", ")
								}
							}
							_ = unstructured.SetNestedField(wsu.Object, status, "status")
							if _, uerr := dyn.Resource(gvr).Namespace(defaultNS).UpdateStatus(context.Background(), wsu, metav1.UpdateOptions{}); uerr != nil {
								log.Printf("workspace fallback: failed to update workspace status cluster=%s name=%s err=%v", clusterID, name, uerr)
							}
						} else {
							log.Printf("workspace fallback: failed to get workspace for status update cluster=%s name=%s err=%v", clusterID, name, err)
						}
					}

					if depCreated && svcCreated {
						resp["status"] = "Running"
						httpx.JSON(w, http.StatusOK, resp)
						return
					}
					resp["status"] = "Failed"
					httpx.JSONError(w, http.StatusInternalServerError, "workspace create failed (fallback)", "create_failed", resp)
					return
				}
				if _, err := dyn.Resource(gvr).Namespace(defaultNS).Create(r.Context(), &unstructured.Unstructured{Object: obj}, metav1.CreateOptions{}); err != nil {
					// If the CRD isn't installed or the GVR doesn't exist, fall back to creating
					// standard Kubernetes resources (Deployment/Service) instead of failing.
					if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) || strings.Contains(err.Error(), "the server could not find the requested resource") || strings.Contains(err.Error(), "404 page not found") {
						log.Printf("workspace create: operator CRD/GVR not found; falling back to Deployment/Service cluster=%s name=%s", clusterID, name)
						doFallback()
						return
					}
					// If this is a Kubernetes StatusError (validation, etc), surface its structured
					// details to the client so the UI can display helpful messages.
					var details any = err.Error()
					if se, ok := err.(*apierrors.StatusError); ok {
						// Use the Status object if available; include message, reason and details.
						s := se.ErrStatus
						// Attempt to include the most useful fields.
						details = map[string]any{"message": s.Message, "reason": string(s.Reason), "details": s.Details}
					}
					httpx.JSONError(w, http.StatusInternalServerError, "workspace create failed", "create_failed", details)
					return
				}
				// Synchronous flow: wait briefly for the operator; if it doesn't reconcile, attempt fallback and return final status
				resp := map[string]any{"id": name, "status": "pending"}
				log.Printf("workspace create: cluster=%s name=%s image=%v specEnv=%v", clusterID, name, obj["spec"].(map[string]any)["image"], obj["spec"].(map[string]any)["env"])

				waitUntil := time.Now().Add(8 * time.Second)
				log.Printf("workspace create: waiting up to 8s for operator reconcile cluster=%s name=%s", clusterID, name)
				reconciled := false
				for time.Now().Before(waitUntil) {
					pods, err := cli.CoreV1().Pods(defaultNS).List(context.Background(), metav1.ListOptions{LabelSelector: fmt.Sprintf("guildnet.io/workspace=%s", name)})
					if err == nil && len(pods.Items) > 0 {
						log.Printf("workspace create: operator reconciled cluster=%s name=%s pods=%d", clusterID, name, len(pods.Items))
						reconciled = true
						break
					}
					time.Sleep(1 * time.Second)
				}

				if reconciled {
					// try to include status/proxyTarget if operator set it
					if dyn != nil {
						if wsu, err := dyn.Resource(gvr).Namespace(defaultNS).Get(context.Background(), name, metav1.GetOptions{}); err == nil {
							if st, ok := wsu.Object["status"].(map[string]any); ok {
								if phase, ok2 := st["phase"].(string); ok2 {
									resp["status"] = phase
								}
								if pt, ok3 := st["proxyTarget"].(string); ok3 {
									resp["proxyTarget"] = pt
								}
							}
						}
					}
					httpx.JSON(w, http.StatusOK, resp)
					return
				}

				// operator did not reconcile; perform fallback creation
				doFallback()
				return
			}
			if len(parts) == 3 && r.Method == http.MethodGet {
				name := parts[2]
				// Try CRD first
				if dyn != nil {
					if ws, err := dyn.Resource(gvr).Namespace(defaultNS).Get(r.Context(), name, metav1.GetOptions{}); err == nil {
						httpx.JSON(w, http.StatusOK, ws.Object)
						return
					}
				}
				// Fallback: synthesize a Workspace-like object from Deployment/Service
				dep, derr := cli.AppsV1().Deployments(defaultNS).Get(r.Context(), name, metav1.GetOptions{})
				svc, serr := cli.CoreV1().Services(defaultNS).Get(r.Context(), name, metav1.GetOptions{})
				if derr != nil && serr != nil {
					httpx.JSONError(w, http.StatusNotFound, "workspace not found", "not_found")
					return
				}
				obj := map[string]any{
					"apiVersion": "guildnet.io/v1alpha1",
					"kind":       "Workspace",
					"metadata":   map[string]any{"name": name, "namespace": defaultNS},
				}
				// Spec synthesis
				spec := map[string]any{}
				ports := []map[string]any{}
				if dep != nil && len(dep.Spec.Template.Spec.Containers) > 0 {
					spec["image"] = dep.Spec.Template.Spec.Containers[0].Image
					for _, cp := range dep.Spec.Template.Spec.Containers[0].Ports {
						ports = append(ports, map[string]any{"name": cp.Name, "containerPort": cp.ContainerPort})
					}
				}
				if len(ports) == 0 && svc != nil {
					for _, sp := range svc.Spec.Ports {
						ports = append(ports, map[string]any{"name": sp.Name, "containerPort": sp.Port})
					}
				}
				if len(ports) > 0 {
					spec["ports"] = ports
				}
				obj["spec"] = spec
				// Status synthesis
				st := map[string]any{"phase": "Pending", "readyReplicas": int64(0)}
				if dep != nil {
					if dep.Status.ReadyReplicas > 0 {
						st["phase"] = "Running"
						st["readyReplicas"] = int64(dep.Status.ReadyReplicas)
					}
				}
				// proxyTarget from first port
				if len(ports) > 0 {
					if p, ok := ports[0]["containerPort"].(int32); ok {
						st["proxyTarget"] = fmt.Sprintf("%s:%d", name, p)
					} else if pf, ok := ports[0]["containerPort"].(int); ok {
						st["proxyTarget"] = fmt.Sprintf("%s:%d", name, pf)
					}
				}
				obj["status"] = st
				httpx.JSON(w, http.StatusOK, obj)
				return
			}
			if len(parts) == 4 && parts[3] == "logs" && r.Method == http.MethodGet {
				name := parts[2]
				pods, err := cli.CoreV1().Pods(defaultNS).List(r.Context(), metav1.ListOptions{LabelSelector: fmt.Sprintf("guildnet.io/workspace=%s", name)})
				if err != nil || len(pods.Items) == 0 {
					httpx.JSONError(w, http.StatusNotFound, "no pods for workspace", "no_pods")
					return
				}
				limit := 200
				if v := r.URL.Query().Get("limit"); v != "" {
					fmt.Sscanf(v, "%d", &limit)
				}
				out := []map[string]string{}
				for _, p := range pods.Items {
					container := ""
					if len(p.Spec.Containers) > 0 {
						container = p.Spec.Containers[0].Name
					}
					data, err := cli.CoreV1().Pods(defaultNS).GetLogs(p.Name, &corev1.PodLogOptions{Container: container}).Do(r.Context()).Raw()
					if err != nil {
						continue
					}
					lines := strings.Split(strings.TrimSpace(string(data)), "\n")
					for _, ln := range lines {
						if ln != "" {
							out = append(out, map[string]string{"t": time.Now().UTC().Format(time.RFC3339), "msg": fmt.Sprintf("[%s] %s", p.Name, ln)})
						}
					}
				}
				if len(out) > limit {
					out = out[len(out)-limit:]
				}
				httpx.JSON(w, http.StatusOK, out)
				return
			}

			// Health endpoint: /api/cluster/{id}/health
			if len(parts) == 2 && parts[1] == "health" {
				if r.Method != http.MethodGet {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				status := map[string]any{"k8s": map[string]any{"ok": false}, "rdb": map[string]any{"ok": false}}
				// k8s connectivity
				ctxc, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				defer cancel()
				if _, err := cli.CoreV1().Namespaces().List(ctxc, metav1.ListOptions{Limit: 1}); err == nil {
					status["k8s"] = map[string]any{"ok": true}
				} else {
					status["k8s"] = map[string]any{"ok": false, "error": err.Error()}
				}
				// RethinkDB connectivity (if instance present and RDB initialized)
				if deps.Registry != nil {
					if present, err := deps.Registry.RDBPresent(clusterID); err == nil {
						if present {
							status["rdb"] = map[string]any{"ok": true}
						} else {
							status["rdb"] = map[string]any{"ok": false, "error": "rdb not initialized"}
						}
					} else {
						status["rdb"] = map[string]any{"ok": false, "error": err.Error()}
					}
				} else {
					status["rdb"] = map[string]any{"ok": false, "error": "no registry"}
				}
				httpx.JSON(w, http.StatusOK, status)
				return
			}
			if len(parts) == 5 && parts[3] == "logs" && parts[4] == "stream" && r.Method == http.MethodGet {
				name := parts[2]
				pods, err := cli.CoreV1().Pods(defaultNS).List(r.Context(), metav1.ListOptions{LabelSelector: fmt.Sprintf("guildnet.io/workspace=%s", name)})
				if err != nil || len(pods.Items) == 0 {
					http.Error(w, "no pods", http.StatusNotFound)
					return
				}
				pod := pods.Items[0]
				container := ""
				if len(pod.Spec.Containers) > 0 {
					container = pod.Spec.Containers[0].Name
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Connection", "keep-alive")
				flusher, ok := w.(http.Flusher)
				if !ok {
					http.Error(w, "stream unsupported", http.StatusInternalServerError)
					return
				}
				ctx := r.Context()
				stream, err := cli.CoreV1().Pods(defaultNS).GetLogs(pod.Name, &corev1.PodLogOptions{Container: container, Follow: true}).Stream(ctx)
				if err != nil {
					http.Error(w, "log stream error", http.StatusInternalServerError)
					return
				}
				defer stream.Close()
				scanner := bufio.NewScanner(stream)
				for scanner.Scan() {
					select {
					case <-ctx.Done():
						return
					default:
					}
					line := scanner.Text()
					msg := fmt.Sprintf("[%s] %s", pod.Name, strings.TrimSpace(line))
					io.WriteString(w, "data: ")
					b, _ := json.Marshal(map[string]string{"t": time.Now().UTC().Format(time.RFC3339), "msg": msg})
					w.Write(b)
					io.WriteString(w, "\n\n")
					flusher.Flush()
				}
				return
			}
			if len(parts) == 3 && r.Method == http.MethodDelete {
				// auth for mutating
				if r.Method != http.MethodGet {
					if deps.Token != "" || true {
						host, _, _ := net.SplitHostPort(r.RemoteAddr)
						ip := net.ParseIP(host)
						if strings.TrimSpace(deps.Token) != "" {
							authz := r.Header.Get("Authorization")
							if !strings.HasPrefix(strings.ToLower(authz), "bearer ") || strings.TrimSpace(authz[7:]) != strings.TrimSpace(deps.Token) {
								http.Error(w, "unauthorized", http.StatusUnauthorized)
								return
							}
						} else if !(ip != nil && (ip.IsLoopback() || host == "127.0.0.1" || host == "::1")) {
							http.Error(w, "unauthorized", http.StatusUnauthorized)
							return
						}
					}
				}
				name := parts[2]
				if err := dyn.Resource(gvr).Namespace(defaultNS).Delete(r.Context(), name, metav1.DeleteOptions{}); err != nil {
					httpx.JSONError(w, http.StatusNotFound, "workspace not found", "not_found")
					return
				}
				httpx.JSON(w, http.StatusOK, map[string]any{"deleted": name})
				return
			}
		}
		// Per-cluster Databases API routes: delegate to httpx.DBAPI with OrgID=clusterID
		if len(parts) >= 2 && parts[1] == "db" {
			api := &httpx.DBAPI{Manager: func() httpx.DBManager {
				// Prefer an already-initialized per-cluster RDB manager. Avoid
				// attempting EnsureRDB during request handling because it can
				// block with retries and produce noisy logs. Pre-warming should
				// be done at bootstrap; handlers will return nil if RDB isn't ready.
				if deps.Registry != nil {
					if present, err := deps.Registry.RDBPresent(clusterID); err == nil && present {
						if inst, err := deps.Registry.Get(r.Context(), clusterID); err == nil && inst != nil {
							return inst.RDB
						}
					}
				}
				return nil
			}(), OrgID: clusterID, RBAC: httpx.NewRBACStore()}
			mux2 := http.NewServeMux()
			api.Register(mux2)
			// Rewrite path to /api/db...
			r2 := r.Clone(r.Context())
			r2.URL = new(url.URL)
			*r2.URL = *r.URL
			if len(parts) == 2 { // /api/cluster/:id/db -> /api/db
				r2.URL.Path = "/api/db"
			} else {
				r2.URL.Path = "/api/db/" + strings.Join(parts[2:], "/")
			}
			mux2.ServeHTTP(w, r2)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	// SSE: per-cluster DB changefeed: /sse/cluster/:id/db/...
	mux.HandleFunc("/sse/cluster/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/sse/cluster/")
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) < 2 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		clusterID := parts[0]
		if len(parts) >= 2 && parts[1] == "db" {
			api := &httpx.DBAPI{Manager: func() httpx.DBManager {
				// Use only an already-initialized RDB manager; do not attempt
				// to Initialize here to avoid request-time retries.
				if deps.Registry != nil {
					if present, err := deps.Registry.RDBPresent(clusterID); err == nil && present {
						if inst, err := deps.Registry.Get(r.Context(), clusterID); err == nil && inst != nil {
							return inst.RDB
						}
					}
				}
				return nil
			}(), OrgID: clusterID, RBAC: httpx.NewRBACStore()}
			mux2 := http.NewServeMux()
			api.Register(mux2)
			// Rewrite to /sse/db/...
			r2 := r.Clone(r.Context())
			r2.URL = new(url.URL)
			*r2.URL = *r.URL
			r2.URL.Path = "/sse/db/" + strings.Join(parts[2:], "/")
			mux2.ServeHTTP(w, r2)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	// Register federation API endpoints (sites and federatedservices)
	RegisterFederationAPIs(mux, deps)
	// Register CRUD endpoints under /api/v1 for federated services
	registerFederationCRUD(mux, deps)
	return mux
}

// RestorePublishedMappings reads persisted published services from host DB and
// attempts to recreate tsnet listeners for them. This should be invoked after
// the Registry and Router are initialized (for example, during hostapp startup).
func RestorePublishedMappings(ctx context.Context, deps Deps) error {
	if deps.DB == nil || deps.Registry == nil {
		return nil
	}
	var list []localdb.PublishedService
	if err := deps.DB.ListPublished(&list); err != nil {
		return err
	}
	for _, p := range list {
		// Attempt to ensure an instance exists for this cluster
		inst, err := deps.Registry.Get(ctx, p.ClusterID)
		if err != nil || inst == nil {
			log.Printf("api: restore published: cannot get instance for cluster=%s err=%v", p.ClusterID, err)
			continue
		}
		if inst.TS == nil {
			log.Printf("api: restore published: ts connector missing for cluster=%s service=%s", p.ClusterID, p.Service)
			continue
		}
		key := p.ClusterID + ":" + p.Service
		publishedMapMu.Lock()
		if _, ok := publishedMap[key]; ok {
			publishedMapMu.Unlock()
			continue
		}
		publishedMapMu.Unlock()

		// Try to listen using stored addr; if that fails, try to extract port and bind to :port
		ln, lerr := inst.TS.Listen("tcp", p.Addr)
		if lerr != nil {
			// attempt to parse port
			_, portStr, perr := net.SplitHostPort(p.Addr)
			if perr == nil {
				ln, lerr = inst.TS.Listen("tcp", fmt.Sprintf(":%s", portStr))
			}
		}
		if lerr != nil {
			log.Printf("api: restore published: listen failed cluster=%s service=%s addr=%s err=%v", p.ClusterID, p.Service, p.Addr, lerr)
			continue
		}
		pl := &publishedListener{clusterID: p.ClusterID, service: p.Service, addr: ln.Addr().String(), ln: ln, addedAt: p.AddedAt}
		publishedMapMu.Lock()
		publishedMap[key] = pl
		publishedMapMu.Unlock()
		log.Printf("api: restored published listener cluster=%s service=%s addr=%s", p.ClusterID, p.Service, pl.addr)

		// Start accept-loop similar to the on-demand path
		go func(pl *publishedListener, key string) {
			defer func() {
				pl.ln.Close()
				publishedMapMu.Lock()
				delete(publishedMap, key)
				publishedMapMu.Unlock()
				if deps.DB != nil {
					if err := deps.DB.DeletePublished(key); err != nil {
						log.Printf("api: restore published: failed delete persisted key=%s err=%v", key, err)
					}
				}
				log.Printf("api: restored published listener closed cluster=%s service=%s", pl.clusterID, pl.service)
			}()
			for {
				conn, err := pl.ln.Accept()
				if err != nil {
					log.Printf("api: restore published accept error cluster=%s err=%v", pl.clusterID, err)
					return
				}
				go func(c net.Conn, lpAddr string) {
					defer c.Close()
					// Dial local loopback port by using original address's port
					_, portStr, _ := net.SplitHostPort(lpAddr)
					dst, dErr := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%s", portStr))
					if dErr != nil {
						log.Printf("api: restore published proxy dial failed addr=%s err=%v", lpAddr, dErr)
						return
					}
					defer dst.Close()
					go func() { _, _ = io.Copy(dst, c); _ = dst.Close() }()
					_, _ = io.Copy(c, dst)
				}(conn, pl.addr)
			}
		}(pl, key)
	}
	return nil
}

func kubeconfigFrom(kc string) (*rest.Config, error) {
	return clientcmd.RESTConfigFromKubeConfig([]byte(kc))
}

func healthyCluster(cfg *rest.Config) error {
	cfg.Timeout = 3 * time.Second
	// Try a lightweight HTTP GET to /version using the kube transport so we can
	// enforce a client-side timeout reliably.
	tr, err := rest.TransportFor(cfg)
	if err == nil {
		httpClient := &http.Client{Transport: tr, Timeout: 3 * time.Second}
		// Build version URL from cfg.Host (may include scheme)
		host := cfg.Host
		if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
			host = "https://" + strings.TrimPrefix(host, "//")
		}
		verURL := strings.TrimRight(host, "/") + "/version"
		req, _ := http.NewRequestWithContext(context.Background(), "GET", verURL, nil)
		if resp, err := httpClient.Do(req); err == nil {
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				_ = resp.Body.Close()
				return nil
			}
			_ = resp.Body.Close()
		}
	}
	// Fallback: try list namespaces with a short context timeout using client-go
	cli, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = cli.CoreV1().Namespaces().List(ctx, metav1.ListOptions{Limit: 1})
	return err
}

func readClusterKubeconfig(db *localdb.DB, sec *secrets.Manager, id string) (string, bool) {
	if db == nil {
		return "", false
	}
	var cred map[string]any
	if db.Get("credentials", fmt.Sprintf("cl:%s:kubeconfig", id), &cred) != nil {
		return "", false
	}
	val := fmt.Sprint(cred["value"])
	// If explicitly marked encrypted, require successful decryption and basic validation
	if encFlag, ok := cred["encrypted"].(bool); ok && encFlag {
		if sec == nil {
			return "", false
		}
		if v, err := sec.Decrypt(val); err == nil {
			if cfg, e2 := kubeconfigFrom(v); e2 == nil && cfg != nil {
				return v, true
			}
		}
		return "", false
	}
	// Legacy/unknown: try decrypt first, then fall back to plaintext; validate either way
	if sec != nil {
		if v, err := sec.Decrypt(val); err == nil {
			if cfg, e2 := kubeconfigFrom(v); e2 == nil && cfg != nil {
				return v, true
			}
		}
	}
	// Treat as plaintext and validate
	if cfg, err := kubeconfigFrom(val); err == nil && cfg != nil {
		return val, true
	}
	return "", false
}

func headscaleHealth(endpoint string) (string, error) {
	if endpoint == "" {
		return "unknown", nil
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "unknown", err
	}
	addr := u.Host
	if !strings.Contains(addr, ":") {
		if u.Scheme == "https" {
			addr = addr + ":443"
		} else {
			addr = addr + ":80"
		}
	}
	c, err := net.DialTimeout("tcp", addr, 1*time.Second)
	if err == nil {
		_ = c.Close()
		return "ok", nil
	}
	return "error", err
}

// resolveClusterIDAlias maps well-known aliases (like "default") to a concrete
// cluster ID when possible. If the provided id already exists or no mapping can
// be determined, it returns the input unchanged. When exactly one cluster
// record exists in the host database and id is "default", it will return that
// record's id to make out-of-the-box flows work without knowing the
// deterministic id.
func resolveClusterIDAlias(db *localdb.DB, id string) string {
	in := strings.TrimSpace(id)
	if db == nil || in == "" {
		return id
	}
	// If an explicit cluster record exists for this id, keep it.
	var rec map[string]any
	if err := db.Get("clusters", in, &rec); err == nil && len(rec) > 0 {
		return in
	}
	// Only alias the well-known "default" identifier to avoid surprising
	// remaps of arbitrary user-provided ids.
	if in != "default" {
		return id
	}
	// If exactly one cluster record exists, map to it.
	var items []map[string]any
	if err := db.List("clusters", &items); err == nil && len(items) == 1 {
		if v, ok := items[0]["id"]; ok {
			sid := strings.TrimSpace(fmt.Sprint(v))
			if sid != "" {
				log.Printf("cluster: aliasing id=%q to sole cluster id=%s", id, sid)
				return sid
			}
		}
	}
	return id
}

// isLocalKubeProxyAvailable returns true if a kubectl proxy is listening on 127.0.0.1:8001.
// isLocalKubeProxyAvailable removed: local kubectl proxy is not used in production paths.

// isTimeoutErr returns true if err looks like a client timeout/connection timeout to the API server.
func isTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	// net.Error with Timeout()
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "client.timeout exceeded") || strings.Contains(msg, "context deadline exceeded") || strings.Contains(msg, "i/o timeout") {
		return true
	}
	return false
}

// ensureProxyFallbackOnTimeout removed: no local proxy fallback in production paths.

// applyClusterAPIProxy applies only explicit per-cluster proxy overrides.
func applyClusterAPIProxy(cfg *rest.Config, setMgr settings.Manager, clusterID string) {
	var cs settings.Cluster
	_ = setMgr.GetCluster(clusterID, &cs)
	host := strings.TrimSpace(cs.APIProxyURL)
	// Only apply APIProxyURL if explicitly configured for this cluster.
	if host != "" {
		cfg.Host = host
		if strings.HasPrefix(strings.ToLower(host), "http://") {
			cfg.TLSClientConfig = rest.TLSClientConfig{}
		}
	}
	if cs.APIProxyForceHTTP {
		if u, err := url.Parse(cfg.Host); err == nil {
			u.Scheme = "http"
			cfg.Host = u.String()
		}
	}
}
