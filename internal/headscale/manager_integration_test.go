package headscale

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/your/module/internal/localdb"
	"github.com/your/module/internal/settings"
	"github.com/your/module/internal/secrets"
)

func TestManagerCreate_PersistsSettingsAndCredentials(t *testing.T) {
	td := t.TempDir()
	// create scripts/headscale-run.sh in temp dir
	scrDir := filepath.Join(td, "scripts")
	if err := os.MkdirAll(scrDir, 0o755); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(scrDir, "headscale-run.sh")
	script := `#!/usr/bin/env bash
cmd="$1"; shift || true
if [ "$cmd" = "up" ]; then
  echo '{"server_url":"http://127.0.0.1:8082","container":"hstub","image":"ghcr.io/juanfont/headscale:0.27.0","port":8082}'
  exit 0
fi
if [ "$cmd" = "preauth-key" ]; then
  echo '{"hex":"d8a3743ca2aefc23682832cdab2a819aba64a03fae845fb245fb2","tskey":"tskey-TESTKEY"}'
  exit 0
fi
echo "{}"
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	// open a fresh localdb in the temp dir
	dbdir := filepath.Join(td, "state")
	if err := os.MkdirAll(dbdir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := localdb.Open(dbdir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// create initial headscale record
	id := "test-hs-2"
	rec := map[string]any{
		"id":        id,
		"name":      "test",
		"state":     "creating",
		"createdAt": time.Now().UTC().Format(time.RFC3339),
	}
	if err := db.Put("headscales", id, rec); err != nil {
		t.Fatal(err)
	}

	// chdir to temp dir so Manager finds ./scripts/headscale-run.sh
	cwd, _ := os.Getwd()
	if err := os.Chdir(td); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)

	// no secrets manager to keep value stored in plaintext for test
	m := New(db, nil)
	if err := m.Create(context.Background(), id, func(step, msg string, kv map[string]any) {}); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// validate settings were updated
	mgr := settings.Manager{DB: db}
	var ts settings.Tailscale
	if err := mgr.GetTailscale(&ts); err != nil {
		t.Fatalf("GetTailscale failed: %v", err)
	}
	if ts.PreauthKey != "tskey-TESTKEY" {
		t.Fatalf("expected tskey in settings, got %q", ts.PreauthKey)
	}
	if ts.LoginServer == "" {
		t.Fatalf("expected login_server in settings, empty")
	}

	// validate credentials stored (raw hex)
	var cred map[string]any
	if err := db.Get("credentials", "hs:test-hs-2:preauth", &cred); err != nil {
		t.Fatalf("credential not stored: %v", err)
	}
	if val, ok := cred["value"].(string); !ok || val == "" {
		t.Fatalf("credential value missing or not string: %#v", cred)
	}

	// also ensure decryption path would work if secrets manager provided
	sec, _ := secrets.New("")
	if encFlag, _ := cred["encrypted"].(bool); encFlag {
		if _, err := sec.Decrypt(valOrString(cred["value"])); err != nil {
			t.Fatalf("decrypt failed: %v", err)
		}
	}
}

func valOrString(v any) string {
	if v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}
