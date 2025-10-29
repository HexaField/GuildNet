# GuildNet k0s-in-Docker Implementation Plan

Status: In progress • Date: 2025-10-30 • Scope: Make Docker-only the default runtime (k0s + Tailscale + DinD), preserving existing API/UX and multi-device federation

This document operationalizes the ADR “Fully Containerized GuildNet Architecture with Cross-Device Federation over Tailscale” into concrete, verifiable engineering tasks. It keeps Host App API and existing flows stable while making containerized k0s the default path.

## Guardrails and non-goals

- Production-first defaults, no special dev/local modes. Everything works out-of-the-box.
- Preserve current APIs, deterministic cluster IDs, bootstrap, per‑cluster tsnet connectors, and DB flows.
- Do not break existing helpers during transition (MicroK8s remains available, marked legacy later).

Acceptance
- Existing Make targets and flows keep working while the new path is added and becomes the default.

## Assumptions

- One Docker host per device runs:
  - k0s (controller+worker) in a privileged container with persisted state.
  - Tailscale container for tailnet overlay (Headscale-backed) advertising Pod/Service CIDRs.
  - DinD (docker:dind) for image builds and push to in-cluster registry.
- Kubernetes pods run on k0s/containerd; DinD is used only for builds and optional containerized workloads.
- No host ports: all inter-device and exposure paths run over Tailscale.

---

## Progress tracker (rolling)

Done
- Phase 1–2: Containerized k0s node stack with kubeconfig emission and dynamic API port selection. Scripts added: `k0s-node-up.sh`, `k0s-node-down.sh`, `attach-local-k0s.sh`, `verify-k0s.sh`. DinD enabled by default; image pipeline smoke implemented.
- Phase 11 (partial): E2E hardened for single-node/local-only API. Federation verifier skips remote perspective when unreachable and relaxes placement when both workspaces land on one node. Build/Lint/Tests: PASS.
- Docs: `DEPLOYMENT.md` and `architecture.md` updated with Docker-only flow and e2e behavior notes.
 - Phase 6: Operator deployed on k0s and verified. CRDs present (`deviceparticipants`, `workspaces`, etc.); DeviceParticipant CRs are created in `guildnet-system` and workspaces reconcile to Running.
 - Phase 4 (partial): DinD TLS option implemented and exposed on localhost (2375/2376) with helper env at `~/.guildnet/dind-env.sh`. Added `scripts/dind-registry-push.sh` and `make dind-image-push` for registry pushes.

In progress
- Phase 3: Tailscale container is started automatically when `TS_AUTHKEY` is set; optional `TS_SERVE_KUBEAPI=1` will configure `tailscale serve tcp` to expose the kube-API over the tailnet. SAN coverage for tailnet IP/MagicDNS is pending.
- Phase 5/9/10: Make defaults + bootstrap polish + docs alignment. Scripts and targets exist; final defaultization and README/API polish underway.
 - Phase 7/8: Storage persistence validation (PVCs survive restarts) and proxy/ingress parity checks on k0s.

Planned next
- Phase 3: Validate tailnet kube-API access (serve + SANs) across devices and document.
- Phase 4: Optional in-cluster registry path docs and Makefile wiring (current push helper covers external registries; local import remains the default fast path).
- Phase 7–8: Validate storage persistence and proxy/ingress parity on k0s.
- Phase 12: Reset safeguards (avoid deleting k0s volumes unless explicitly confirmed).

## Phase 0 — Compatibility (no regressions)

Tasks
- Keep MicroK8s helpers working during transition.
- No behavior changes to Host App HTTP API, reverse proxy priority, or DB/CRD semantics.

Acceptance
- Health endpoints and e2e checks pass with current flow while new path is introduced.

## Phase 1 — Containerized node stack design

Deliverables
- Node composition (per device):
  - k0s node container (controller+worker)
  - tailscale container (overlay, routes)
  - docker:dind container (builds)
  - optional wrapper “node supervisor” script to orchestrate startup/shutdown and kubeconfig emission
- Volumes: persist k0s state, containerd, kubelet, DinD state, and a shared path for emitted kubeconfig.
- Networking: all traffic over tailnet; advertise cluster CIDRs.

Acceptance
- Architecture diagram (high-level) and minimal compose/shell orchestration sketch.

## Phase 2 — k0s in Docker: image, init, and kubeconfig emission

Tasks
- Use upstream k0s image or a thin custom image with k0s CLI.
- On first run: `k0s install controller --single` into a mounted state path; then `k0s start`.
- Emit kubeconfig to a shared mount (e.g., /state/kubeconfig) and copy to `~/.guildnet/kubeconfig`.
- Env overrides: `K0S_POD_CIDR` (default 10.244.0.0/16), `K0S_SVC_CIDR` (default 10.96.0.0/12).
- Ensure kube-apiserver cert SAN includes the device’s Tailscale IP or MagicDNS name.

Scripts (proposed)
- `scripts/k0s-node-up.sh` – bring up tailscale + k0s + dind; wait for `/readyz`; write kubeconfig to `$(GN_KUBECONFIG)`.
- `scripts/k0s-node-down.sh` – stop containers (non-destructive by default).

Acceptance
- `kubectl --kubeconfig ~/.guildnet/kubeconfig get nodes` shows a Ready node.

## Phase 3 — Tailscale integration (control + data plane)

Tasks
- Add tailscale container with `/dev/net/tun`, privileged, and envs:
  - `TS_LOGIN_SERVER`, `TS_AUTHKEY`, `TS_ROUTES="10.244.0.0/16,10.96.0.0/12"`, `TS_HOSTNAME`.
- Ensure k0s API TLS SAN covers the tailscale address/MagicDNS.
- Validate inter-device: access device B kube-API from device A over tailnet.

Acceptance
- `make verify-federation-e2e` passes using containerized nodes only; `make headscale-approve-routes` works unchanged.

Notes (current status)
- The k0s node-up script starts a `guildnet-tailscale` container when `TS_AUTHKEY` is provided and advertises `$K0S_POD_CIDR,$K0S_SVC_CIDR` by default. Set `TS_SERVE_KUBEAPI=1` to automatically configure `tailscale serve tcp` to forward the local kube-API port over the tailnet. Adding SANs for the tailnet address to the kube-apiserver cert is tracked separately.

## Phase 4 — DinD builds and in-cluster registry

Tasks
- Add docker:dind with TLS on 2376; client access via `DOCKER_HOST=tcp://dind:2376` and mounted client certs.
- Keep current `k8s-setup-registry-secret` and push images to in-cluster registry DNS.
- Smoke: build tiny image in DinD, push to registry, deploy Workspace, open via Host App proxy.

Acceptance
- Build→push→deploy loop works fully in-container; no host Docker dependency.

## Phase 5 — Makefile and scripts (new default path)

Tasks
- Add targets:
  - `node-up` → `scripts/k0s-node-up.sh`
  - `node-down` → `scripts/k0s-node-down.sh`
  - `deploy-k0s-node` → tailscale up + k0s up + kubeconfig emit
  - `attach-local-node` → POST `/bootstrap` with emitted kubeconfig
- Update `setup-all` to prefer Docker+k0s by default; keep MicroK8s on explicit opt-in.

Acceptance
- `make setup-all` provisions the containerized node and completes existing e2e checks.

## Phase 6 — Operator, CRDs, RBAC on k0s

Tasks
- Apply CRDs (`make gen`, `make crd-apply`) and deploy the operator on k0s.
- Validate RBAC manifests are accepted; controller-runtime leader election remains functional.
- Confirm `DeviceParticipant` CRD upsert and ConfigMap mirroring (`guildnet-system/published-<id>`).

Acceptance
- Operator reconciles `Workspace` CRs into Deployments/Services on k0s as it does on MicroK8s.

## Phase 7 — Storage and persistence

Tasks
- Default: hostPath-based persistence via k0s volumes (path-provisioner style).
- Persist: `/var/lib/k0s`, `/var/lib/containerd`, `/var/lib/kubelet`, and registry data.
- Follow-ups: optional OpenEBS-LocalPV; optional Longhorn for replication.

Acceptance
- RethinkDB StatefulSet binds PVCs and survives restarts; Workspace PVCs persist.

## Phase 8 — Networking and ingress parity

Tasks
- Keep reverse proxy priority: Service/LB → API-proxy → port-forward fallback.
- Document optional tailscale serve/funnel for private exposure.
- Ensure ClusterIP/LB behavior aligns with proxy resolution on k0s.

Acceptance
- Proxy tests (incl. websockets, header rewrite) pass on k0s.

## Phase 9 — Bootstrap & attach automation

Tasks
- `scripts/attach-local-k0s.sh`:
  - Read emitted kubeconfig and call Host App `/bootstrap`.
  - Apply default per-cluster settings (e.g., API proxy hints, imagePullSecret) via `PUT /api/settings/cluster/{id}`.
- UI “Download join config” remains unchanged.

Acceptance
- Two devices, both on k0s, bootstrap to the same deterministic cluster ID and pass federation e2e.

## Phase 10 — Documentation updates

Required updates (commit alongside implementation):
- `README.md`: Docker-only quickstart (tailscale + k0s + DinD); MicroK8s marked legacy.
- `DEPLOYMENT.md`: containerized node steps, volumes, route advertising, registry flow, verification.
- `architecture.md`: per-device containers (Tailscale, k0s, DinD, Host App, Operator) and tailnet-only links.
- `API.md`: clarify that deterministic ID and bootstrap semantics apply equally to k0s kubeconfig; show example with emitted kubeconfig.

Acceptance
- Docs reflect the new default runtime and avoid multi-line command blocks.

## Phase 11 — Tests, health, and quality gates

Tasks
- Keep `make test`, `make lint`, `make tidy` passing.
- Extend `scripts/verify-e2e.sh` to detect k0s runtime and probe kube-API, RethinkDB svc, operator logs, and workspace proxy.
- Add a tiny smoke deploy using an image built in DinD.

Acceptance
- Build: PASS • Lint/Typecheck: PASS • Tests: PASS on the new default path.

## Phase 12 — Migration and rollback

Tasks
- `scripts/node-migrate.sh`: guide migration from MicroK8s to k0s (export kubeconfig, apply CRDs, deploy DB/operator, re-bootstrap).
- Ensure `make reset` does not remove k0s volumes unless explicitly confirmed.

Acceptance
- Clear, low-risk migration path with simple rollback.

---

## Edge cases to handle

- Certificate SANs: kube-apiserver cert must include tailscale IP/MagicDNS for remote kubectl and Host App.
- Headscale unreachability: tailscale container fails fast with clear logs; Host App exposes health over `/api/health`.
- Multi-arch: support amd64/arm64 images; document `--platform` where relevant.
- Port-forward fallback: remains available when service IPs aren’t reachable.

## Proposed files and targets (high-level)

New scripts
- `scripts/k0s-node-up.sh`
- `scripts/k0s-node-down.sh`
- `scripts/attach-local-k0s.sh`

Makefile additions
- `node-up`, `node-down`, `deploy-k0s-node`, `attach-local-node`
- Update `setup-all` to prefer k0s-in-Docker by default

Optional (for reference only)
- Minimal docker-compose.yml (kept simple; shell scripts preferred to avoid multiline CLI stalling).

## Try it (quick preview commands)

Note: These commands assume the scripts from this plan are implemented.

- Bring up a device node and emit kubeconfig:
```bash
scripts/k0s-node-up.sh
```
- Attach cluster to Host App:
```bash
scripts/attach-local-k0s.sh
```
- Verify end-to-end:
```bash
make verify-e2e
```

## Acceptance summary

- Default runtime becomes Docker-only: k0s + Tailscale + DinD, with no host-level dependencies beyond Docker.
- Multi-device federation and workspace access work over the tailnet with the same Host App API/UI.
- Docs updated (README, DEPLOYMENT, architecture, API) in lockstep with implementation.
