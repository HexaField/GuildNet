package api

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// This is an integration/e2e test that starts a real hostapp binary against
// a temporary HOME/state directory and a stubbed scripts/headscale-run.sh
// helper. It is skipped by default; enable by setting RUN_INTEGRATION=1.
func TestHostAppHeadscaleFlow(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION") != "1" {
		t.Skip("skipping integration test; set RUN_INTEGRATION=1 to enable")
	}

	tmp, err := os.MkdirTemp("", "gn-int-")
	if err != nil {
		t.Fatalf("tmpdir: %v", err)
	}
	defer os.RemoveAll(tmp)

	// build hostapp into tmp/bin
	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	outPath := filepath.Join(binDir, "hostapp")
	build := exec.Command("go", "build", "-o", outPath, "./cmd/hostapp")
	build.Env = append(os.Environ(), "GOFLAGS=")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, string(b))
	}

	// create a stub scripts/headscale-run.sh next to the built binary
	scriptsDir := filepath.Join(tmp, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	script := filepath.Join(scriptsDir, "headscale-run.sh")
	scriptContent := `#!/usr/bin/env bash
cmd="$1"
if [ "$cmd" = "up" ]; then
  # emit JSON last-line compatible with manager parsing
  echo '{"server_url":"http://127.0.0.1:8082","container":"hs-test","image":"ghcr.io/juanfont/headscale:0.27.0","port":8082}'
  exit 0
fi
if [ "$cmd" = "preauth-key" ]; then
  # accept: preauth-key <user> --json
  echo '{"hex":"abcd1234","tskey":"tskey-TEST"}'
  exit 0
fi
echo '{}'
exit 0
`
	if err := os.WriteFile(script, []byte(scriptContent), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	// craft a minimal config under HOME/.guildnet/config.json
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	// pick a free port
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().(*net.TCPAddr)
	port := addr.Port
	l.Close()

	cfgDir := filepath.Join(home, ".guildnet")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatalf("mkdir cfg: %v", err)
	}
	cfg := fmt.Sprintf(`{"login_server":"http://127.0.0.1:8082","auth_key":"tskey-TEST","hostname":"test-host","listen_local":"127.0.0.1:%d","dial_timeout_ms":5000}`, port)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// ensure state dir exists under HOME
	if err := os.MkdirAll(filepath.Join(cfgDir, "state"), 0o700); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}

	// start hostapp
	hostapp := outPath
	cmd := exec.Command(hostapp, "serve")
	cmd.Env = append(os.Environ(), "HOME="+home, "LISTEN_LOCAL=127.0.0.1:"+fmt.Sprint(port), "GN_HEADSCALE_RECONCILE_INTERVAL=1s", "GN_DISABLE_PDEATHSIG=1")
	// ensure the built binary will locate our scripts: executable dir is tmp/bin, script candidate is ../scripts -> tmp/scripts
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start hostapp: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		cmd.Process.Release()
	}()

	// wait for health endpoint
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	url := fmt.Sprintf("https://127.0.0.1:%d/healthz", port)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			goto ready
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("hostapp did not become healthy on %s", url)
ready:

	// create headscale via API
	createURL := fmt.Sprintf("https://127.0.0.1:%d/api/deploy/headscale", port)
	resp, err := client.Post(createURL, "application/json", nil)
	if err != nil {
		t.Fatalf("create headscale request failed: %v", err)
	}
	if resp.StatusCode != 202 {
		t.Fatalf("create headscale unexpected status: %d", resp.StatusCode)
	}
	var cre map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&cre); err != nil {
		t.Fatalf("decode create resp: %v", err)
	}
	resp.Body.Close()
	id, _ := cre["id"].(string)
	if id == "" {
		t.Fatalf("create did not return id")
	}

	// poll for ready state
	getURL := fmt.Sprintf("https://127.0.0.1:%d/api/deploy/headscale/%s", port, id)
	deadline = time.Now().Add(30 * time.Second)
	var rec map[string]any
	for time.Now().Before(deadline) {
		r2, err := client.Get(getURL)
		if err == nil && r2.StatusCode == 200 {
			_ = json.NewDecoder(r2.Body).Decode(&rec)
			r2.Body.Close()
			if rec["state"] == "ready" {
				goto done
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("headscale record did not reach ready state in time; last=%v", rec)
done:

	// verify settings.tailscale contains preauth key persisted
	settingsURL := fmt.Sprintf("https://127.0.0.1:%d/api/settings/tailscale", port)
	r3, err := client.Get(settingsURL)
	if err != nil {
		t.Fatalf("settings get failed: %v", err)
	}
	if r3.StatusCode != 200 {
		t.Fatalf("settings get status: %d", r3.StatusCode)
	}
	var ts map[string]any
	if err := json.NewDecoder(r3.Body).Decode(&ts); err != nil {
		t.Fatalf("decode tailscale settings: %v", err)
	}
	r3.Body.Close()
	if ts["preauth_key"] == nil {
		t.Fatalf("preauth_key missing in settings: %v", ts)
	}
}
