## ADR 0003: In-cluster Source-of-Truth for cluster metadata and participation

Date: 2025-10-21

Status: Proposed

Scope
-----
This ADR defines the decision to centralize cluster metadata and participation records in the Kubernetes cluster (in-cluster SOT) rather than replicating them redundantly across devices. It also prescribes a CRD-based DeviceParticipant resource to represent device participation in the cluster.

Context
-------
- ADR 0001 introduced multi-device federated clusters and emphasized using cluster-side control-plane artifacts. To achieve consistent, auditable, and authoritative cluster state we propose storing metadata & participation state inside the cluster itself.

Decision
--------
- Store cluster-level name and metadata (cluster settings, image registry pointer) in-cluster using a well-known ConfigMap or a typed CRD (GuildNet `ClusterSettings` or similar), in namespace `guildnet-system`.
- Use a new CRD `DeviceParticipant` (group `guildnet.io`, v1alpha1) in the `guildnet-system` namespace as the canonical representation of a device participating in the cluster. Fields include device id, name, tailnetIPs, hostapp version, lastSeen, and optional endpoint info.
- Devices (HostApp instances) will attempt to create/update their DeviceParticipant record when they have valid kubeconfig/permissions; if they cannot, HostApp will still write presence telemetry to the presence DB (see ADR 0002) and reconcile when cluster access is available.

 Additional decisions and conventions
 -----------------------------------
 - Presence DB isolation: presence & realtime telemetry will be stored in a dedicated RethinkDB namespace with a deterministic naming convention (recommended: DB `guildnet_presence` with per-cluster tables `presence_<cluster-id>`). Presence DBs are system-only and must be excluded from DB management UI.
 - Heartbeat & DeviceParticipant flow: devices post heartbeats to HostApp; HostApp persists telemetry to presence tables and, when permitted, upserts a `DeviceParticipant` CR in-cluster. The cluster CR is the canonical (SOT) record for participation; presence DB is the realtime telemetry plane.
 - Streaming & changefeeds: HostApp instances subscribe to RethinkDB changefeeds and expose a streaming endpoint `/api/v1/sites/stream` (SSE/WebSocket) for near-realtime UI updates. Changefeeds are used for replication and live views.
 - Fallback and reconciliation: when RethinkDB or the cluster API is unavailable, HostApp persists writes locally (sqlite) and enqueues reconciliation/outbox records for eventual forwarding. When connectivity returns, HostApp performs backfill and reconciliation using `lastSeen` semantics to avoid downgrades.

Rationale
---------
- Kubernetes API server provides per-cluster isolation, RBAC, auditing and a naturally authoritative store. Using the cluster API solves cross-device drift and avoids the pitfalls of replicated local settings.

Consequences
------------
- Devices must have limited, scoped credentials to write DeviceParticipant CRs (RBAC rules required).
- UI/server must prefer in-cluster resources for canonical decisions and use presence DBs for realtime telemetry.

Alternatives considered
---------------------
- Store metadata in localdb and replicate: rejected because it increases drift and complicates reconciliation.

Next steps
----------
- Add `DeviceParticipant` CRD and sample RBAC rules.
- Implement DeviceParticipant create/update in HostApp heartbeat path when cluster kubeconfig is available.
- Update GET /api/v1/sites to prefer DeviceParticipant as canonical for participation.

 See also: ADR 0002 (DB replication) for presence isolation and replication plan, and `docs/implementation/0003-in-cluster-sot-implementation.md` for the full step-by-step implementation plan.

See implementation plan: `docs/implementation/0003-in-cluster-sot-implementation.md` and progress: `docs/implementation/0003-in-cluster-sot-progress.md`.
