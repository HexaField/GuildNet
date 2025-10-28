package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/your/module/internal/cluster"
	"github.com/your/module/internal/db"
	"github.com/your/module/internal/headscale"
	"github.com/your/module/internal/httpx"
	"github.com/your/module/internal/k8s"
	"github.com/your/module/internal/localdb"
	"github.com/your/module/internal/settings"
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

		// Determine the local device hostname to mark self entries for callers hitting this Host App.
		selfHost := ""
		if deps.DB != nil {
			setMgr := settings.Manager{DB: deps.DB}
			var ts settings.Tailscale
			_ = setMgr.GetTailscale(&ts)
			selfHost = strings.ToLower(strings.TrimSpace(ts.Hostname))
		}
		if selfHost == "" {
			if h, err := os.Hostname(); err == nil {
				selfHost = strings.ToLower(strings.TrimSpace(h))
			}
		}
		isSelf := func(rec map[string]any) bool {
			if selfHost == "" {
				return false
			}
			id := strings.ToLower(strings.TrimSpace(fmt.Sprint(rec["id"])))
			name := strings.ToLower(strings.TrimSpace(fmt.Sprint(rec["name"])))
			// Consider self if either id or name matches the local hostname (common case).
			return id == selfHost || name == selfHost
		}

		// helper: when no tailnetIPs are present, try to query Headscale admin
		// API (remote) using global Tailscale settings when available. This
		// replaces the old local sqlite fallback and keeps the behavior remote-
		// first and production-ready. Token wiring (if required) should be
		// provided via settings or per-cluster secrets; for now we call with
		// an empty token and prefer the configured login server.
		fetchFromHeadscale := func(devID string) []string {
			ips := []string{}
			if deps.DB == nil {
				return ips
			}
			var ts settings.Tailscale
			_ = settings.Manager{DB: deps.DB}.GetTailscale(&ts)
			endpoint := strings.TrimSpace(ts.LoginServer)
			if endpoint == "" {
				return ips
			}
			// TODO: wire an admin token via settings / credentials if Headscale
			// requires authentication. For now call unauthenticated.
			found, _, err := headscale.FindMachineIPsByHostname(endpoint, "", devID)
			if err != nil {
				log.Printf("fetchFromHeadscale: headscale lookup failed dev=%s err=%v", devID, err)
				return ips
			}
			if len(found) > 0 {
				ips = append(ips, found...)
			}
			return ips
		}

		// First, attempt to enumerate clusters using the registry and read their
		// per-cluster DBs when an Instance is available.
		if deps.Registry != nil {
			for _, s := range deps.Registry.List() {
				if inst, err := deps.Registry.Get(r.Context(), s.ID); err == nil && inst != nil && inst.DB != nil {
					// If the cluster exposes a dynamic client, prefer DeviceParticipant CRs
					// as the canonical records. Build a map of device id -> cr fields.
					crMap := map[string]map[string]any{}
					if inst.Dyn != nil {
						if ulist, err := inst.Dyn.Resource(schema.GroupVersionResource{Group: "guildnet.io", Version: "v1alpha1", Resource: "deviceparticipants"}).Namespace("guildnet-system").List(r.Context(), metav1.ListOptions{}); err == nil {
							for _, it := range ulist.Items {
								id := it.GetName()
								m := map[string]any{}
								if sp, ok := it.Object["spec"].(map[string]any); ok {
									for k, v := range sp {
										m[k] = v
									}
								}
								if st, ok := it.Object["status"].(map[string]any); ok {
									for k, v := range st {
										m[k] = v
									}
								}
								crMap[id] = m
							}
						}
					}
					var devices []map[string]any
					if err := inst.DB.List("devices", &devices); err == nil && len(devices) > 0 {
						for _, dm := range devices {
							id := fmt.Sprint(dm["id"])
							// Start with DB persisted record
							rec := map[string]any{
								"clusterId":       s.ID,
								"id":              id,
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
								// preserve device metadata; ignore any legacy `cluster` field
								if k == "cluster" {
									continue
								}
								if k == "clusterId" {
									rec["clusterId"] = fmt.Sprint(v)
									continue
								}
								rec[k] = v
							}
							// If CR exists for this device id, prefer its fields as canonical
							if cm, ok := crMap[id]; ok {
								for k, v := range cm {
									// overwrite or set canonical fields
									rec[k] = v
								}
							}
							if _, ok := rec["supportsCluster"]; !ok {
								rec["supportsCluster"] = inst.K8s != nil
							}
							if _, ok := rec["lastSeen"]; !ok {
								rec["lastSeen"] = time.Now()
							}
							// Ensure we expose the canonical clusterId only
							// Mark self entries and null lastSeen for UI to optionally hide them locally
							if isSelf(rec) {
								rec["self"] = true
								rec["lastSeen"] = nil
							}

							// If we don't have tailnet IPs from the persisted record or CR, try
							// to obtain them from the per-cluster tsnet connector (best-effort).
							if inst != nil && inst.TS != nil {
								// only try when nothing present
								if arr, ok := rec["tailnetIPs"].([]any); !ok || len(arr) == 0 {
									// Debug: log that we're attempting a TS Health call for this cluster/device
									log.Printf("backfill: attempting TS.Health cluster=%s device=%s", s.ID, id)
									// use HealthWithRetry with a small timeout and retries so
									// transient tsnet startup delays don't immediately fail.
									st, det := inst.TS.HealthWithRetry(context.Background(), 5*time.Second, 3, 200*time.Millisecond)
									// Debug: log the returned status and details to aid debugging
									log.Printf("backfill: TS.Health returned cluster=%s device=%s status=%s details=%v", s.ID, id, st, det)
									if st == "ok" {
										ips := []string{}
										if v, ok2 := det["ip"].(string); ok2 && v != "" {
											ips = append(ips, v)
										}
										if v, ok2 := det["fqdn"].(string); ok2 && v != "" {
											ips = append(ips, v)
										}
										if len(ips) > 0 {
											rec["tailnetIPs"] = ips
											// Persist the backfilled tailnetIPs into the per-cluster DB
											// so future reads don't need to re-query the TS connector.
											if inst.DB != nil {
												// only persist when the original persisted record lacked tailnetIPs
												needPersist := true
												if t, ok := dm["tailnetIPs"]; ok {
													switch x := t.(type) {
													case []any:
														if len(x) > 0 {
															needPersist = false
														}
													case []string:
														if len(x) > 0 {
															needPersist = false
														}
													}
												}
												if needPersist {
													dm["tailnetIPs"] = ips
													if err := inst.DB.Put("devices", id, dm); err != nil {
														log.Printf("persist backfilled tailnetIPs cluster=%s device=%s err=%v", s.ID, id, err)
													} else {
														log.Printf("persisted backfilled tailnetIPs cluster=%s device=%s ips=%v", s.ID, id, ips)
													}
												}
											}
										}
									}
								} else {
									// schedule a background, longer-running backfill attempt which will try longer
									// and persist when an IP becomes available. This avoids blocking API callers
									// while still ensuring eventual consistency.
									go func(inst *cluster.Instance, clusterID, deviceID string) {
										// longer retry window for background attempts
										// increase attempts and backoff so tsnet has ample time to finish login/initialization
										st2, det2 := inst.TS.HealthWithRetry(context.Background(), 6*time.Second, 20, 1*time.Second)
										if st2 == "ok" {
											var bgips []string
											if ip2, ok := det2["ip"].(string); ok && ip2 != "" {
												bgips = append(bgips, ip2)
											}
											if fqdn2, ok := det2["fqdn"].(string); ok && fqdn2 != "" {
												bgips = append(bgips, fqdn2)
											}
											if len(bgips) > 0 {
												// load device row and only persist if empty
												var row map[string]any
												if err := inst.DB.Get("devices", deviceID, &row); err == nil {
													if cur, ok := row["tailnetIPs"].([]string); !ok || len(cur) == 0 {
														row["tailnetIPs"] = bgips
														if err := inst.DB.Put("devices", deviceID, row); err != nil {
															log.Printf("backfill-bg: persist err=%v cluster=%s device=%s", err, clusterID, deviceID)
														} else {
															log.Printf("backfill-bg: persisted backfilled tailnetIPs cluster=%s device=%s ips=%v", clusterID, deviceID, bgips)
														}
													}
												}
											}
										}
									}(inst, s.ID, id)
								}
							}
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
								"clusterId":       cl,
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
								if k == "cluster" {
									continue
								}
								if k == "clusterId" {
									rec["clusterId"] = fmt.Sprint(v)
									continue
								}
								rec[k] = v
							}
							// if no tailnetIPs in persisted record, try headscale DB best-effort
							if t, ok := rec["tailnetIPs"].([]any); !ok || len(t) == 0 {
								if id, ok := rec["id"].(string); ok && id != "" {
									ips := fetchFromHeadscale(id)
									if len(ips) > 0 {
										rec["tailnetIPs"] = ips
									}
								}
							}
							// Mark self entries and null lastSeen for UI to optionally hide them locally
							if isSelf(rec) {
								rec["self"] = true
								rec["lastSeen"] = nil
							}
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
					"clusterId":       s.ID,
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
				// attempt to attach tailnet IPs from the registry instance's TS connector
				if inst, err := deps.Registry.Get(r.Context(), s.ID); err == nil && inst != nil && inst.TS != nil {
					// call HealthWithRetry to tolerate short startup delays.
					st, det := inst.TS.HealthWithRetry(context.Background(), 5*time.Second, 3, 200*time.Millisecond)
					if st == "ok" {
						ips := []string{}
						if v, ok := det["ip"].(string); ok && v != "" {
							ips = append(ips, v)
						}
						if v, ok := det["fqdn"].(string); ok && v != "" {
							ips = append(ips, v)
						}
						if len(ips) > 0 {
							rec["tailnetIPs"] = ips
						}
					}
				}
				if isSelf(rec) {
					rec["self"] = true
					rec["lastSeen"] = nil
				}
				out = append(out, rec)
			}
		}

		httpx.JSON(w, http.StatusOK, out)
	})

	// Streaming endpoint: /v1/sites/stream
	// Streams presence changefeed events (SSE). Optional query param `cluster` to scope to one cluster.
	mux.HandleFunc("/v1/sites/stream", func(w http.ResponseWriter, r *http.Request) {
		if deps.Registry == nil {
			httpx.JSONError(w, http.StatusServiceUnavailable, "no registry", "no_registry", "stream unavailable")
			return
		}
		// Only accept the canonical clusterId query param
		clusterQ := strings.TrimSpace(r.URL.Query().Get("clusterId"))
		// prepare subscriptions
		type sub struct {
			id     string
			stream *db.ChangefeedStream
		}
		subs := []sub{}
		ctx := r.Context()
		// helper to subscribe to a presence table
		subscribe := func(clusterID string) (*db.ChangefeedStream, error) {
			inst, err := deps.Registry.Get(ctx, clusterID)
			if err != nil || inst == nil {
				return nil, fmt.Errorf("cluster not found")
			}
			mgr := inst.RDB
			if mgr == nil {
				return nil, fmt.Errorf("rdb not present")
			}
			// Normalize clusterID table suffix
			table := fmt.Sprintf("presence_%s", clusterID)
			stream, err := mgr.SubscribeTable(ctx, "", "guildnet_presence", table)
			if err != nil {
				return nil, err
			}
			return stream, nil
		}
		if clusterQ != "" {
			nid := strings.ToLower(strings.TrimSpace(clusterQ))
			if st, err := subscribe(nid); err == nil {
				subs = append(subs, sub{id: nid, stream: st})
			} else {
				httpx.JSONError(w, http.StatusBadRequest, "subscribe failed", "subscribe_failed", err.Error())
				return
			}
		} else {
			for _, s := range deps.Registry.List() {
				nid := s.ID
				if inst, err := deps.Registry.Get(ctx, nid); err == nil && inst != nil {
					present := inst.RDB != nil
					if !present {
						continue
					}
					if st, err := subscribe(nid); err == nil {
						subs = append(subs, sub{id: nid, stream: st})
					}
				}
			}
		}
		if len(subs) == 0 {
			httpx.JSONError(w, http.StatusNotFound, "no subscriptions", "no_subs", "no presence feeds available")
			return
		}
		// SSE headers
		h := w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		// fan-in channel
		ch := make(chan map[string]any, 256)
		// start readers
		for _, s := range subs {
			ss := s
			go func() {
				for ev := range ss.stream.C {
					// Emit canonical clusterId only
					m := map[string]any{"clusterId": ss.id, "event": ev}
					select {
					case ch <- m:
					case <-ctx.Done():
						return
					}
				}
			}()
		}
		// cleanup function
		defer func() {
			for _, s := range subs {
				if s.stream != nil && s.stream.Cancel != nil {
					s.stream.Cancel()
				}
			}
			close(ch)
		}()
		// main loop
		for {
			select {
			case <-ctx.Done():
				return
			case m, ok := <-ch:
				if !ok {
					return
				}
				// write SSE data field per event
				// data: <json>\n\n
				b, _ := json.Marshal(m)
				if _, err := fmt.Fprintf(w, "data: %s\n\n", string(b)); err != nil {
					return
				}
				flusher.Flush()
			}
		}
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
		// Require canonical clusterId in heartbeat payload
		clusterIDRaw, _ := payload["clusterId"].(string)
		if clusterIDRaw == "" {
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
		// Persist canonical clusterId only
		payload["clusterId"] = clusterID
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

		// Attempt to upsert a DeviceParticipant CR in-cluster when possible.
		// This must not block the heartbeat response; perform best-effort and
		// enqueue for reconciliation on failure.
		go func(p map[string]any, inst *cluster.Instance, devID string) {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("deviceparticipant upsert panic: %v", r)
				}
			}()
			if inst == nil || inst.Dyn == nil {
				// Persist to pending queue for later reconciliation
				if inst != nil && inst.DB != nil {
					_ = inst.DB.Put("pending_deviceparticipants", devID, p)
				}
				return
			}
			// build spec and status maps from payload
			spec := map[string]any{}
			if v, ok := p["id"]; ok {
				spec["id"] = v
			}
			if v, ok := p["name"]; ok {
				spec["name"] = v
			}
			if v, ok := p["tailnetIPs"]; ok {
				spec["tailnetIPs"] = v
			}
			if v, ok := p["hostappVersion"]; ok {
				spec["hostappVersion"] = v
			}
			// resources grouping
			res := map[string]any{}
			if v, ok := p["cpuMilli"]; ok {
				res["cpuMilli"] = v
			}
			if v, ok := p["memoryMB"]; ok {
				res["memoryMb"] = v
			}
			if v, ok := p["storageMB"]; ok {
				res["storageMb"] = v
			}
			if len(res) > 0 {
				spec["resources"] = res
			}
			if v, ok := p["endpoint"]; ok {
				spec["endpoint"] = v
			}
			status := map[string]any{}
			if v, ok := p["lastSeen"]; ok {
				if t, ok2 := v.(time.Time); ok2 {
					status["lastSeen"] = t.UTC().Format(time.RFC3339)
				} else {
					status["lastSeen"] = fmt.Sprint(v)
				}
			}
			status["state"] = "online"
			// Call create/update helper
			if _, err := k8s.CreateOrUpdateDeviceParticipant(context.Background(), inst.Dyn, "guildnet-system", devID, spec, status); err != nil {
				cid := p["clusterId"]
				if cid == nil {
					cid = p["cluster"]
				}
				log.Printf("deviceparticipant upsert failed cluster=%v device=%s err=%v", cid, devID, err)
				// enqueue for reconciliation
				if inst.DB != nil {
					_ = inst.DB.Put("pending_deviceparticipants", devID, p)
				}
			}
		}(payload, inst, deviceID)

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
