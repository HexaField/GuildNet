package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/your/module/internal/httpx"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// RegisterFederationAPIs registers minimal endpoints for Sites and FederatedService status.
func RegisterFederationAPIs(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("/v1/sites", func(w http.ResponseWriter, r *http.Request) {
		if deps.Registry == nil {
			httpx.JSON(w, http.StatusOK, []any{})
			return
		}
		raw := deps.Registry.List()
		out := make([]any, 0)
		// For each registry entry (cluster) attempt to enumerate persisted devices
		for _, s := range raw {
			// Try to open instance to read per-cluster DB
			if inst, err := deps.Registry.Get(r.Context(), s.ID); err == nil && inst != nil && inst.DB != nil {
				var devices []map[string]any
				if err := inst.DB.List("devices", &devices); err == nil && len(devices) > 0 {
					for _, dm := range devices {
						// Build a record per-device, merging cluster info and device metadata
						rec := map[string]any{
							"clusterId":       s.ID,
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
						// Merge any persisted fields from device metadata
						for k, v := range dm {
							rec[k] = v
						}
						// Ensure some typed defaults
						if _, ok := rec["supportsCluster"]; !ok {
							rec["supportsCluster"] = inst.K8s != nil
						}
						if _, ok := rec["lastSeen"]; !ok {
							rec["lastSeen"] = time.Now()
						}
						out = append(out, rec)
					}
					// done with this cluster
					continue
				}
			}
			// Fallback: emit a single cluster-level record when no persisted devices found
			rec := map[string]any{
				"id":              s.ID,
				"state":           s.HasK8s || s.HasDB, // best-effort state text boolean
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
			if inst, err := deps.Registry.Get(r.Context(), s.ID); err == nil && inst != nil {
				// Fallback: Populate tailnet IPs from TS connector health when available
				if inst.TS != nil {
					if st, det := inst.TS.Health(r.Context()); st == "ok" || st == "degraded" || st == "starting" {
						if ip, ok := det["ip"].(string); ok && ip != "" {
							rec["tailnetIPs"] = []string{ip}
						}
						if fqdn, ok := det["fqdn"].(string); ok && fqdn != "" {
							if ips, ok := rec["tailnetIPs"].([]string); ok {
								rec["tailnetIPs"] = append(ips, fqdn)
							}
						}
					}
				}
				// SupportsCluster is true when k8s client present
				rec["supportsCluster"] = inst.K8s != nil
				// Best-effort lastSeen time from created map or now
				rec["lastSeen"] = time.Now()
			}
			out = append(out, rec)
		}
		httpx.JSON(w, http.StatusOK, out)
	})

	// Devices post their capabilities and heartbeat here. Devices are the source-of-truth
	// for capabilities such as CPU/Memory/Storage/VRAM and tailnet IPs. Request body should
	// include at minimum: clusterId and id (device id). Other optional fields will be stored.
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
		clusterIDRaw, ok := payload["clusterId"].(string)
		if !ok || strings.TrimSpace(clusterIDRaw) == "" {
			httpx.JSONError(w, http.StatusBadRequest, "clusterId required", "bad_request", "clusterId required")
			return
		}
		deviceIDRaw, ok := payload["id"].(string)
		if !ok || strings.TrimSpace(deviceIDRaw) == "" {
			httpx.JSONError(w, http.StatusBadRequest, "id required", "bad_request", "id required")
			return
		}
		// Normalize id to registry key form
		clusterID := strings.TrimSpace(clusterIDRaw)
		deviceID := strings.TrimSpace(deviceIDRaw)
		if deps.Registry == nil {
			httpx.JSONError(w, http.StatusServiceUnavailable, "no registry", "no_registry", "device heartbeat unavailable")
			return
		}
		inst, err := deps.Registry.Get(r.Context(), clusterID)
		if err != nil || inst == nil {
			httpx.JSONError(w, http.StatusNotFound, "cluster not found", "not_found", fmt.Sprintf("cluster %s not found", clusterID))
			return
		}
		// Persist payload under collection "devices" with key deviceID. Keep only JSON-serializable fields.
		// Ensure LastSeen is present
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

	mux.HandleFunc("/v1/federatedservices", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			httpx.JSONError(w, http.StatusMethodNotAllowed, "not implemented", "not_implemented", "only GET supported in minimal API")
			return
		}
		// Attempt to list via any registry instance with a dynamic client
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
						out = append(out, map[string]any{"clusterId": s.ID, "namespace": it.GetNamespace(), "name": it.GetName()})
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
		// Iterate registry instances and attempt to read the resource's status field via dynamic client
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
