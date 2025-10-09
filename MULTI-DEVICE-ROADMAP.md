# Multi-Device Roadmap

## 1. Vision
A mesh of devices (laptops, edge nodes, home lab servers) securely connected via a private tailnet, sharing one or more Kubernetes clusters as a common execution substrate. Every authorized device can:
- Launch Workspaces (ephemeral or semi-persistent server environments) declaratively.
- Observe, interact with, and (subject to policy) manage Workspaces launched by others.
- Rely on a single-origin proxy and consistent security boundary without bespoke ingress setup.

Long-term attributes:
- Identity-aware, quota-enforced multi-tenancy.
- Federation across geographically distributed clusters.
- Resilience and graceful degradation when individual devices or network links fail.
- Auditable actions, enforceable policy, and secure supply chain.

## 2. Design Principles
| Principle | Rationale | Applied Mechanisms |
|-----------|-----------|--------------------|
| Declarative First | Enables reconciliation, policy, and audit | Workspace CRD + operator |
| Secure Overlay Access | Avoid public exposure complexity | Tailscale (tsnet) integration |
| Single Origin UX | Simplifies browser security & auth insertion | Reverse proxy frontend |
| Hardening by Default | Reduces scale-amplified risk | Non-root, seccomp, dropped caps |
| Composability | Swap operators, add federation, evolve routing | Controller-runtime embedding |
| Deterministic Discovery | Reliable ownership & lifecycle management | Standard labels & naming |
| Progressive Enhancement | Ship minimal substrate, layer policy later | Current lean API + CRD |

## 3. Current Foundation
Existing capabilities forming the substrate:
- Workspace CRD + embedded operator (Deployment + Service reconciliation, status fields).
- Tailnet-secured connectivity (tsnet listeners) enabling multi-device HTTPS access.
- Single-origin reverse proxy with WebSocket and cookie/redirect adjustments.
- Unique workspace naming via randomized suffix to avoid collisions.
- Security posture: non-root containers, seccomp runtime default, capabilities dropped, privilege escalation disabled.
- Probe strategy tuned for slow-start images to reduce churn.
- Label scheme: `guildnet.io/managed=true`, `guildnet.io/id=<name>` (supports discovery, bulk ops).
- Log retrieval endpoints (REST + SSE tail) for basic observability.

## 4. Gap Analysis
| Category | Gap | Impact if Unaddressed |
|----------|-----|------------------------|
| Identity & Attribution | No user/device ownership recorded on Workspaces | Cannot scope views, quotas, or revoke actions |
| Multi-Tenancy | No per-owner isolation or RBAC overlay | Risk of accidental interference |
| Policy & Admission | No image allowlist / resource guardrails | Cluster resource abuse or unsafe images |
| Quotas & Fairness | No per-owner concurrency or resource caps | Single actor can exhaust capacity |
| Observability | No metrics, structured events, or Conditions | Hard to debug at scale |
| Lifecycle / Hygiene | No TTL, idle reaper, or lease model | Drift & abandoned workloads accumulate |
| Federation | Single-cluster assumption | Limits geographic distribution, latency optimization |
| Resilience | Operator embedded only; no HA / leader election | Single Host App failure stops reconciliation |
| API Completeness | Missing `/api/workspaces`, Conditions, filtering | External tooling & automation limited |
| Startup Resilience | No startupProbe or variant probe profiles | Certain images may flap or false-fail |
| Security Policy | No provenance/signature enforcement | Supply chain risks propagate |
| Persistence | No standardized volume abstraction | Limits class of deployable workloads |
| Conflict Backoff | Simple next-reconcile pattern only | Potential thrash under rapid changes |

## 5. Specification & Outcomes Catalog

Each numbered specification defines: Objective, Scope, Functional Requirements (FR), Non‑Functional Requirements (NFR), Interfaces, Data Model Impact, Acceptance Outcomes.

### Spec 1: Workspace Ownership & Basic Attribution
Objective: Associate each Workspace with an originating device/user identity.
Scope: API layer, Workspace CRD labels, listing endpoints.
FR:
	1. Inject `guildnet.io/owner=<stable-id>` label on creation.
	2. Expose `owner` field in `/api/servers` and `/api/workspaces` responses.
	3. Reject creation if owner label injection fails.
NFR:
	- Injection adds <1ms overhead per request.
	- Deterministic stable-id generation (collision probability ~0).
Interfaces:
	- New: `GET /api/workspaces` (list), `GET /api/workspaces/{name}` (detail).
Data Model:
	- No CRD schema change (label only).
Acceptance Outcomes:
	- A listed workspace includes `owner` and matches tailnet identity mapping.
	- Two different devices produce distinct owner values.

### Spec 2: Workspace Conditions Framework
Objective: Provide structured status diagnostics for lifecycle and failure modes.
Scope: Operator reconciliation, CRD status extension.
FR:
	1. Maintain `status.conditions[]` with `type,status,reason,message,lastTransitionTime`.
	2. Emit at minimum: `Ready`, `Progressing`, `Failed`.
	3. Update `Ready` only when Deployment readyReplicas > 0.
	4. Set `Failed` if pod enters CrashLoopBackOff > X retries (configurable).
NFR:
	- Condition update frequency bounded to avoid status spam (>200ms debounce).
Interfaces:
	- Read via existing list/detail endpoints.
Data Model:
	- Extend CRD schema with `conditions` array (struct).
Acceptance Outcomes:
	- A crashing image surfaces `Failed` with reason.
	- Stable workspace shows `Ready=True` and no `Failed` condition.

### Spec 3: Image & Resource Admission Policy
Objective: Enforce baseline safety and resource fairness.
Scope: API preflight + operator defaulting.
FR:
	1. Deny images not matching allowlist regex(es) (config var or file).
	2. Inject default cpu/memory requests/limits if absent.
	3. Reject requests exceeding configured max CPU/memory per workspace.
	4. Return structured error JSON (`code`,`reason`,`detail`).
NFR:
	- Admission decision <5ms typical.
	- Config reload without restart (SIGHUP or periodic poll) optional.
Interfaces:
	- Config: `WORKSPACE_IMAGE_ALLOWLIST`, `WORKSPACE_CPU_MAX`, `WORKSPACE_MEM_MAX`.
Data Model:
	- None (pure validation/defaulting).
Acceptance Outcomes:
	- Disallowed image returns 400 with reason `ImageDenied`.
	- Missing resources show defaults in resulting Deployment spec.

### Spec 4: Per-Owner Concurrency Quota
Objective: Prevent single-owner resource exhaustion.
Scope: API layer counting active Workspaces.
FR:
	1. Track active count = Workspaces without terminal condition (Deleted) for owner.
	2. Config `WORKSPACE_MAX_PER_OWNER` (integer >0).
	3. Reject creation with 429 when limit reached.
NFR:
	- Count query <20ms across typical namespace size (assume label-select).
Interfaces:
	- Error schema as in Spec 3.
Data Model:
	- None.
Acceptance Outcomes:
	- Owner at limit receives 429; others unaffected.
	- Removing a workspace frees capacity within one reconcile cycle.

### Spec 5: Idle Reaping & TTL
Objective: Remove unused Workspaces automatically.
Scope: Operator periodic job + status tracking.
FR:
	1. Track `status.lastAccessTime` (proxy traffic or logs fetch updates it).
	2. If idle duration > `WORKSPACE_IDLE_TTL` delete workspace (label select opt-out: `guildnet.io/keepalive=true`).
	3. Support explicit `spec.ttlSecondsAfterFinished` for ephemeral mode.
NFR:
	- Reap scan interval configurable; default 5m.
	- Scale: 5k workspaces scan <2s.
Interfaces:
	- Config: `WORKSPACE_IDLE_TTL`.
Data Model:
	- Add `lastAccessTime` field to status.
Acceptance Outcomes:
	- Idle workspace removed after TTL with event log.
	- Workspace with keepalive label persists.

### Spec 6: Metrics & Events Observability
Objective: Provide operational insight.
Scope: Host App + operator HTTP metrics endpoint; event emission.
FR:
	1. Expose Prometheus endpoint `/metrics` (combined or split).
	2. Metrics: `guildnet_workspace_total`, `guildnet_workspace_active`, `guildnet_workspace_failures_total`, `guildnet_quota_reject_total`, latency histogram for create.
	3. Emit structured events (in-memory ring + optional stdout) for state transitions.
NFR:
	- Metrics scrape <50ms.
Interfaces:
	- /metrics, optional `/api/events` (paginated tail).
Data Model:
	- None persistent.
Acceptance Outcomes:
	- Dashboard shows active vs total; failure spikes observable.
	- Event stream includes launch and reap events.

### Spec 7: High Availability Operator Mode
Objective: Decouple reconciliation from any single Host App instance.
Scope: Standalone operator Deployment with leader election.
FR:
	1. Flag `HOSTAPP_DISABLE_OPERATOR=true` disables embedded reconcile.
	2. Provide K8s Deployment manifest (or Helm snippet) for external operator.
	3. Leader election via Lease resource.
	4. Safe coexistence: only one active reconciler at a time.
NFR:
	- Failover <15s after leader pod termination.
Interfaces:
	- Config flag, deployment YAML.
Data Model:
	- None.
Acceptance Outcomes:
	- Two Host Apps run concurrently; operator pod solely reconciles.
	- Kill operator pod → new leader within target window.

### Spec 8: Multi-Cluster Placement
Objective: Schedule Workspaces across registered clusters.
Scope: New CRDs + proxy routing logic.
FR:
	1. `ClusterRegistration` CRD enumerates reachable clusters (kubeconfig secret ref + labels).
	2. `spec.placement` on Workspace selects cluster by label selector; default cluster used otherwise.
	3. Proxy resolves remote workspace transparently (tailnet or gateway path).
	4. Health degrade if cluster unreachable (Condition update).
NFR:
	- Placement decision <30ms.
Interfaces:
	- New CRDs, config for default cluster name.
Data Model:
	- Workspace spec extension, new CRDs.
Acceptance Outcomes:
	- Workspace appears only in target cluster.
	- Proxying cross-cluster succeeds with same URL pattern.

### Spec 9: Supply Chain Integrity
Objective: Enforce trusted image execution.
Scope: Admission + optional verify step.
FR:
	1. Cosign signature verification when `WORKSPACE_REQUIRE_SIGNATURE=true`.
	2. Deny unsigned or untrusted predicate images with `ImageNotVerified` reason.
	3. Cache signature verifications (LRU) for pull efficiency.
NFR:
	- Verification cache hit path <5ms.
Interfaces:
	- Config: `WORKSPACE_REQUIRE_SIGNATURE`, `WORKSPACE_TRUSTED_ATTESTERS`.
Data Model:
	- None.
Acceptance Outcomes:
	- Signed allowed image launches; unsigned denied with clear message.

### Spec 10: Persistent Workspace Storage
Objective: Support stateful workloads.
Scope: CRD spec extension + operator volume handling.
FR:
	1. `spec.volumes[]` supporting `persistent` (PVC template) and `ephemeral` (emptyDir) types.
	2. Auto-create PVCs with name pattern `<workspace>-<volname>`.
	3. Optional snapshot operation via annotation (if CSI snapshots enabled).
NFR:
	- Volume creation integrated within single reconcile pass.
Interfaces:
	- Snapshot trigger annotation `guildnet.io/snapshot=<name>`.
Data Model:
	- Spec volumes array; status includes volume claim refs.
Acceptance Outcomes:
	- Restart preserves data for persistent volume.
	- Snapshot annotation produces snapshot resource.

### Spec 11: Advanced Probes & Startup Profiles
Objective: Reduce false negative readiness for diverse images.
Scope: Operator probe templating.
FR:
	1. Support probe profiles selectable via annotation (`guildnet.io/probe-profile=slow|default|fast`).
	2. StartupProbe optional when profile=slow.
	3. Override path via `guildnet.io/probe-path` annotation.
NFR:
	- Profile change reconciled within one cycle.
Interfaces:
	- Annotations as above.
Data Model:
	- None.
Acceptance Outcomes:
	- Slow profile prevents early restarts for known heavy images.

### Spec 12: Conflict Backoff & Reconcile Efficiency
Objective: Avoid thrash during rapid concurrent updates.
Scope: Operator reconciliation loop.
FR:
	1. Detect update conflicts and apply exponential backoff (cap 2s) per workspace.
	2. Aggregate rapid spec changes within debounce window (200ms) before apply.
	3. Emit Condition `Progressing` with reason `ConflictBackoff` when backing off.
NFR:
	- Backoff metadata memory footprint O(active workspaces).
Interfaces:
	- None external.
Data Model:
	- None.
Acceptance Outcomes:
	- Burst spec edits settle with minimal redundant updates.

### Spec 13: Events & API Access to History
Objective: Expose lifecycle events for tooling.
Scope: Host App event buffering API.
FR:
	1. In-memory ring buffer (size configurable) of structured events.
	2. `GET /api/events?since=<rfc3339>&types=Launch,Delete,...` filtering.
	3. SSE endpoint `/sse/events` for streaming.
NFR:
	- Ring append O(1); eviction deterministic.
Interfaces:
	- New endpoints above.
Data Model:
	- Transient only.
Acceptance Outcomes:
	- Client receives ordered event stream; no duplication across reconnect.

### Spec 14: Workspace Placement Policy (Pre-Federation Optimization)
Objective: Introduce policy decisions prior to multi-cluster federation.
Scope: Single cluster admission heuristics.
FR:
	1. Annotate workspace with `guildnet.io/hint=<latency|compute|memory>`.
	2. Admission sets resource defaults based on hint profile.
	3. Record applied profile in status (field `profileApplied`).
NFR:
	- Decision <3ms.
Interfaces:
	- Annotation as above.
Data Model:
	- Status field addition.
Acceptance Outcomes:
	- Different hints yield distinct resource request sets.

---
Prioritization can order these specifications based on current strategic focus (e.g., start with Specs 1,2,3,4,6 for multi-user readiness + observability baseline).

## 6. Risk Register
| Risk | Mitigation | Trigger to Reassess |
|------|------------|----------------------|
| Scope Creep in Early Phases | Enforce small batch deliverables | Phase review slippage > 2 sprints |
| Unbounded Cluster Resource Use | Implement quotas early (Phase 2) | Sustained >80% node saturation |
| Identity Ambiguity | Owner label + signature plan | Duplicate owner collisions |
| Operator Coupling | Externalize in Phase 5 | Difficulty scaling reconciles |
| Undetected Failures | Conditions & metrics | Increase in silent timeouts |
| Federation Complexity | Isolate until Phase 6 | Cross-cluster latency incidents |

## 7. Success Metrics (Indicative)
Metric | Target (post phases noted)
-------|---------------------------
Mean launch latency (Phase 2) | < 15s P50 for common images
Failed launch rate (Phase 4) | < 5% non-user-error
Idle reaped reclaim (Phase 3) | > 30% of stale workspaces/day
Quota denial correctness (Phase 2) | 100% reproducible
Condition diagnostic coverage (Phase 4) | > 90% of failure classes categorized
Cross-cluster success rate (Phase 6) | > 95% of routed proxy sessions

## 8. Immediate Next Steps (Actionable Backlog Seed)
1. Add `/api/workspaces` endpoint (list/detail) sourcing CRDs.
2. Inject `guildnet.io/owner` label and surface owner in list responses.
3. Implement initial Conditions array on `Workspace.status`.
4. Add per-owner max concurrency check in API before CR creation.
5. Introduce minimal metrics registry (workspace_total, workspace_active, workspace_failed).

## 9. Glossary
Term | Definition
-----|-----------
Workspace | Declarative resource describing a deployable server environment
Owner | Derived identity label representing originating device/user context
Condition | Structured status entry describing state facts (type, status, reason, message)
Placement | Directive guiding which cluster should host a Workspace
Idle TTL | Duration after last access after which a Workspace is eligible for removal

## 10. Architectural Leverage Summary
The current system (CRD + operator + tailnet proxy) reduces future migration cost and establishes a clear control surface for identity, policy, and federation layers. Each phase layers orthogonal capabilities without reworking the substrate.

---
This roadmap is intentionally incremental. Each completed phase produces user-visible value and de-risks later federation and security ambitions.
