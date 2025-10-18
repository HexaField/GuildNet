# Implementation Plan: Multi-device Federated Cluster

Based on ADR 0001 (Multi-device federated cluster with cross-device service balancing) — detailed implementation plan, file-level work breakdown, tests, commands and rollout timeline.

Date: 2025-10-17

## Summary
This document expands ADR 0001 into a concrete implementation plan. It maps work items to repository locations, provides small contracts for components, lists edge cases and tests, and gives commands and a phased timeline for development and rollout.

## Implementation contract
- Inputs: site kubeconfigs and identity, per-site metrics (CPU, memory, labels, network), user `FederatedService` manifests.
- Outputs: per-site Kubernetes manifests (Deployments, Services, DaemonSets), Host App global endpoint registry / DNS, per-host proxy configuration, and status conditions on CRDs.
- Error modes: unreachable site, insufficient capacity, conflicting names, network partition, unauthenticated join.
- Success criteria: creating a `FederatedService` results in per-site resources and endpoints published by Host App; unit and integration tests pass.

## High-level phases
- Phase 0 — Discovery & metrics (PoC)
- Phase 1 — CRDs + controllers (per-site resource creation + endpoint registry)
- Phase 2 — Placement & rebalancing (scoring, migration policies)
- Phase 3 — Hardening, observability and UI polish

## File-level work and locations
1. API & CRDs
   - Add: `api/v1alpha1/federatedcluster_types.go` (previously multidevicecluster_types.go), `api/v1alpha1/federatedservice_types.go` (previously multideviceservice), `api/v1alpha1/sitestatus_types.go`.
   - Generate CRD YAML under `config/crd/bases/`.
   - Add `go:generate` / controller-gen markers.

2. Codegen & clients
   - Run `controller-gen` to produce deepcopy, CRDs, RBAC markers.
   - Add generated artifacts in `config/` for kustomize.

3. Controllers & operator wiring
   - Add controllers under `internal/controller/`:
   - `federation/reconciler_multideviceservice.go` — main reconciler mapping logical service to per-site manifests (handles FederatedService CRD).
   - `federation/reconciler_federatedcluster.go` (previously reconciler_multidevicecluster.go) — membership tracking.
     - `placement/placement.go` — wrapper around the placement engine.
   - Wire into `cmd/hostapp/main.go` to register scheme and start controllers.

4. Site registration & REST API
   - `internal/hostapp/api/site_handlers.go` — HTTP handlers: join, leave, heartbeat, list sites.
   - Secure endpoints using Headscale/Tailscale pre-auth pattern and require kubeconfig proof.

5. Per-site metrics collector
   - `internal/agent/collector/main.go` — a small agent (DaemonSet) that posts node and pod metrics and heartbeat to Host App.
   - `images/agent/Dockerfile` and `k8s/agent-daemonset.yaml` template.

6. Placement engine
   - `pkg/placement/engine.go`, `pkg/placement/heuristics/*.go`.
   - Unit tests in `pkg/placement/engine_test.go`.

7. Reconcile controller
   - Reconciler duties: call planner, create/update per-site Deployments/Services (named with site suffix), annotate/label for GC, and update Host App endpoint registry.
   - Use remote kube clients created from stored site kubeconfigs.

8. Per-host proxy & config controller
   - `k8s/proxy/daemonset.yaml.tmpl` (Envoy or lightweight proxy), `internal/controller/proxy/proxy_controller.go` to push configs.
   - `images/proxy/` example image and config templates.

9. Global DNS & discovery
   - `internal/hostapp/discovery/dns_registry.go` and REST endpoints to serve endpoint lists per logical service.

10. Scripts & onboarding
   - Update `scripts/join.sh`, `scripts/microk8s-setup.sh`, and `scripts/deploy-tailscale-router.sh` to include registration steps and kubeconfig emission.

11. UI
   - Extend `ui/` with pages: Site List, Service creation wizard, and Service detail with endpoints. Use new Host App API routes.

12. Tests & CI
   - Add `tests/multidevice_*` integration tests (simulate 2 clusters), unit tests for placement and controllers.
   - Add `make gen`, `make test-integration` targets and update CI config to run them.

13. Docs
   - Update `API.md`, `architecture.md`, and `DEPLOYMENT.md` with API examples, diagrams and rollout steps.

## Reconciler design notes
- Reconciler idempotently maps `FederatedService` to per-site manifests.
- Naming convention: `<svcname>-<siteid>-deployment`, `<svcname>-<siteid>-service` to avoid collisions.
- Use saved kubeconfigs and client-go to create remote resources.
- Record conditions in `FederatedService.status` for partial failures.
- Garbage collect per-site resources when service/site removed (GC TTL window configurable).

## Placement engine contract
- Planner(Input: ServiceSpec, []SiteStatus) -> Output: map[siteID]int (replica counts) + ranked candidate list.
- Initial heuristics: spread across N sites, prefer high CPU or low latency, avoid insufficient capacity.
- Rebalancing: only migrate when benefit > migration cost threshold.

## Per-host proxy (v1)
- Start with templated Envoy or a simple Go proxy per-site deployed as a DaemonSet.
- Proxy config lists healthy endpoints for logical services and performs retries and basic LB.
- Proxy controller updates configs on Host App endpoint registry changes.

## Security
- Site join requires Headscale/Tailscale pre-auth + kubeconfig proof.
- Host App endpoints require authentication; collector posts use mTLS or short-lived tokens.
- Remote kubeconfigs should have narrowly-scoped privileges; document rotation and revocation.

## Tests & validation
1. Unit tests
   - `pkg/placement` and controller unit tests using fake client-go clients.
2. Integration tests
   - Two containerized clusters (kind or microk8s) joined as two sites, create `FederatedService`, assert per-site Deployments.
3. E2E smoke
   - Manual/test harness verifying failover and rebalancing after simulated site down.

## Sample commands
Install codegen tools (one-time):
```bash
go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.12.0
go install sigs.k8s.io/kubebuilder@v3.6.0
```
Generate CRDs:
```bash
controller-gen crd paths=./api/v1alpha1/... output:crd:artifacts:config=config/crd
```
Build backend:
```bash
make build-backend
```
Run tests:
```bash
make test
# targeted
go test ./pkg/placement -race -v
```
Run hostapp locally:
```bash
make run
```

## Acceptance criteria
- CRDs install cleanly via `kubectl apply -k config/crd`.
- Creating `FederatedService` leads to per-site Deployments/Services in joined clusters.
- Host App endpoint discovery API returns per-site endpoints.
- Unit & integration tests pass in CI.

## Edge cases and mitigations
- Unreachable site: mark NotReady and avoid scheduling new replicas there.
- Conflicting names: include siteID suffix in resource names.
- Overlapping Pod CIDRs: prefer L7 proxy routing; document L3 requirements and tsnet subnet router option.
- Insufficient capacity: produce partial plan and mark CRD status `Degraded` with reason.
- Compromised kubeconfig: document revoke and require short-lived join proofs.

## CI & quality gates
- Build: `make build-backend` → PASS
- Lint: `make lint` → PASS
- Tests: `make test` (unit + integration subset) → PASS

## Minimal first sprint (recommended tasks)
1. Add CRD types and controller-gen scaffolding.
2. Implement site registry + simple collector (heartbeat + node capacity reporting).
3. Implement a minimal placement engine and a reconciler that creates per-site Deployments (1 replica per site).
4. Add unit tests for placement and an integration test that simulates two sites.

## Timeline (suggested)
- Sprint 0 (1 week): CRDs, codegen, site registry and collector PoC.
- Sprint 1 (1–2 weeks): placement engine, basic reconciler, unit tests.
- Sprint 2 (1–2 weeks): per-host proxy, integration tests across 2 sites.
- Sprint 3 (1–2 weeks): UI, docs, monitoring, rollouts.

## Next steps (I can do for you)
I can scaffold the CRD types and controller skeleton now, run controller-gen to output CRDs, and add a minimal unit test for `pkg/placement`. Say the word and I'll implement the first sprint artifacts into the repo.

---

References:
- ADR 0001: `docs/adr/0001-multi-device-cluster.md`
- Repository locations referenced: `cmd/hostapp`, `api/v1alpha1`, `internal/`, `pkg/placement`, `k8s/`, `images/`.
