package orch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/your/module/internal/cluster"
	"github.com/your/module/internal/headscale"
	"github.com/your/module/internal/jobs"
	"github.com/your/module/internal/localdb"
	"github.com/your/module/internal/secrets"
)

// Deps carries minimal dependencies for orchestration handlers.
type Deps struct {
	DB      *localdb.DB
	Secrets *secrets.Manager
}

// tail2k returns the last up to 2000 bytes of the provided string.
func tail2k(s string) string {
	if len(s) <= 2000 {
		return s
	}
	return s[len(s)-2000:]
}

// HandlerFor returns a jobs handler function for a given kind.
func HandlerFor(kind string, deps Deps) func(ctx context.Context, j *jobs.Record, logf func(step, msg string, kv map[string]any)) {
	switch kind {
	case "headscale.create":
		return func(ctx context.Context, j *jobs.Record, logf func(step, msg string, kv map[string]any)) {
			var spec map[string]any
			_ = json.Unmarshal([]byte(j.SpecJSON), &spec)
			id := fmt.Sprint(spec["id"])
			if id == "" {
				return
			}
			mgr := headscale.New(deps.DB, deps.Secrets)
			_ = mgr.Create(ctx, id, logf)
			j.Progress = 1
		}
	case "headscale.start":
		return func(ctx context.Context, j *jobs.Record, logf func(step, msg string, kv map[string]any)) {
			var spec map[string]any
			_ = json.Unmarshal([]byte(j.SpecJSON), &spec)
			id := fmt.Sprint(spec["id"])
			if id == "" {
				return
			}
			mgr := headscale.New(deps.DB, deps.Secrets)
			_ = mgr.Start(ctx, id, logf)
			j.Progress = 1
		}
	case "headscale.stop":
		return func(ctx context.Context, j *jobs.Record, logf func(step, msg string, kv map[string]any)) {
			var spec map[string]any
			_ = json.Unmarshal([]byte(j.SpecJSON), &spec)
			id := fmt.Sprint(spec["id"])
			if id == "" {
				return
			}
			mgr := headscale.New(deps.DB, deps.Secrets)
			_ = mgr.Stop(ctx, id, logf)
			j.Progress = 1
		}
	case "headscale.destroy":
		return func(ctx context.Context, j *jobs.Record, logf func(step, msg string, kv map[string]any)) {
			var spec map[string]any
			_ = json.Unmarshal([]byte(j.SpecJSON), &spec)
			id := fmt.Sprint(spec["id"])
			if id == "" {
				return
			}
			mgr := headscale.New(deps.DB, deps.Secrets)
			_ = mgr.Destroy(ctx, id, logf)
			j.Progress = 1
		}
	case "cluster.create":
		return func(ctx context.Context, j *jobs.Record, logf func(step, msg string, kv map[string]any)) {
			var spec map[string]any
			_ = json.Unmarshal([]byte(j.SpecJSON), &spec)
			tmpId := fmt.Sprint(spec["id"])
			name := fmt.Sprint(spec["name"])
			// Optional addons map: { metallb: bool, localpath: bool }
			addons := map[string]bool{"metallb": true, "localpath": true}
			if raw, ok := spec["addons"]; ok {
				if m, ok2 := raw.(map[string]any); ok2 {
					if v, ok3 := m["metallb"]; ok3 {
						addons["metallb"] = strings.EqualFold(fmt.Sprint(v), "true") || fmt.Sprint(v) == "1"
					}
					if v, ok3 := m["localpath"]; ok3 {
						addons["localpath"] = strings.EqualFold(fmt.Sprint(v), "true") || fmt.Sprint(v) == "1"
					}
				}
			}
			if tmpId == "" {
				return
			}
			logf("create", "provisioning local k0s cluster (docker)", map[string]any{"id": tmpId, "name": name})

			// 1) Attempt to launch a local k0s-in-Docker node via scripts/k0s-node-up.sh
			// Locate script path relative to cwd or to executable dir
			script := "scripts/k0s-node-up.sh"
			if _, err := os.Stat(script); err != nil {
				// try relative to executable dir
				if ex, e := os.Executable(); e == nil {
					dir := filepath.Dir(ex)
					candidate := filepath.Join(dir, "..", "scripts", "k0s-node-up.sh")
					if st, e2 := os.Stat(candidate); e2 == nil && st.Mode().IsRegular() {
						script = candidate
					}
				}
			}
			if st, err := os.Stat(script); err == nil && st.Mode().IsRegular() {
				cmd := exec.CommandContext(ctx, "/usr/bin/env", "bash", script)
				// Inherit minimal environment; allow caller to pass through TS_AUTHKEY etc.
				cmd.Env = os.Environ()
				// Ensure non-interactive; attach no stdin
				out, err := cmd.CombinedOutput()
				if err != nil {
					logf("warn", "k0s bootstrap script returned non-zero", map[string]any{"error": err.Error()})
				}
				// Truncate very large output in logs
				tail := string(out)
				if len(tail) > 2000 {
					tail = tail[len(tail)-2000:]
				}
				if strings.TrimSpace(tail) != "" {
					logf("info", "k0s bootstrap output (tail)", map[string]any{"tail": tail})
				}
			} else {
				logf("warn", "k0s bootstrap script not found; skipping automatic provisioning", map[string]any{"script": script})
			}

			// 2) Read kubeconfig from default path and attach to cluster record
			kcPath := os.Getenv("GN_KUBECONFIG")
			if strings.TrimSpace(kcPath) == "" {
				home, _ := os.UserHomeDir()
				kcPath = filepath.Join(home, ".guildnet", "kubeconfig")
			}
			data, err := os.ReadFile(kcPath)
			if err != nil || len(data) == 0 {
				logf("warn", "kubeconfig not found after bootstrap attempt", map[string]any{"path": kcPath})
				// Leave the record in creating state; UI can attach later
				if deps.DB != nil {
					var rec map[string]any
					if err := deps.DB.Get("clusters", tmpId, &rec); err == nil {
						rec["state"] = "creating"
						rec["updatedAt"] = time.Now().UTC().Format(time.RFC3339)
						_ = deps.DB.Put("clusters", tmpId, rec)
					}
				}
				j.Progress = 1
				return
			}

			kc := string(data)
			// Compute deterministic id to normalize the record across devices
			detID, derr := cluster.DeterministicIDFromKubeconfig(kc)
			if derr != nil || strings.TrimSpace(detID) == "" {
				detID = tmpId
			}
			// Persist kubeconfig under credentials (encrypt when possible)
			enc := kc
			encrypted := false
			if deps.Secrets != nil {
				if v, e := deps.Secrets.Encrypt(kc); e == nil {
					enc = v
					encrypted = true
				}
			}
			cred := map[string]any{
				"id":        fmt.Sprintf("cred-%s", detID),
				"scopeType": "cluster",
				"scopeId":   detID,
				"kind":      "cluster.kubeconfig",
				"value":     enc,
				"encrypted": encrypted,
				"rotatedAt": time.Now().UTC().Format(time.RFC3339),
			}
			if deps.DB != nil {
				_ = deps.DB.Put("credentials", fmt.Sprintf("cl:%s:kubeconfig", detID), cred)
				// Reconcile cluster record id if it changed
				var rec map[string]any
				if err := deps.DB.Get("clusters", tmpId, &rec); err == nil {
					rec["id"] = detID
					if name != "" {
						rec["name"] = name
					}
					rec["state"] = "ready"
					rec["updatedAt"] = time.Now().UTC().Format(time.RFC3339)
					// Write under deterministic id and delete temporary id when different
					_ = deps.DB.Put("clusters", detID, rec)
					if detID != tmpId {
						_ = deps.DB.Delete("clusters", tmpId)
					}
				}
			}

			// 3) Install requested addons (best-effort, idempotent scripts)
			// Ensure kubectl targets the new cluster by setting KUBECONFIG
			envBase := os.Environ()
			envWithKC := append(envBase, fmt.Sprintf("KUBECONFIG=%s", kcPath))
			if addons["localpath"] {
				logf("addon", "ensuring local-path-provisioner (default StorageClass)", map[string]any{"cluster": detID})
				cmd := exec.CommandContext(ctx, "/usr/bin/env", "bash", "scripts/install-local-path-provisioner.sh")
				cmd.Env = envWithKC
				if out, err := cmd.CombinedOutput(); err != nil {
					logf("warn", "local-path-provisioner install returned non-zero", map[string]any{"error": err.Error()})
					if s := string(out); strings.TrimSpace(s) != "" {
						logf("info", "local-path install output", map[string]any{"tail": tail2k(s)})
					}
				} else {
					if s := string(out); strings.TrimSpace(s) != "" {
						logf("info", "local-path install output", map[string]any{"tail": tail2k(s)})
					}
				}
			}
			if addons["metallb"] {
				logf("addon", "deploying MetalLB (L2 mode)", map[string]any{"cluster": detID})
				cmd := exec.CommandContext(ctx, "/usr/bin/env", "bash", "scripts/deploy-metallb.sh")
				cmd.Env = envWithKC
				if out, err := cmd.CombinedOutput(); err != nil {
					logf("warn", "metallb deploy returned non-zero", map[string]any{"error": err.Error()})
					if s := string(out); strings.TrimSpace(s) != "" {
						logf("info", "metallb deploy output", map[string]any{"tail": tail2k(s)})
					}
				} else {
					if s := string(out); strings.TrimSpace(s) != "" {
						logf("info", "metallb deploy output", map[string]any{"tail": tail2k(s)})
					}
				}
			}

			j.Progress = 1
		}
	case "cluster.scale", "cluster.upgrade":
		return func(ctx context.Context, j *jobs.Record, logf func(step, msg string, kv map[string]any)) {
			var spec map[string]any
			_ = json.Unmarshal([]byte(j.SpecJSON), &spec)
			id := fmt.Sprint(spec["id"])
			if id == "" {
				return
			}
			action := "scale"
			if kind == "cluster.upgrade" {
				action = "upgrade"
			}
			logf("op", action+" cluster", map[string]any{"id": id})
			if deps.DB != nil {
				var rec map[string]any
				if err := deps.DB.Get("clusters", id, &rec); err == nil {
					rec["updatedAt"] = time.Now().UTC().Format(time.RFC3339)
					_ = deps.DB.Put("clusters", id, rec)
				}
			}
			j.Progress = 1
		}
	case "cluster.destroy":
		return func(ctx context.Context, j *jobs.Record, logf func(step, msg string, kv map[string]any)) {
			var spec map[string]any
			_ = json.Unmarshal([]byte(j.SpecJSON), &spec)
			id := fmt.Sprint(spec["id"])
			if id == "" {
				return
			}
			logf("op", "destroy cluster", map[string]any{"id": id})
			if deps.DB != nil {
				_ = deps.DB.Delete("clusters", id)
			}
			j.Progress = 1
		}
	}
	// default no-op handler
	return func(ctx context.Context, j *jobs.Record, logf func(step, msg string, kv map[string]any)) {
		logf("noop", "unhandled job kind", map[string]any{"kind": kind})
		j.Progress = 1
	}
}
