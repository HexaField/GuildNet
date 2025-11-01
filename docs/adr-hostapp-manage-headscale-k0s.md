## ADR: HostApp manages Headscale and k0s (DinD)

Status: proposed

Date: 2025-11-01

## Context

- The GuildNet hostapp currently expects external Headscale (Tailscale control/login) servers and k0s clusters to be provisioned and running. Local helper scripts (e.g. `scripts/headscale-run.sh`, `scripts/k0s-node-up.sh`) exist, but hostapp does not manage lifecycle of those services.
- During development we observed frequent misconfiguration and poor DX when headscale containers fail to start (image/config mismatch) and hostapp's tsnet components repeatedly log "connection refused" because the login server at `tailscale.login_server` is not running.
- The codebase contains orchestration hooks and stubs: `internal/orch/handlers.go` already calls bootstrap scripts and persists kubeconfigs; `internal/headscale/manager.go` exists with TODOs. This indicates an intended design where hostapp coordinates these services.

## Decision

HostApp will become the primary lifecycle manager for local Headscale instances and for k0s-in-Docker (DinD) clusters. HostApp will provide APIs, jobs, and UI controls to create, start, stop, destroy, and monitor Headscale instances and k0s clusters. The implementation below describes the work required to meet these requirements.

## Rationale

- Better experience: hostapp can ensure required dependencies (Headscale, kubeconfigs) are available and healthy without asking users to run separate scripts manually.
- Single control plane: users can operate clusters and Headscale instances from the hostapp UI and API, improving discoverability and reducing setup friction.
- Reuse and safety: leveraging existing tested scripts and transitioning to SDK-based orchestration where appropriate balances speed and robustness.

## Consequences

- HostApp will need permissions to manage local Docker (or a remote Docker endpoint) if using Docker-based Headscale and k0s DinD. This requires documentation and clear opt-in.
- Sensitive credentials (preauth keys, kubeconfigs) must be stored securely. HostApp will use `internal/secrets.Manager` and respect `GUILDNET_MASTER_KEY` for encryption; fallback to plaintext only with explicit warnings.
- On macOS and Docker Desktop, port binding behavior can vary; hostapp must choose bind addresses carefully (prefer 127.0.0.1 for local-only services) and persist chosen ports.

## Implementation plan (concrete)

The implementation will deliver the functionality required for hostapp to manage Headscale and k0s DinD clusters. The plan below lists the concrete tasks and expectations; these should be completed as part of the implementation rather than left as separate phases.

- Programmatic lifecycle manager

  - Implement `internal/headscale/manager.go` to run the helper scripts programmatically (non-interactive) where appropriate, and parse their output robustly (prefer machine-parsable output such as JSON from scripts when possible).
  - Persist metadata to the `headscales` DB record (fields: login_server, port, container id/name, image, state, updatedAt).
  - Ensure `internal/orch/handlers.go` job handlers call the manager and persist state transitions with clear error handling and logging.

- Secrets and credentials

  - Store preauth keys and kubeconfigs using `internal/secrets.Manager` and encrypt them when `GUILDNET_MASTER_KEY` is present.
  - Ensure any exported kubeconfigs are emitted deterministically and validated before marking a cluster as ready.

- Replace fragile shell-outs with SDK usage where it increases reliability

  - Use Docker SDK (where available) to create and manage containers deterministically (port mappings, healthchecks, logs) and record container IDs.
  - Provide an option to manage Headscale via k8s manifests/operator when requested, using client-go to apply resources.

- k0s DinD cluster provisioning

  - Harden or reimplement `scripts/k0s-node-up.sh` to reliably produce kubeconfigs and deterministic artifacts when used from hostapp, or reimplement provisioning using the Docker SDK for stronger control.
  - Persist kubeconfigs securely and mark cluster state appropriately when reachable.
  - Provide cluster lifecycle APIs for create/start/stop/destroy and for managing addons (MetalLB, local-path storage).

- Observability, UX and reconciliation

  - Add API endpoints and UI controls (in `ui/`) to create/manage instances and to view logs and status.
  - Add audit logging for management actions and RBAC enforcement for sensitive operations.
  - Implement reconciliation on hostapp startup to compare DB state with actual running resources and repair or mark entries as `error` when mismatches are found.

- Testing and validation
  - Unit tests for `internal/headscale.Manager` (fake DB, fake secrets manager).
  - Integration tests that can run the helper scripts (configurable image tags) and probe the Headscale endpoints (e.g. `http://127.0.0.1:<port>/key?v=1`) to assert basic liveness/response.

## Data shapes

- Headscale record (bucket `headscales`):
  - id, name, login_server, port, container (id/name), image, admin_token_secret_id, state, createdAt, updatedAt
- Cluster record (bucket `clusters`):
  - id, name, state, kubeconfig_secret_id, addons, createdAt, updatedAt

## Testing

- Unit tests for `internal/headscale.Manager` using a fake DB and fake secrets manager.
- Integration test to run `scripts/headscale-run.sh up` (or the SDK-managed equivalent) with configurable image tag and assert that `http://127.0.0.1:<port>/key?v=1` returns non-404.

## Rollback / Migration

- If hostapp changes cause problems, operators can disable automatic management by setting environment flags (documented) or reverting to previous behavior (jobs not scheduled). The DB will retain records; a destroy operation will attempt to remove containers/resources.

## Security considerations

- Store secrets encrypted (when possible). Document that hostapp requires access to the Docker socket to manage containers; running hostapp with such access is an explicit security choice.

## Notes / Next steps

- Implement the manager wiring in `internal/headscale/manager.go` and corresponding handler updates in `internal/orch/handlers.go`.
- Add or extend `scripts/headscale-run.sh` and `scripts/k0s-node-up.sh` to support machine-parsable output where it simplifies robust parsing (JSON flags are recommended).
- Update `API.md`, `architecture.md`, and `DEPLOYMENT.md` with the new operational requirements (Docker access, `GUILDNET_MASTER_KEY`, ports used) and the changed responsibilities of hostapp.

## References

- `internal/orch/handlers.go` (cluster.create and headscale.\* handlers)
- `scripts/headscale-run.sh` and `scripts/k0s-node-up.sh` (helpers used by hostapp)
- `guildnet.config` sample that contains `tailscale.login_server` and `preauth_key`

Decision made-by: maintainers
