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
	"github.com/your/module/internal/settings"
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

	// If the docker socket exists, try a pragmatic docker CLI run. This
	// is intentionally simple: create a container mapping 127.0.0.1:8082
	// to the headscale port. If docker is not available or the command
	// fails, continue to the script fallback below.
	if _, err := os.Stat("/var/run/docker.sock"); err == nil {
		img := "ghcr.io/juanfont/headscale:0.27.0"
		name := "guildnet-headscale-" + id
		args := []string{"run", "-d", "--rm", "--name", name, "-p", "127.0.0.1:8082:8082", "-e", "HEADSCALE_BIND_HOST=127.0.0.1", "-e", "HEADSCALE_PORT=8082", "-e", "HEADSCALE_SERVER_URL=http://127.0.0.1:8082", img}
		if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err == nil {
			cid := strings.TrimSpace(string(out))
			if cid != "" {
				rec["container"] = cid
				rec["image"] = img
				rec["port"] = 8082
				rec["login_server"] = "http://127.0.0.1:8082"
			}
		}
	}

	if st, err := os.Stat(script); err == nil && st.Mode().IsRegular() {
		cmd := exec.CommandContext(ctx, "/usr/bin/env", "bash", script, "up", "--json")
		// Prefer binding Headscale to loopback for host-local development so
		// that tsnet and connectors can reach the control plane reliably.
		env := os.Environ()
		env = append(env, "HEADSCALE_BIND_HOST=127.0.0.1")
		env = append(env, "HEADSCALE_PORT=8082")
		env = append(env, "HEADSCALE_SERVER_URL=http://127.0.0.1:8082")
		cmd.Env = env
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
		pCmd := exec.CommandContext(ctx, "/usr/bin/env", "bash", script, "preauth-key", preauthUser, "--json")
		// reuse the same HEADSCALE_* env so preauth-key targets the chosen server
		pCmd.Env = env
		pout, _ := pCmd.CombinedOutput()
		// Parse JSON output which should contain both the raw hex and the tskey form
		keyHex := ""
		keyTS := ""
		var pk struct {
			Hex   string `json:"hex"`
			Tskey string `json:"tskey"`
		}
		if err := json.Unmarshal(bytesTrim(pout), &pk); err == nil {
			keyHex = strings.TrimSpace(pk.Hex)
			keyTS = strings.TrimSpace(pk.Tskey)
		} else {
			// Fallback: try to extract a tskey-like token or hex from plain output
			s := string(pout)
			reTs := regexp.MustCompile(`tskey-[A-Za-z0-9_\-]+`)
			if m := reTs.FindString(s); m != "" {
				keyTS = m
			}
			// try hex-like token
			reHex := regexp.MustCompile(`\b[0-9a-fA-F]{32,}\b`)
			if h := reHex.FindString(s); h != "" {
				keyHex = h
			}
		}

		encrypted := false
		// Prefer storing the raw hex in credentials (Headscale expects raw hex)
		storeVal := keyHex
		if storeVal == "" {
			// Fallback: if no hex available, attempt to derive from tskey
			storeVal = keyTS
		}
		encVal := storeVal
		if storeVal != "" && m.Secrets != nil {
			if sEnc, err := m.Secrets.Encrypt(storeVal); err == nil {
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

		// Also, update global Tailscale settings so connectors and agents
		// default to this Headscale login server and preauth key. Be careful
		// not to overwrite existing settings with empty or stale values — the
		// manager may be invoked multiple times while the helper script is
		// still converging. Persist only when we have a canonical preauth
		// token (tskey form) or when no LoginServer exists yet.
		if m.DB != nil {
			// attempt to read back the stored credential (may be encrypted)
			var stored map[string]any
			val := ""
			if err := m.DB.Get("credentials", credKey, &stored); err == nil {
				if v, ok := stored["value"].(string); ok {
					val = v
				}
				if enc, ok := stored["encrypted"].(bool); ok && enc && val != "" && m.Secrets != nil {
					if dec, derr := m.Secrets.Decrypt(val); derr == nil {
						val = dec
					}
				}
			}

			mgr := settings.Manager{DB: m.DB}
			// read current settings so we don't clobber them
			var cur settings.Tailscale
			_ = mgr.GetTailscale(&cur)

			// Build candidate settings but only set fields we can confidently
			// provide. Prefer the explicit tskey returned by the helper script.
			cand := settings.Tailscale{}
			// Only set LoginServer if there is no existing one — avoid
			// replacing a working value with a transient one from the helper.
			if strings.TrimSpace(cur.LoginServer) == "" {
				cand.LoginServer = fmt.Sprint(rec["login_server"])
			}

			// Prefer tskey from script output; fall back to stored value only
			// if it already appears to be a tskey. We will not persist raw hex
			// here to avoid confusion — tsnet normalization will handle hex if
			// necessary elsewhere.
			if keyTS != "" {
				cand.PreauthKey = keyTS
			} else if val != "" && strings.HasPrefix(val, "tskey-") {
				cand.PreauthKey = val
			}

			// Persist only if we have at least a preauth key (preferred) or
			// we are filling an empty LoginServer. This avoids accidental
			// clearing or overwriting with incomplete data.
			if (cand.PreauthKey != "") || (cand.LoginServer != "") {
				// merge with existing settings so we only change provided fields
				out := settings.Tailscale{}
				out = cur
				if cand.LoginServer != "" {
					out.LoginServer = cand.LoginServer
				}
				if cand.PreauthKey != "" {
					out.PreauthKey = cand.PreauthKey
				}
				_ = mgr.PutTailscale(out)
			}
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
