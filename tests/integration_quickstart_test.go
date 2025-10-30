package tests

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// This integration test is intentionally skipped by default. To run locally set
// CI_QUICKSTART=1 and ensure a Kubernetes cluster and HostApp are available (use scripts/k0s-node-up.sh for local k0s).
func TestIntegration_QuickstartSubset(t *testing.T) {
	if os.Getenv("CI_QUICKSTART") != "1" {
		t.Skip("skipping quickstart integration; set CI_QUICKSTART=1 to run")
	}
	root := ""
	// Find repo root relative to test binary
	cwd, _ := os.Getwd()
	root = filepath.Join(cwd, "..")

	// Run verify-cluster.sh. This script no longer creates disposable clusters;
	// ensure KUBECONFIG points to a reachable cluster before running (use k0s-node-up.sh if needed).
	verifyCmd := exec.Command("bash", "scripts/verify-cluster.sh")
	verifyCmd.Env = append(os.Environ(), "NO_DELETE=1", "SKIP_METALLB=1")
	// Limit runtime to avoid long CI hangs
	var bout bytes.Buffer
	verifyCmd.Stdout = &bout
	verifyCmd.Stderr = &bout
	t.Log("starting verify-cluster.sh (this may take ~30s locally)")
	if err := verifyCmd.Start(); err != nil {
		t.Fatalf("failed to start verify-cluster: %v", err)
	}
	// Wait with a generous timeout for the script to produce guildnet.config
	done := make(chan error)
	go func() { done <- verifyCmd.Wait() }()
	select {
	case <-time.After(5 * time.Minute):
		_ = verifyCmd.Process.Kill()
		t.Fatalf("verify-cluster.sh timed out; output:\n%s", bout.String())
	case err := <-done:
		if err != nil {
			t.Fatalf("verify-cluster.sh failed: %v\noutput:\n%s", err, bout.String())
		}
	}

	// Read generated guildnet.config
	cfgPath := filepath.Join(root, "guildnet.config")
	b, err := ioutil.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read guildnet.config: %v", err)
	}
	var cf map[string]any
	if err := json.Unmarshal(b, &cf); err != nil {
		t.Fatalf("unmarshal guildnet.config: %v", err)
	}
	clusterObj, _ := cf["cluster"].(map[string]any)
	name := ""
	if clusterObj != nil {
		if v, ok := clusterObj["name"].(string); ok {
			name = v
		}
	}
	if name == "" {
		// fallback to random generated name
		name = "test-cluster"
	}

	// Query HostApp for clusters and find ID by name
	hostapp := os.Getenv("HOSTAPP_URL")
	if hostapp == "" {
		hostapp = "https://127.0.0.1:8090"
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(hostapp + "/api/deploy/clusters")
	if err != nil {
		t.Fatalf("failed to query hostapp clusters: %v", err)
	}
	defer resp.Body.Close()
	var arr []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&arr); err != nil {
		t.Fatalf("decode clusters: %v", err)
	}
	clusterID := ""
	for _, it := range arr {
		if n, _ := it["name"].(string); n == name {
			if id, _ := it["id"].(string); id != "" {
				clusterID = id
				break
			}
		}
	}
	if clusterID == "" {
		t.Fatalf("could not find cluster id for name %s", name)
	}

	// Run verify-workspace.sh with found cluster ID
	cmd := exec.Command("bash", "scripts/verify-workspace.sh", clusterID, "integration-test-ws")
	cmd.Env = append(os.Environ(), "HOSTAPP_URL="+hostapp)
	out, err := cmd.CombinedOutput()
	t.Logf("verify-workspace output:\n%s", string(out))
	if err != nil {
		t.Fatalf("verify-workspace failed: %v", err)
	}
}
