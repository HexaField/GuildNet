package api

import (
	"bytes"
	"context"
	"encoding/json"

	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/your/module/internal/localdb"
	"github.com/your/module/internal/secrets"
)

func TestHeadscaleAPICreateAndPreauth(t *testing.T) {
	td := t.TempDir()
	// create scripts/headscale-run.sh that responds to up and preauth-key
	scrDir := filepath.Join(td, "scripts")
	if err := os.MkdirAll(scrDir, 0o755); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(scrDir, "headscale-run.sh")
	script := `#!/usr/bin/env bash
cmd="$1"; shift || true
if [ "$cmd" = "up" ]; then
  echo '{"action":"up","server_url":"http://127.0.0.1:8081","container":"guildnet-headscale","image":"ghcr.io/juanfont/headscale:0.27.0","port":8081,"data_dir":"/tmp"}'
  exit 0
fi
if [ "$cmd" = "preauth-key" ]; then
  echo "tskey-APIKEY123"
  exit 0
fi
echo ok
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	// open local db
	dbdir := filepath.Join(td, "state")
	if err := os.MkdirAll(dbdir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := localdb.Open(dbdir)
	if err != nil {
		t.Fatal(err)
	}

	sec, _ := secrets.New("")

	deps := Deps{DB: db, Secrets: sec}
	srv := httptest.NewServer(Router(deps))
	defer srv.Close()

	// change cwd so scripts are found
	cwd, _ := os.Getwd()
	if err := os.Chdir(td); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)

	// create headscale
	body := map[string]any{"name": "test-hs"}
	b, _ := json.Marshal(body)
	res, err := http.Post(srv.URL+"/api/deploy/headscale", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", res.StatusCode)
	}
	var resp map[string]any
	_ = json.NewDecoder(res.Body).Decode(&resp)
	id, _ := resp["id"].(string)
	if id == "" {
		t.Fatal("no id returned")
	}

	// wait for headscale record to become ready
	ok := false
	for i := 0; i < 20; i++ {
		var rec map[string]any
		if err := db.Get("headscales", id, &rec); err == nil {
			if rec["state"] == "ready" {
				ok = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ok {
		t.Fatalf("headscale record did not become ready in time")
	}

	// call preauth-key action to store a manual key
	reqBody := map[string]any{"value": "manual-tskey-XYZ"}
	rb, _ := json.Marshal(reqBody)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/api/deploy/headscale/"+id+"?action=preauth-key", bytes.NewReader(rb))
	req.Header.Set("Content-Type", "application/json")
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("preauth-key returned %d", r2.StatusCode)
	}
	// verify credential stored
	var cred map[string]any
	if err := db.Get("credentials", "hs:"+id+":preauth", &cred); err != nil {
		t.Fatalf("credential not found: %v", err)
	}

}

func TestClustersAPICreateAttach(t *testing.T) {
	td := t.TempDir()
	scrDir := filepath.Join(td, "scripts")
	if err := os.MkdirAll(scrDir, 0o755); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(scrDir, "k0s-node-up.sh")
	// script writes kubeconfig to GN_KUBECONFIG
	script := `#!/usr/bin/env bash
echo "apiVersion: v1" > "$GN_KUBECONFIG"
echo "clusters: []" >> "$GN_KUBECONFIG"
echo ok
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	dbdir := filepath.Join(td, "state")
	if err := os.MkdirAll(dbdir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := localdb.Open(dbdir)
	if err != nil {
		t.Fatal(err)
	}
	sec, _ := secrets.New("")
	deps := Deps{DB: db, Secrets: sec}
	srv := httptest.NewServer(Router(deps))
	defer srv.Close()

	// set GN_KUBECONFIG to a path under tempdir so script writes there
	kcPath := filepath.Join(td, "kubeconfig")
	os.Setenv("GN_KUBECONFIG", kcPath)

	cwd, _ := os.Getwd()
	if err := os.Chdir(td); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)

	// create cluster
	body := map[string]any{"name": "test-cluster"}
	b, _ := json.Marshal(body)
	res, err := http.Post(srv.URL+"/api/deploy/clusters", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", res.StatusCode)
	}
	var resp map[string]any
	_ = json.NewDecoder(res.Body).Decode(&resp)
	id, _ := resp["id"].(string)
	if id == "" {
		t.Fatal("no id returned")
	}

	// wait for cluster record to exist
	ok := false
	for i := 0; i < 20; i++ {
		var rec map[string]any
		if err := db.Get("clusters", id, &rec); err == nil {
			ok = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ok {
		t.Fatalf("cluster record not created")
	}
}
