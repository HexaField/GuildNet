## ADR: HostApp manages Headscale and k0s (DinD) clusters

Status: proposed

Date: 2025-11-01

Context
-------
- The GuildNet hostapp currently expects external Headscale (Tailscale control/login) servers and k0s clusters to be provisioned and running. Local helper scripts (e.g. `scripts/headscale-run.sh`, `scripts/k0s-node-up.sh`) exist to assist developers, but hostapp does not manage lifecycle of those services.
- During development we observed frequent misconfiguration and poor DX when headscale containers fail to start (image/config mismatch) and hostapp's tsnet components repeatedly log "connection refused" because the login server at `tailscale.login_server` is not running.
- The codebase contains orchestration hooks and stubs: `internal/orch/handlers.go` already calls bootstrap scripts and persists kubeconfigs; `internal/headscale/manager.go` exists with TODOs. This indicates an intended design where hostapp coordinates these services.

Decision
--------
HostApp will become the primary lifecycle manager for local Headscale instances and for developer k0s-in-Docker (DinD) clusters. HostApp will provide APIs, jobs, and UI controls to create, start, stop, destroy, and monitor Headscale instances and k0s clusters. The change will be implemented incrementally:

- MVP (shell-out): implement managers that invoke existing scripts programmatically (non-interactive), parse their outputs, and persist metadata and credentials in the local DB/secrets store.
- Medium-term: replace fragile shell-outs with direct orchestration via Docker SDK for local Docker-backed instances and client-go for k8s-backed Headscale deployments (operator/CRDs) when requested.
- Long-term: add reconciliation on startup to bring DB state in sync with running containers/resources, add log streaming, health checks, RBAC, and extend the UI.

Rationale
---------
- Better developer experience: hostapp can ensure required dependencies (Headscale, kubeconfigs) are available and healthy without asking users to run separate scripts manually.
- Single control plane: users can operate clusters and Headscale instances from the hostapp UI and API, improving discoverability and reducing setup friction.
- Reuse and safety: initial shell-out approach leverages existing tested scripts and avoids a large up-front rewrite. Later transitions to SDKs improve robustness.

Consequences
------------
- HostApp will need permissions to manage local Docker (or a remote Docker endpoint) if using Docker-based Headscale and k0s DinD. This requires documentation and clear opt-in.
- Sensitive credentials (preauth keys, kubeconfigs) must be stored securely. HostApp will use `internal/secrets.Manager` and respect `GUILDNET_MASTER_KEY` for encryption; fallback to plaintext only with explicit warnings.
- On macOS and Docker Desktop, port binding behavior can vary; hostapp must choose bind addresses carefully (prefer 127.0.0.1 for local-only services) and persist chosen ports.

Alternatives considered
---------------------
1) Leave responsibilities external (no change)
   - Pros: minimal code changes.
   - Cons: poor DX, repeated support burden, user error.

2) Immediate full SDK implementation (Docker + client-go) for v1
   - Pros: more robust and production-quality.
   - Cons: much larger implementation surface, more risk and time.

We choose the staged approach: MVP shell-out, then SDK replacement.

Implementation Plan (concrete)
-----------------------------
Step 1 — MVP (quick, low-risk)
- Edit `internal/headscale/manager.go` to call `scripts/headscale-run.sh up|down|status` programmatically (non-interactive). Parse the script output for server URL (the helper prints "Server URL:") and persist to the `headscales` DB record (fields: login_server, port, container, image, state, updatedAt).
- Edit `internal/orch/handlers.go` (already wired) to ensure job handlers call the manager and properly log progress. Harden error handling and persist state transitions.
- Persist preauth keys and kubeconfigs in `internal/secrets.Manager` (encrypted when GUILDNET_MASTER_KEY is present).
- Add small integration test that runs headscale via script and probes `/key` endpoint.

Step 2 — Robust manager
- Replace shell-outs with Docker client calls for local developer flow. Create containers with deterministic port mapping and healthchecks. Record container IDs and attach log streaming via the Docker API.
- Add a k8s client mode that applies manifests/CRDs for Headscale operator when managing Headscale in a k8s cluster.

Step 3 — k0s DinD cluster improvements
- Harden `scripts/k0s-node-up.sh` or reimplement provisioning using Docker SDK so kubeconfig is emitted deterministically and safely. Persist the kubeconfig (encrypted) and mark cluster as `ready` when kubeconfig is reachable.
- Add cluster lifecycle APIs for scaling, addons (MetalLB/localpath), and deletion.

Step 4 — UX, security, and reconciliation
- Add UI pages/buttons to the `ui/` to create/manage instances and show logs and status.
- Add audit logging (the code already uses `audit.Append`) and RBAC for management actions.
- Implement state reconciliation on hostapp start: reconcile DB entries with actual resources and repair or mark as `error`.

Data shapes
-----------
- Headscale record (bucket `headscales`):
  - id, name, login_server, port, container (id/name), image, admin_token_secret_id, state, createdAt, updatedAt
- Cluster record (bucket `clusters`):
  - id, name, state, kubeconfig_secret_id, addons, createdAt, updatedAt

Testing
-------
- Unit tests for `internal/headscale.Manager` using a fake DB and fake secrets manager.
- Integration test (dev-only) to run `scripts/headscale-run.sh up` with configurable image tag and assert that `http://127.0.0.1:<port>/key?v=1` returns non-404.

Rollback / Migration
--------------------
- If hostapp changes cause problems, operators can disable automatic management by setting environment flags (documented) or reverting to the previous behavior (jobs not scheduled). The DB will retain records; a destroy operation will attempt to remove containers/resources.

Security considerations
-----------------------
- Store secrets encrypted (when possible). Document that hostapp requires access to the Docker socket to manage containers; running hostapp with such access is an explicit security choice.

Notes / Next steps
------------------
- Implement MVP wiring in `internal/headscale/manager.go` and add tests. Add a `--json` or machine-parsable output mode to `scripts/headscale-run.sh` for robust parsing (optional but recommended).
- Update `API.md`, `architecture.md`, and `DEPLOYMENT.md` with summary of changes and the new operational requirements (Docker access, GUILDNET_MASTER_KEY, ports used).

References
----------
- `internal/orch/handlers.go` (cluster.create and headscale.* handlers)
- `scripts/headscale-run.sh` (helper used by the MVP)
- `guildnet.config` sample that contains `tailscale.login_server` and `preauth_key`

Decision made-by: maintainers
