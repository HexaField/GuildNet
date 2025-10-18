package api

import (
	"net/http"
	"strings"

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
		lst := deps.Registry.List()
		httpx.JSON(w, http.StatusOK, lst)
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
