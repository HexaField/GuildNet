package hostapp

import (
	"sync"
	"time"
)

// Site represents a joined site record
type Site struct {
	ID        string
	Name      string
	LastSeen  time.Time
	NodeCount int32
	CPU       int32
	MemoryMB  int32
}

// Registry is a simple in-memory registry of sites.
type Registry struct {
	mu    sync.RWMutex
	sites map[string]*Site
}

// NewRegistry creates a new Registry instance.
func NewRegistry() *Registry {
	return &Registry{sites: make(map[string]*Site)}
}

// Register adds or updates a site.
func (r *Registry) Register(s *Site) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s.LastSeen = time.Now().UTC()
	r.sites[s.ID] = s
}

// Unregister removes a site by ID.
func (r *Registry) Unregister(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sites, id)
}

// List returns a snapshot of registered sites.
func (r *Registry) List() []*Site {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Site, 0, len(r.sites))
	for _, s := range r.sites {
		out = append(out, s)
	}
	return out
}

// Get returns a site by id or nil.
func (r *Registry) Get(id string) *Site {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sites[id]
}
