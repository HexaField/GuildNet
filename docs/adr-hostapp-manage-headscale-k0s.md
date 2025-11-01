## ADR: HostApp manages Headscale and k0s (DinD)

- Implement a real Headscale Manager (replace stubs in `internal/headscale/manager.go`):

  - support create/start/stop/destroy using either Docker SDK or k8s client-go
  - persist login_server, port, container id/name, image, state, updatedAt in `headscales` DB
  - surface machine-parsable results (JSON) and stable logs for UI/jobs

- Harden orchestration handlers (`internal/orch/handlers.go`):

  - call manager APIs, surface and persist errors, and stop swallowing failures
  - record audit events and state transitions consistently

- Make scripts machine-friendly and deterministic:

  - add `--json` (or similar) output to `scripts/headscale-run.sh` and `scripts/k0s-node-up.sh`
  - ensure kubeconfig path is deterministic and validated before marking cluster ready

- Secrets and kubeconfigs:

  - persist preauth keys and kubeconfigs via `internal/secrets.Manager` and honor `GUILDNET_MASTER_KEY` encryption
  - add deterministic credential IDs and validation before marking cluster `ready`

- Prefer SDKs over fragile shell-outs where low-risk:

  - implement Docker SDK usage for container lifecycle (port mapping, healthchecks, container id)
  - optionally support k8s/operator path via client-go when user requests

- Reconciliation & observability:

  - implement reconciliation on hostapp startup (DB vs actual resources) and mark/repair mismatches
  - add logs, healthchecks, and a small status API for each managed resource

- Tests and CI:

  - unit tests for headscale.Manager (fake DB, fake secrets manager)
  - small integration test that runs headscale/k0s helpers (configurable images) and probes endpoints

- Docs & deployment notes:
  - update `API.md`, `architecture.md`, and `DEPLOYMENT.md` with requirements: Docker access, `GUILDNET_MASTER_KEY`, ports and bind defaults (prefer 127.0.0.1)

Minimum acceptance criteria

- Manager methods perform real lifecycle ops (or reliably call scripts that emit parseable JSON).
- Secrets stored encrypted when master key provided.
- Handlers persist state and errors; UI/jobs can show logs and final status.

Next steps (practical, small wins)

- Add `--json` flag to `scripts/headscale-run.sh` and `scripts/k0s-node-up.sh` to emit chosen ports/container ids/kubeconfig location.
- Implement Manager.Create using Docker SDK to start Headscale container and persist container id + server URL.
- Update `internal/orch/handlers.go` to check manager errors and persist them to DB.

Decision made-by: maintainers
