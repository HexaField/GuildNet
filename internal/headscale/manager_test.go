package headscale

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/your/module/internal/localdb"
	"github.com/your/module/internal/secrets"
)

func TestManagerCreateUsesScriptAndStoresCred(t *testing.T) {
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
  echo '{"action":"up","server_url":"http://127.0.0.1:8081","container":"guildnet-headscale","image":"ghcr.io/juanfont/headscale:0.27.0","port":8081,"data_dir":"/tmp"}'
  exit 0
fi
if [ "$cmd" = "preauth-key" ]; then
  echo "tskey-TESTKEY123"
  exit 0
fi
echo "ok"
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

	sec, _ := secrets.New("")

	// create initial headscale record
	id := "test-hs"
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

	m := New(db, sec)
	if err := m.Create(context.Background(), id, func(step, msg string, kv map[string]any) {}); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	var out map[string]any
	if err := db.Get("headscales", id, &out); err != nil {
		t.Fatal(err)
	}
	if out["state"] != "ready" {
		t.Fatalf("expected ready state, got %#v", out["state"])
	}
	// check credential stored
	credKey := "hs:test-hs:preauth"
	var cred map[string]any
	if err := db.Get("credentials", credKey, &cred); err != nil {
		t.Fatalf("credential not stored: %v", err)
	}
	// value may be encrypted by secrets.Manager; attempt decrypt when flagged
	if encFlag, _ := cred["encrypted"].(bool); encFlag {
		dec, err := sec.Decrypt(fmt.Sprint(cred["value"]))
		if err != nil {
			t.Fatalf("failed to decrypt cred: %v", err)
		}
		if dec != "tskey-TESTKEY123" {
			t.Fatalf("unexpected decrypted cred value: %v", dec)
		}
	} else {
		if val := cred["value"]; val != "tskey-TESTKEY123" {
			t.Fatalf("unexpected cred value: %v", val)
		}
	}
}
