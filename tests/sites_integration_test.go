package tests

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/your/module/internal/api"
	"github.com/your/module/internal/localdb"
)

// Quick integration test: create a per-cluster localdb entry under a temporary
// HOME and verify that GET /v1/sites returns the persisted tailnetIPs via the
// local state-scan path.
func TestSitesHeartbeatPersistence(t *testing.T) {
	// Use a temporary HOME so RegisterFederationAPIs will scan ~/.guildnet/state
	tmpdir := t.TempDir()
	// ensure ~/.guildnet/state exists under tmpdir
	stateDir := filepath.Join(tmpdir, ".guildnet", "state", "test-cluster")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	// open per-cluster local DB and insert a device record
	cdb, err := localdb.Open(stateDir)
	if err != nil {
		t.Fatalf("open per-cluster db: %v", err)
	}
	defer cdb.Close()
	dev := map[string]any{"clusterId": "test-cluster", "id": "node-1", "name": "node-1", "tailnetIPs": []string{"10.0.0.5"}}
	if err := cdb.Put("devices", "node-1", dev); err != nil {
		t.Fatalf("put device: %v", err)
	}
	// Set HOME so the API scanning code picks up our tmp state dir
	oldHome := os.Getenv("HOME")
	defer os.Setenv("HOME", oldHome)
	os.Setenv("HOME", tmpdir)

	// Minimal deps for API: no Registry required (we want the local-scan path)
	deps := api.Deps{DB: nil, Registry: nil}
	mux := http.NewServeMux()
	api.RegisterFederationAPIs(mux, deps)

	// Fetch /v1/sites
	req2 := httptest.NewRequest(http.MethodGet, "/v1/sites", nil)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("sites get failed: code=%d body=%s", w2.Code, w2.Body.String())
	}
	var out []map[string]any
	if err := json.Unmarshal(w2.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json from sites: %v", err)
	}
	found := false
	for _, r := range out {
		if id, _ := r["id"].(string); id == "node-1" {
			if arr, ok := r["tailnetIPs"].([]any); ok && len(arr) == 1 && arr[0] == "10.0.0.5" {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatalf("expected tailnetIPs to be present in /v1/sites response, got: %v", out)
	}
}
