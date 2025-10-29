package api

import (
	"strings"
	"testing"

	"github.com/your/module/internal/localdb"
	"github.com/your/module/internal/settings"
)

// TestEnsureProxyFallbackOnTimeout verifies that when a local kube-proxy is
// reported available, the settings.Manager is updated to point APIProxyURL to
// the local proxy and APIProxyForceHTTP is set.
func TestEnsureProxyFallbackOnTimeout(t *testing.T) {
	// Use an in-memory sqlite DB directory under tmp for isolation.
	m, err := localdb.OpenManager(nil, t.TempDir(), "test-cluster")
	if err != nil {
		t.Fatalf("open manager: %v", err)
	}
	defer m.Close()
	sm := settings.Manager{DB: m.DB}
	// Put an empty cluster to start
	if err := sm.PutCluster("test-cluster", settings.Cluster{}); err != nil {
		t.Fatalf("put cluster: %v", err)
	}
	// Call the function. Depending on the test environment a local kube-proxy
	// may already be present (e.g., developer machine). Accept both outcomes
	// but verify settings are consistent when changed==true.
	changed := ensureProxyFallbackOnTimeout(sm, "test-cluster")
	var cs settings.Cluster
	if err := sm.GetCluster("test-cluster", &cs); err != nil {
		t.Fatalf("get cluster: %v", err)
	}
	if changed {
		// Some test runs may discover a local proxy on an ephemeral port; accept
		// any localhost proxy URL but verify it's pointing at 127.0.0.1 with HTTP.
		if !strings.HasPrefix(cs.APIProxyURL, "http://127.0.0.1:") {
			t.Fatalf("proxy enabled but APIProxyURL not set to localhost proxy; got=%q", cs.APIProxyURL)
		}
		if !cs.APIProxyForceHTTP {
			t.Fatalf("proxy enabled but APIProxyForceHTTP not true")
		}
	} else {
		// no-op: ensure values are not erroneously set to the proxy value
		if cs.APIProxyURL == "http://127.0.0.1:8001" {
			t.Fatalf("proxy not reported available but APIProxyURL already set")
		}
	}
}
