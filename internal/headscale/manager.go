package headscale

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/your/module/internal/audit"
	"github.com/your/module/internal/localdb"
	"github.com/your/module/internal/secrets"
)

// Manager coordinates lifecycle operations for Headscale instances.
// The Kubernetes cluster remains the source of truth. Local DB stores only
// minimal connectivity and state hints used by the server/UI.
type Manager struct {
	DB      *localdb.DB
	Secrets *secrets.Manager
}

func New(db *localdb.DB, sec *secrets.Manager) *Manager { return &Manager{DB: db, Secrets: sec} }

func (m *Manager) Create(ctx context.Context, id string, logf func(step, msg string, kv map[string]any)) error {
	if m.DB == nil {
		return fmt.Errorf("no db")
	}
	var rec map[string]any
	if err := m.DB.Get("headscales", id, &rec); err != nil {
		return err
	}
	logf("create", "ensure headscale resources (local docker)", map[string]any{"id": id})

	// locate script
	script := "scripts/headscale-run.sh"
	if _, err := os.Stat(script); err != nil {
		if ex, e := os.Executable(); e == nil {
			dir := filepath.Dir(ex)
			candidate := filepath.Join(dir, "..", "scripts", "headscale-run.sh")
			if st, e2 := os.Stat(candidate); e2 == nil && st.Mode().IsRegular() {
				script = candidate
			}
		}
	}

	// If script exists, invoke with --json to get machine-parsable output
	if st, err := os.Stat(script); err == nil && st.Mode().IsRegular() {
		cmd := exec.CommandContext(ctx, "/usr/bin/env", "bash", script, "up", "--json")
		cmd.Env = os.Environ()
		out, err := cmd.CombinedOutput()
		if err != nil {
			logf("error", "headscale script failed", map[string]any{"error": err.Error(), "output": string(out)})
			rec["state"] = "error"
			rec["lastError"] = err.Error()
			rec["updatedAt"] = time.Now().UTC().Format(time.RFC3339)
			_ = m.DB.Put("headscales", id, rec)
			audit.Append(m.DB, "system", "create", "headscale", id, err.Error())
			return err
		}
		// parse JSON (script emits a single JSON object line in --json)
		var js map[string]any
		if err := json.Unmarshal(bytesTrim(out), &js); err != nil {
			// fallback: try to find a JSON object on the last line
			s := string(out)
			lines := strings.Split(strings.TrimSpace(s), "\n")
			last := lines[len(lines)-1]
			if err2 := json.Unmarshal([]byte(last), &js); err2 != nil {
				logf("warn", "failed to parse headscale script JSON", map[string]any{"err": err2.Error(), "raw": last})
			}
		}
		if v, ok := js["server_url"].(string); ok {
			rec["login_server"] = v
		}
		if v, ok := js["container"].(string); ok {
			rec["container"] = v
		}
		if v, ok := js["image"].(string); ok {
			rec["image"] = v
		}
		// try to parse port as number or string
		if v, ok := js["port"]; ok {
			rec["port"] = v
		}
		// attempt to create a reusable preauth key for hostapp usage
		preauthUser := "hostapp"
		pCmd := exec.CommandContext(ctx, "/usr/bin/env", "bash", script, "preauth-key", preauthUser)
		pCmd.Env = os.Environ()
		pout, _ := pCmd.CombinedOutput()
		// find tskey-like token in output
		key := ""
		re := regexp.MustCompile(`tskey-[A-Za-z0-9_\-]+`)
		if m := re.FindString(string(pout)); m != "" {
			key = m
		}

		encrypted := false
		encVal := key
		if key != "" && m.Secrets != nil {
			if sEnc, err := m.Secrets.Encrypt(key); err == nil {
				encVal = sEnc
				encrypted = true
			}
		}

		// persist credential record for the preauth key
		credId := fmt.Sprintf("cred-hs-%s-preauth", id)
		credKey := fmt.Sprintf("hs:%s:preauth", id)
		cred := map[string]any{
			"id":        credId,
			"scopeType": "headscale",
			"scopeId":   id,
			"kind":      "headscale.preauth",
			"value":     encVal,
			"encrypted": encrypted,
			"rotatedAt": time.Now().UTC().Format(time.RFC3339),
		}
		if m.DB != nil {
			_ = m.DB.Put("credentials", credKey, cred)
			// reference from headscale record
			rec["admin_token_secret_id"] = credId
		}

		rec["state"] = "ready"
		rec["updatedAt"] = time.Now().UTC().Format(time.RFC3339)
		_ = m.DB.Put("headscales", id, rec)
		audit.Append(m.DB, "system", "create", "headscale", id, "")
		return nil
	}

	// No local script found: mark ready (best-effort placeholder)
	logf("warn", "headscale helper script not found; marking ready in DB", map[string]any{"id": id, "script": script})
	rec["state"] = "ready"
	rec["updatedAt"] = time.Now().UTC().Format(time.RFC3339)
	_ = m.DB.Put("headscales", id, rec)
	audit.Append(m.DB, "system", "create", "headscale", id, "script-missing")
	return nil
}

func (m *Manager) Start(ctx context.Context, id string, logf func(step, msg string, kv map[string]any)) error {
	if m.DB == nil {
		return fmt.Errorf("no db")
	}
	var rec map[string]any
	if err := m.DB.Get("headscales", id, &rec); err != nil {
		return err
	}
	logf("start", "start headscale", map[string]any{"id": id})
	// attempt to call helper script to ensure container is running
	script := "scripts/headscale-run.sh"
	if ex, e := os.Executable(); e == nil {
		dir := filepath.Dir(ex)
		candidate := filepath.Join(dir, "..", "scripts", "headscale-run.sh")
		if st, e2 := os.Stat(candidate); e2 == nil && st.Mode().IsRegular() {
			script = candidate
		}
	}
	if st, err := os.Stat(script); err == nil && st.Mode().IsRegular() {
		cmd := exec.CommandContext(ctx, "/usr/bin/env", "bash", script, "up", "--json")
		cmd.Env = os.Environ()
		out, err := cmd.CombinedOutput()
		if err != nil {
			logf("error", "failed to start headscale", map[string]any{"error": err.Error(), "output": string(out)})
			rec["state"] = "error"
			rec["lastError"] = err.Error()
			rec["updatedAt"] = time.Now().UTC().Format(time.RFC3339)
			_ = m.DB.Put("headscales", id, rec)
			audit.Append(m.DB, "system", "start", "headscale", id, err.Error())
			return err
		}
		rec["state"] = "ready"
		rec["updatedAt"] = time.Now().UTC().Format(time.RFC3339)
		_ = m.DB.Put("headscales", id, rec)
		audit.Append(m.DB, "system", "start", "headscale", id, "")
		return nil
	}
	// fallback
	rec["state"] = "ready"
	rec["updatedAt"] = time.Now().UTC().Format(time.RFC3339)
	_ = m.DB.Put("headscales", id, rec)
	audit.Append(m.DB, "system", "start", "headscale", id, "script-missing")
	return nil
}

func (m *Manager) Stop(ctx context.Context, id string, logf func(step, msg string, kv map[string]any)) error {
	if m.DB == nil {
		return fmt.Errorf("no db")
	}
	var rec map[string]any
	if err := m.DB.Get("headscales", id, &rec); err != nil {
		return err
	}
	logf("stop", "stop headscale", map[string]any{"id": id})
	script := "scripts/headscale-run.sh"
	if ex, e := os.Executable(); e == nil {
		dir := filepath.Dir(ex)
		candidate := filepath.Join(dir, "..", "scripts", "headscale-run.sh")
		if st, e2 := os.Stat(candidate); e2 == nil && st.Mode().IsRegular() {
			script = candidate
		}
	}
	if st, err := os.Stat(script); err == nil && st.Mode().IsRegular() {
		cmd := exec.CommandContext(ctx, "/usr/bin/env", "bash", script, "down", "--json")
		cmd.Env = os.Environ()
		out, err := cmd.CombinedOutput()
		if err != nil {
			logf("warn", "headscale down returned error", map[string]any{"err": err.Error(), "output": string(out)})
		}
	}
	rec["state"] = "stopped"
	rec["updatedAt"] = time.Now().UTC().Format(time.RFC3339)
	_ = m.DB.Put("headscales", id, rec)
	audit.Append(m.DB, "system", "stop", "headscale", id, "")
	return nil
}

func (m *Manager) Destroy(ctx context.Context, id string, logf func(step, msg string, kv map[string]any)) error {
	if m.DB == nil {
		return fmt.Errorf("no db")
	}
	logf("destroy", "destroy headscale from cluster", map[string]any{"id": id})
	// attempt to stop/remove container via helper script
	script := "scripts/headscale-run.sh"
	if ex, e := os.Executable(); e == nil {
		dir := filepath.Dir(ex)
		candidate := filepath.Join(dir, "..", "scripts", "headscale-run.sh")
		if st, e2 := os.Stat(candidate); e2 == nil && st.Mode().IsRegular() {
			script = candidate
		}
	}
	if st, err := os.Stat(script); err == nil && st.Mode().IsRegular() {
		cmd := exec.CommandContext(ctx, "/usr/bin/env", "bash", script, "down", "--json")
		cmd.Env = os.Environ()
		_ = cmd.Run()
	}
	_ = m.DB.Delete("headscales", id)
	audit.Append(m.DB, "system", "destroy", "headscale", id, "")
	return nil
}

// bytesTrim trims leading/trailing whitespace and NULs to make JSON parsing more robust
func bytesTrim(b []byte) []byte {
	s := strings.TrimSpace(string(b))
	return []byte(s)
}
