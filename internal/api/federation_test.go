package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/your/module/internal/cluster"
	"github.com/your/module/internal/localdb"
)

func TestRegisterFederationAPIs_SitesIncludesTailnetIPs(t *testing.T) {
	// prepare per-cluster state dir with a device record
	td := t.TempDir()
	clusterID := "testcluster"
	cldir := filepath.Join(td, clusterID)
	if err := os.MkdirAll(cldir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := localdb.Open(cldir)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.EnsureBuckets("devices")
	deviceRec := map[string]any{
		"id":         "guildnet-test-host",
		"name":       "guildnet-test-host",
		"tailnetIPs": []string{"100.101.102.103"},
	}
	if err := db.Put("devices", "guildnet-test-host", deviceRec); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	// create registry that will seed from td
	r := cluster.NewRegistry(cluster.Options{StateDir: td})
	deps := Deps{Registry: r}
	mux := http.NewServeMux()
	RegisterFederationAPIs(mux, deps)
	// run server
	s := httptest.NewServer(mux)
	defer s.Close()
	resp, err := http.Get(s.URL + "/v1/sites")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var arr []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&arr); err != nil {
		t.Fatal(err)
	}
	if len(arr) == 0 {
		t.Fatalf("expected at least one site, got none")
	}
	found := false
	for _, it := range arr {
		if id := it["clusterId"]; id == clusterID {
			// ensure tailnetIPs present
			if ips, ok := it["tailnetIPs"].([]any); ok {
				if len(ips) > 0 && ips[0] == "100.101.102.103" {
					found = true
					break
				}
			}
		}
	}
	if !found {
		t.Fatalf("did not find expected cluster with tailnet IPs in response: %v", arr)
	}
}
