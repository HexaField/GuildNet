package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/your/module/internal/httpx"
	"github.com/your/module/internal/localdb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// RegisterFederationAPIs registers minimal endpoints for Sites and FederatedService status.
// This implementation always exposes a canonical `clusterId` field and attempts
// to include persisted per-device rows by scanning per-cluster sqlite DBs under
// ~/.guildnet/state/<cluster>/guildnet.sqlite. If a registry Instance exists
// for a cluster we prefer reading its DB; otherwise we fall back to scanning the
// state directory so local persisted device rows are not missed.
func RegisterFederationAPIs(mux *http.ServeMux, deps Deps) {
	// /v1/sites: return per-device rows from per-cluster DBs when available,
	// otherwise emit a cluster-level record. All records use `clusterId`.
	mux.HandleFunc("/v1/sites", func(w http.ResponseWriter, r *http.Request) {
		out := make([]any, 0)
		seen := map[string]bool{}

		// First, attempt to enumerate clusters using the registry and read their
		// per-cluster DBs when an Instance is available.
		if deps.Registry != nil {
			for _, s := range deps.Registry.List() {
				if inst, err := deps.Registry.Get(r.Context(), s.ID); err == nil && inst != nil && inst.DB != nil {
					var devices []map[string]any
					if err := inst.DB.List("devices", &devices); err == nil && len(devices) > 0 {
						for _, dm := range devices {
							rec := map[string]any{
								"cluster":         s.ID,
								"id":              fmt.Sprint(dm["id"]),
								"name":            fmt.Sprint(dm["name"]),
								"state":           s.HasK8s || s.HasDB,
								"createdAt":       s.CreatedAt,
								"started":         s.Started,
								"stateDir":        s.StateDir,
								"hasDB":           s.HasDB,
								"hasK8s":          s.HasK8s,
								"forwards":        s.Forwards,
								"supportsCluster": dm["supportsCluster"],
								"tailnetIPs":      []string{},
								"cpuMilli":        int64(0),
								"memoryMB":        int64(0),
								"storageMB":       int64(0),
								"vramMB":          int64(0),
								"lastSeen":        time.Now(),
							}
							for k, v := range dm {
								// preserve device metadata, but normalize legacy clusterId -> cluster
								if k == "clusterId" {
									rec["cluster"] = fmt.Sprint(v)
									continue
								}
								rec[k] = v
							}
							if _, ok := rec["supportsCluster"]; !ok {
								rec["supportsCluster"] = inst.K8s != nil
							}
							if _, ok := rec["lastSeen"]; !ok {
								rec["lastSeen"] = time.Now()
							}
							// Ensure no legacy clusterId leaks into API responses
							delete(rec, "clusterId")
							out = append(out, rec)
						}
						seen[s.ID] = true
					}
				}
			}
		}

		// Next, scan the local state directory for per-cluster DBs to include any
		// persisted device rows that might exist even when a Registry Instance
		// is not present (local-only clusters).
		if h, err := os.UserHomeDir(); err == nil {
			stateDir := filepath.Join(h, ".guildnet", "state")
			if ents, err := os.ReadDir(stateDir); err == nil {
				for _, e := range ents {
					if !e.IsDir() {
						continue
					}
					cl := e.Name()
					if seen[cl] {
						continue
					}
					pdir := filepath.Join(stateDir, cl)
					db, err := localdb.Open(pdir)
					if err != nil {
						continue
					}
					var devices []map[string]any
					if err := db.List("devices", &devices); err == nil && len(devices) > 0 {
						for _, dm := range devices {
							rec := map[string]any{
								"cluster":         cl,
								"name":            fmt.Sprint(dm["name"]),
								"state":           false,
								"createdAt":       time.Now(),
								"started":         true,
								"stateDir":        pdir,
								"hasDB":           true,
								"hasK8s":          false,
								"forwards":        0,
								"supportsCluster": dm["supportsCluster"],
								"tailnetIPs":      []string{},
								"cpuMilli":        int64(0),
								"memoryMB":        int64(0),
								"storageMB":       int64(0),
								"vramMB":          int64(0),
								"lastSeen":        time.Now(),
							}
							for k, v := range dm {
								if k == "clusterId" {
									rec["cluster"] = fmt.Sprint(v)
									continue
								}
								rec[k] = v
							}
							// Ensure no legacy clusterId leaks into API responses
							delete(rec, "clusterId")
							out = append(out, rec)
						}
						seen[cl] = true
					}
					_ = db.Close()
				}
			}
		}

		// Finally, append cluster-level records for clusters not already represented
		// by per-device rows so the UI can still show clusters that exist in the
		// registry even if they have no devices persisted.
		if deps.Registry != nil {
			for _, s := range deps.Registry.List() {
				if seen[s.ID] {
					continue
				}
				rec := map[string]any{
					"id":              s.ID,
					"state":           s.HasK8s || s.HasDB,
					"createdAt":       s.CreatedAt,
					"started":         s.Started,
					"stateDir":        s.StateDir,
					"hasDB":           s.HasDB,
					"hasK8s":          s.HasK8s,
					"forwards":        s.Forwards,
					"supportsCluster": s.HasK8s,
					"tailnetIPs":      []string{},
					"cpuMilli":        0,
					"memoryMB":        0,
					"storageMB":       0,
					"vramMB":          0,
					"lastSeen":        time.Now(),
				}
				out = append(out, rec)
			}
		}

		httpx.JSON(w, http.StatusOK, out)
	})

	// Devices post their capabilities and heartbeat here.
	mux.HandleFunc("/v1/sites/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			httpx.JSONError(w, http.StatusMethodNotAllowed, "only POST supported", "method_not_allowed", "only POST supported")
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			httpx.JSONError(w, http.StatusBadRequest, "invalid json", "bad_request", err.Error())
			return
		}
		clusterIDRaw, ok := payload["cluster"].(string)
		if !ok || clusterIDRaw == "" {
			httpx.JSONError(w, http.StatusBadRequest, "cluster required", "bad_request", "cluster required")
			return
		}
		deviceIDRaw, ok := payload["id"].(string)
		if !ok || deviceIDRaw == "" {
			httpx.JSONError(w, http.StatusBadRequest, "id required", "bad_request", "id required")
			return
		}
		clusterID := clusterIDRaw
		deviceID := deviceIDRaw
		if deps.Registry == nil {
			httpx.JSONError(w, http.StatusServiceUnavailable, "no registry", "no_registry", "device heartbeat unavailable")
			return
		}
		inst, err := deps.Registry.Get(r.Context(), clusterID)
		if err != nil || inst == nil {
			httpx.JSONError(w, http.StatusNotFound, "cluster not found", "not_found", fmt.Sprintf("cluster %s not found", clusterID))
			return
		}
		// Normalize persisted payload to include `cluster` field and drop legacy `clusterId`.
		payload["cluster"] = clusterID
		delete(payload, "clusterId")
		payload["lastSeen"] = time.Now().UTC()
		if inst.DB == nil {
			httpx.JSONError(w, http.StatusInternalServerError, "no per-cluster DB", "no_db", "per-cluster DB unavailable")
			return
		}
		if err := inst.DB.Put("devices", deviceID, payload); err != nil {
			log.Printf("heartbeat persist cluster=%s device=%s err=%v", clusterID, deviceID, err)
			httpx.JSONError(w, http.StatusInternalServerError, "persist failed", "persist_failed", err.Error())
			return
		}
		log.Printf("heartbeat persisted cluster=%s device=%s", clusterID, deviceID)
		httpx.JSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	// Federated services listing (unchanged)
	mux.HandleFunc("/v1/federatedservices", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			httpx.JSONError(w, http.StatusMethodNotAllowed, "not implemented", "not_implemented", "only GET supported in minimal API")
			return
		}
		if deps.Registry == nil {
			httpx.JSON(w, http.StatusOK, []any{})
			return
		}
		gvr := schema.GroupVersionResource{Group: "guildnet.io", Version: "v1alpha1", Resource: "federatedservices"}
		for _, s := range deps.Registry.List() {
			if inst, err := deps.Registry.Get(r.Context(), s.ID); err == nil && inst != nil && inst.Dyn != nil {
				if ulist, err := inst.Dyn.Resource(gvr).Namespace(metav1.NamespaceAll).List(r.Context(), metav1.ListOptions{}); err == nil {
					out := make([]any, 0, len(ulist.Items))
					for _, it := range ulist.Items {
						out = append(out, map[string]any{"id": s.ID, "namespace": it.GetNamespace(), "name": it.GetName()})
					}
					httpx.JSON(w, http.StatusOK, out)
					return
				}
			}
		}
		httpx.JSON(w, http.StatusOK, []any{})
	})

	mux.HandleFunc("/v1/federatedservices/per-site", func(w http.ResponseWriter, r *http.Request) {
		ns := strings.TrimSpace(r.URL.Query().Get("ns"))
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		if ns == "" || name == "" {
			httpx.JSONError(w, http.StatusBadRequest, "ns and name required", "bad_request", "ns and name required")
			return
		}
		if deps.Registry == nil {
			httpx.JSONError(w, http.StatusServiceUnavailable, "no registry", "no_registry", "per-site info unavailable")
			return
		}
		for _, s := range deps.Registry.List() {
			if inst, err := deps.Registry.Get(r.Context(), s.ID); err == nil && inst != nil && inst.Dyn != nil {
				if obj, err := inst.Dyn.Resource(schema.GroupVersionResource{Group: "guildnet.io", Version: "v1alpha1", Resource: "federatedservices"}).Namespace(ns).Get(r.Context(), name, metav1.GetOptions{}); err == nil {
					if status, ok := obj.Object["status"]; ok {
						httpx.JSON(w, http.StatusOK, status)
						return
					}
				}
			}
		}
		httpx.JSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
	})
}
