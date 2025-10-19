# ADR 0001 — Implementation progress checklist

This checklist accompanies ADR 0001 and the implementation plan (`docs/implementation/0001-multi-device-cluster-implementation.md`). Each item is a single-sentence task. Completed items are checked off.

- [x] Add API types for `FederatedCluster` (previously MultiDeviceCluster), `FederatedService`, and `SiteStatus` under `api/v1alpha1`.
- [x] Add a minimal placement engine (`pkg/placement`) with a happy-path unit test.
- [x] Add a skeleton reconciler for `FederatedService` under `internal/controller/federation`.
- [x] Update `API.md`, `architecture.md`, and `DEPLOYMENT.md` to reference the ADR and implementation plan.
- [x] Run `make build-backend` to validate backend compiles after initial changes.
- [x] Add controller-gen `go:generate` marker to `api/v1alpha1` (CRD generation requires `controller-gen` to run).
- [ ] Run `controller-gen` to generate deepcopy and CRD YAML under `config/crd/` (attempted locally; generator unstable in this environment).
- [ ] Add generated clientsets / deepcopies (`zz_generated.deepcopy.go`) and wire `go:generate` markers where appropriate (pending — CI will run controller-gen; minimal CRD added as a fallback).
- [x] Implement site registry REST handlers (join, leave, heartbeat) in `internal/hostapp/api` (in-memory prototype added).
- [x] Implement a simple agent/collector skeleton under `internal/agent/collector` (heartbeat loop; no image built).
- [x] Add `images/agent/Dockerfile` and `k8s/agent-daemonset.yaml.tmpl` to support deploying the collector as a DaemonSet.
- [x] Wire the reconciler into `cmd/hostapp/main.go` (register scheme and start controllers via manager).
- [x] Implement per-site actuation: reconciler creates/updates per-site Deployments using cluster registry (prototype).
- [x] Implement planner integration: reconciler calls planner and produces per-site Deployments/Services (prototype integrated).
- [ ] Implement per-host proxy config and a proxy controller to push configs to per-site DaemonSets.
- [ ] Add garbage collection logic for per-site resources when services/sites are removed.
- [ ] Add unit tests for placement heuristics covering edge cases (insufficient capacity, zero sites).
- [x] Add reconciler unit tests that exercise per-site actuation with a fake registry/client (integration-style tests added).
- [ ] Add integration tests that simulate two joined clusters and assert per-site resources are created.
- [x] Add UI pages for Site list, FederatedService creation wizard, and service detail endpoints (creation wizard scaffold added at `ui/src/routes/FederatedCreate.tsx`).
- [ ] Update `scripts/join.sh`, `scripts/microk8s-setup.sh`, and `scripts/deploy-tailscale-router.sh` to support site enrollment and kubeconfig emission.
- [ ] Add RBAC rules and security docs for site join and kubeconfig handling, and document revoke/rotation.
- [ ] Add CI targets for codegen, unit tests, and integration tests (e.g., `make gen`, `make test-integration`). Note: a workflow to run controller-gen, tests and UI build was added; CI will be authoritative for generated artifacts.
- [ ] Document operational runbooks for failover, rebalancing thresholds and migration cost settings.
- [ ] Harden controllers: health checks, metrics, tracing, and error handling policies.

If you want, I can proceed next with any checked-off TODO (for example running `controller-gen` and adding generated CRDs) — tell me which item to pick and I'll start it and mark it in the todo list.
