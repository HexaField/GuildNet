package api

import (
	"encoding/json"
	"net/http"

	"github.com/your/module/internal/hostapp"
)

// joinRequest is the expected payload for site join
type joinRequest struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	NodeCount int32  `json:"nodeCount"`
	CPUMilli  int32  `json:"cpuMilli"`
	MemoryMB  int32  `json:"memoryMb"`
}

// NewSiteHandlers returns handlers bound to the given registry.
func NewSiteHandlers(reg *hostapp.Registry) (join http.HandlerFunc, leave http.HandlerFunc, heartbeat http.HandlerFunc) {
	join = func(w http.ResponseWriter, r *http.Request) {
		var req joinRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		reg.Register(&hostapp.Site{ID: req.ID, Name: req.Name, NodeCount: req.NodeCount, CPU: req.CPUMilli, MemoryMB: req.MemoryMB})
		w.WriteHeader(http.StatusOK)
	}

	leave = func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		reg.Unregister(req.ID)
		w.WriteHeader(http.StatusOK)
	}

	heartbeat = func(w http.ResponseWriter, r *http.Request) {
		var req joinRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		// update LastSeen and stats
		reg.Register(&hostapp.Site{ID: req.ID, Name: req.Name, NodeCount: req.NodeCount, CPU: req.CPUMilli, MemoryMB: req.MemoryMB})
		w.WriteHeader(http.StatusOK)
	}

	return
}
