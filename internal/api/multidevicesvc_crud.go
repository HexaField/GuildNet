package api

import (
	"encoding/json"
	"net/http"

	"github.com/your/module/internal/httpx"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Register CRUD endpoints for FederatedService under /api/v1
func registerFederationCRUD(mux *http.ServeMux, deps Deps) {
	gvr := schema.GroupVersionResource{Group: "guildnet.io", Version: "v1alpha1", Resource: "federatedservices"}

	// List across clusters (same as earlier but under /api/v1)
	mux.HandleFunc("/api/v1/federatedservices", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if deps.Registry == nil {
			httpx.JSON(w, http.StatusOK, []any{})
			return
		}
		out := []any{}
		for _, s := range deps.Registry.List() {
			inst, err := deps.Registry.Get(r.Context(), s.ID)
			if err != nil || inst == nil || inst.Dyn == nil {
				continue
			}
			if list, err := inst.Dyn.Resource(gvr).Namespace(metav1.NamespaceAll).List(r.Context(), metav1.ListOptions{}); err == nil {
				for _, it := range list.Items {
					out = append(out, map[string]any{"clusterId": s.ID, "namespace": it.GetNamespace(), "name": it.GetName()})
				}
			}
		}
		httpx.JSON(w, http.StatusOK, out)
	})

	// Mutating paths: POST to create, PUT to update, DELETE to delete. Require cluster query param.
	mux.HandleFunc("/api/v1/federatedservices/cluster", func(w http.ResponseWriter, r *http.Request) {
		clusterID := r.URL.Query().Get("clusterId")
		if clusterID == "" {
			httpx.JSONError(w, http.StatusBadRequest, "clusterId required", "bad_request", "clusterId query parameter required")
			return
		}
		if deps.Registry == nil {
			httpx.JSONError(w, http.StatusServiceUnavailable, "registry unavailable", "no_registry", "registry unavailable")
			return
		}
		inst, err := deps.Registry.Get(r.Context(), clusterID)
		if err != nil || inst == nil || inst.Dyn == nil {
			httpx.JSONError(w, http.StatusNotFound, "cluster not found or dynamic client unavailable", "no_cluster", "cluster not found or dynamic client unavailable")
			return
		}
		switch r.Method {
		case http.MethodPost:
			var u unstructured.Unstructured
			if err := json.NewDecoder(r.Body).Decode(&u.Object); err != nil {
				httpx.JSONError(w, http.StatusBadRequest, "invalid body", "bad_request", err.Error())
				return
			}
			if _, err := inst.Dyn.Resource(gvr).Namespace(u.GetNamespace()).Create(r.Context(), &u, metav1.CreateOptions{}); err != nil {
				httpx.JSONError(w, http.StatusInternalServerError, "create failed", "create_failed", err.Error())
				return
			}
			httpx.JSON(w, http.StatusCreated, map[string]any{"ok": true})
			return
		case http.MethodPut:
			var u unstructured.Unstructured
			if err := json.NewDecoder(r.Body).Decode(&u.Object); err != nil {
				httpx.JSONError(w, http.StatusBadRequest, "invalid body", "bad_request", err.Error())
				return
			}
			name := u.GetName()
			ns := u.GetNamespace()
			if name == "" || ns == "" {
				httpx.JSONError(w, http.StatusBadRequest, "namespace and name required", "bad_request", "object metadata.name and metadata.namespace required")
				return
			}
			if _, err := inst.Dyn.Resource(gvr).Namespace(ns).Update(r.Context(), &u, metav1.UpdateOptions{}); err != nil {
				httpx.JSONError(w, http.StatusInternalServerError, "update failed", "update_failed", err.Error())
				return
			}
			httpx.JSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		case http.MethodDelete:
			ns := r.URL.Query().Get("ns")
			name := r.URL.Query().Get("name")
			if ns == "" || name == "" {
				httpx.JSONError(w, http.StatusBadRequest, "ns and name required", "bad_request", "ns and name query parameters required")
				return
			}
			if err := inst.Dyn.Resource(gvr).Namespace(ns).Delete(r.Context(), name, metav1.DeleteOptions{}); err != nil {
				httpx.JSONError(w, http.StatusInternalServerError, "delete failed", "delete_failed", err.Error())
				return
			}
			httpx.JSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
	})
}
