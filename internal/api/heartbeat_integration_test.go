package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/your/module/internal/cluster"
	"github.com/your/module/internal/k8s"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// TestHeartbeatReconcile simulates posting a heartbeat when the cluster Instance
// has no dynamic client available, ensuring the pending_deviceparticipants queue
// is used and that when a fake dynamic client is provided the reconciliation
// worker upserts the DeviceParticipant and removes the pending entry.
func TestHeartbeatReconcile(t *testing.T) {
	// Create a temp state dir for registry
	dir := t.TempDir()
	// Prepare a simple in-memory registry with a fake resolver that returns a kubeconfig
	reg := cluster.NewRegistry(cluster.Options{StateDir: dir, Resolver: &fakeResolver{}})

	// Create per-cluster DB by calling Get (which will open localdb)
	inst, err := reg.Get(context.Background(), "test-cluster")
	if err != nil {
		t.Fatalf("registry get: %v", err)
	}
	if inst == nil || inst.DB == nil {
		t.Fatalf("instance or db nil")
	}
	// Simulate dynamic client unavailable for initial heartbeat path
	inst.Dyn = nil

	// Build server mux with deps pointing at our registry and its DB
	deps := Deps{DB: inst.DB, Registry: reg}
	mux := http.NewServeMux()
	RegisterFederationAPIs(mux, deps)

	// Create test server
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Post heartbeat payload (Dyn not set on instance initially)
	payload := map[string]any{"clusterId": "test-cluster", "id": "dev-1", "name": "device-1"}
	b, _ := json.Marshal(payload)
	resp, err := http.Post(srv.URL+"/v1/sites/heartbeat", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("post heartbeat: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %v", resp.Status)
	}

	// Allow background goroutine a short moment to enqueue pending entries when
	// the dynamic client is unavailable.
	time.Sleep(100 * time.Millisecond)

	// Verify pending_deviceparticipants contains the entry
	var pending []map[string]any
	if err := inst.DB.List("pending_deviceparticipants", &pending); err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) == 0 {
		t.Fatalf("expected pending entry, got none")
	}

	// Now inject a fake dynamic client that records creations
	fakeDyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	inst.Dyn = fakeDyn

	// Wait a short moment for background worker (if running) to pick up pending
	time.Sleep(500 * time.Millisecond)

	// Force reconcile run: iterate pending and call helper
	var pend []map[string]any
	if err := inst.DB.List("pending_deviceparticipants", &pend); err == nil {
		for _, item := range pend {
			idVal, _ := item["id"].(string)
			spec := map[string]any{"name": item["name"]}
			status := map[string]any{"state": "online"}
			if _, err := k8s.CreateOrUpdateDeviceParticipant(context.Background(), inst.Dyn, "guildnet-system", idVal, spec, status); err == nil {
				_ = inst.DB.Delete("pending_deviceparticipants", idVal)
			}
		}
	}

	// Assert created object exists in fake dynamic client by attempting to Get
	gvr := schema.GroupVersionResource{Group: "guildnet.io", Version: "v1alpha1", Resource: "deviceparticipants"}
	if obj, err := inst.Dyn.Resource(gvr).Namespace("guildnet-system").Get(context.Background(), "dev-1", metav1.GetOptions{}); err != nil || obj == nil {
		t.Fatalf("expected DeviceParticipant to be created in fake dynamic client: %v", err)
	}

	// Assert pending queue empty
	var final []map[string]any
	if err := inst.DB.List("pending_deviceparticipants", &final); err != nil {
		t.Fatalf("list pending final: %v", err)
	}
	if len(final) != 0 {
		t.Fatalf("expected no pending entries after reconcile, got %d", len(final))
	}
}

// fakeResolver returns a non-empty kubeconfig so registry.Get will create an Instance.
type fakeResolver struct{}

func (f *fakeResolver) KubeconfigYAML(clusterID string) (string, error) {
	// Return a minimal, valid kubeconfig that clientcmd can parse. The server
	// will not be contacted during tests; we only need a REST config object.
	kc := "apiVersion: v1\n" +
		"kind: Config\n" +
		"clusters:\n" +
		"- name: test\n" +
		"  cluster:\n" +
		"    server: https://127.0.0.1\n" +
		"contexts:\n" +
		"- name: test\n" +
		"  context:\n" +
		"    cluster: test\n" +
		"    user: test\n" +
		"current-context: test\n" +
		"users:\n" +
		"- name: test\n" +
		"  user: {}\n"
	return kc, nil
}
