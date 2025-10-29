# Implementation Plan: In-cluster Source-of-Truth (DeviceParticipant CRD)

Date: 2025-10-21

Summary
-------
This document expands ADR 0003 into a comprehensive, concrete implementation plan. It covers presence DB isolation, heartbeat handling, changefeed streaming, the DeviceParticipant CRD and operator integration, optional per-device in-cluster agents, security/RBAC, UI/server merge behavior, migration/fallback rules, and tests/verification. Tasks are ordered small steps suitable for incremental implementation.

Contract
--------
- Inputs: host heartbeats (HostApp), cluster kubeconfigs (per-site), RethinkDB changefeeds and local sqlite persistence.  
- Outputs: in-cluster `DeviceParticipant` CRs (canonical participation), presence telemetry in isolated presence tables, streaming API for realtime clients, and merged GET /api/v1/sites responses that prefer in-cluster SOT.  
- Error modes: missing kubeconfig, insufficient RBAC, RethinkDB unavailability, conflicting concurrent updates.  

Goals
-----
- Ensure cluster metadata & participation are authoritative inside the cluster (single SOT).  
- Keep presence & telemetry realtime and device-centric (device posts heartbeat; server persists and forwards).  
- Isolate presence DBs from user-visible DB management screens.  
- Provide safe, incremental migration and robust fallbacks when cluster API or RethinkDB are unavailable.

Design overview (concise)
-------------------------
- Presence isolation: keep presence data in a dedicated DB namespace (recommended: single DB `guildnet_presence` with per-cluster tables `presence_<clusterID>`). Backend/UI must filter these out.  
- Heartbeat handling: POST /v1/sites/heartbeat writes into presence table in RethinkDB when `inst.RDB` is available, else falls back to per-cluster localdb. Also attempt to upsert an in-cluster `DeviceParticipant` CR when kubeconfig/RBAC allow.  
- Changefeeds & streaming: registry starts per-cluster changefeed watchers; hostapp exposes /api/v1/sites/stream (SSE or websocket) to forward realtime events to UI.  
- DeviceParticipant CRD: cluster-native resource in `guildnet-system` namespace representing device participation. Device creates/updates its CRD on heartbeat when possible; otherwise presence DB is used as realtime fallback.  

Concrete ordered implementation steps
------------------------------------

Step 0 — constants & naming (very small)
- Add `internal/db/constants.go` with PresenceDBName = `guildnet_presence` and PresenceTablePrefix = `presence_`. Use deterministic cluster NormalID for table suffixes.  
- Update any helper comments and document the naming convention.

Step 1 — Presence DB isolation and UI filter (low risk)
- Backend: update the DB listing helper to exclude `guildnet_presence` DB or tables prefixed with `presence_`. This prevents exposure in DB management UI.  
	- File(s): the endpoint(s) that enumerate RethinkDB databases/tables in `internal/db/` (or wherever listing is implemented). If listing is performed by the UI directly, add a backend wrapper endpoint that returns filtered results.  
- UI: ensure `ui/src/*` callers use the backend list endpoint and do not call any low-level admin API directly.  

Step 2 — Heartbeat write to presence RethinkDB with local fallback
- Modify POST /v1/sites/heartbeat to prefer RethinkDB when available:  
	- If `inst, _ := deps.Registry.Get(ctx, clusterID)` and `inst.RDB != nil` then `inst.RDB.Put(tableName, deviceID, payload)` where `tableName = fmt.Sprintf("presence_%s", NormalID(clusterID))`.  
	- Else fallback to `inst.DB.Put("devices", deviceID, payload)` (existing localdb behavior).  
- Ensure `payload["lastSeen"]` is set to `time.Now().UTC()` before persist.  
	- Files: `internal/api/multidevicesvc_handlers.go` (heartbeat handler), `internal/db/` add lightweight RethinkDB wrappers `Put/Get/List` if needed.  

Step 3 — Reconciliation (prefill/backfill)
- When an Instance transitions from no-RDB to RDB available, perform a backfill: read per-cluster sqlite `devices` rows from `~/.guildnet/state/<cluster>/guildnet.sqlite` and upsert into presence table using `lastSeen` as version.  
	- File: `internal/cluster/registry.go` — after `inst.RDB` becomes available, call a reconcile helper that reads localdb and writes to RDB (skip older records).  

Step 4 — Changefeed watcher & streaming API (near-realtime)
- Start changefeed watchers per cluster `Instance` once `inst.RDB` is present. The watcher publishes events to an Instance-local pubsub channel.  
	- Files: `internal/cluster/registry.go` (add `inst.watchPresenceChangefeed()`), `internal/db/rethink_replication.go` (changefeed helper).  
- Add a subscription API: `Registry.SubscribePresence(clusterID) -> (<-chan Event, unsubscribe func())`.  
- Hostapp stream endpoint: add `/api/v1/sites/stream` (SSE or websocket) in `internal/api/multidevicesvc_handlers.go`. Clients may subscribe to one cluster or all clusters. Server forwards changefeed events immediately.  
- UI: implement WebSocket/SSE client in `ui/src/routes/MultiDevice.tsx` to apply events live.  

Step 5 — DeviceParticipant CRD (in-cluster SOT)
- Add `api/v1alpha1/deviceparticipant_types.go` defining `DeviceParticipant` with small `spec` and `status` fields:  
	- spec: deviceID, name, tailnetIPs, hostappVersion, optional endpoint info  
	- status: lastSeen (RFC3339), state (string), health (optional struct)  
- Generate CRD YAML with `controller-gen` and place under `config/crd/bases/guildnet.io_deviceparticipants.yaml`.  
	- Add `go:generate` markers in `api/v1alpha1` if not already present.  

Step 6 — HostApp: create/update DeviceParticipant on heartbeat
- In heartbeat handler (same place as Step 2), after writing presence telemetry, attempt to upsert DeviceParticipant CR in the cluster if: `inst` exists, `inst.Dyn` or `inst.K8s` is available and the credentials permit CR creation.  
	- Implement helper: `internal/k8s/deviceparticipant.go` with `CreateOrUpdateDeviceParticipant(ctx, inst, ns, rec)`. Use `Resource(...).Namespace(ns).Get/Create/Update`.  
	- Use safe retries/backoff and do not block heartbeat on failures (log and enqueue reconciliation).  

Step 7 — Fallback & reconciliation queue for CRD creation
- If DeviceParticipant cannot be created (missing kubeconfig or insufficient RBAC), enqueue a reconciliation task (localdb collection `pending_deviceparticipants`) to attempt creation when cluster connectivity or credentials change.  
- Add a reconciler worker that periodically tries to reconcile pending CRD creations.  

Step 8 — GET /api/v1/sites merge logic (server-side)
- Update `internal/api/multidevicesvc_handlers.go` GET /v1/sites to compute per-device records by merging three sources in this order of authority:  
	1. In-cluster DeviceParticipant CRs (existence => participatingInCluster).  
	2. Realtime presence RethinkDB rows (lastSeen, cpu/memory/storage/tailnetIPs).  
	3. Local in-memory HostApp agent records (internal/store) and localdb fallback.  
- The response should normalize keys (`cluster`, `id`) and avoid exposing legacy `clusterId`.  

Step 9 — Optional per-device in-cluster Pod (opt-in)
- Provide an operator-managed resource `DeviceAgentRequest` CRD: device requests via CRD and the operator reconciles into a `Deployment` or `DaemonSet` that runs a small proxy agent in-cluster representing the device.  
- This is opt-in and gated by RBAC and admin consent (imagePullSecret, allowed images).  
- Implementation is separate; starting with the CRD-only approach is recommended.  

Step 10 — UI: hide system DBs & show merged participation
- Update DB management UI to filter out `guildnet_presence` DB or tables with `presence_` prefix. Implement server-side filtering as authoritative.  
- Update MultiDevice UI to subscribe to `/api/v1/sites/stream` and to present `participatingInCluster`, `lastSeenRealtime`, `tailnetIPs`, and `hostappRunning`.  

Step 11 — Security & RBAC (critical)
- RethinkDB credentials: isolate presence DBs with dedicated credentials and store them as K8s Secrets in `guildnet-system`. Do not hand RDB admin to devices. HostApp performs DB writes on behalf of devices.  
- Kubernetes RBAC: create a namespace-scoped Role `guildnet-device-writer` that allows `create`/`update` on `DeviceParticipant` resources in `guildnet-system`. Onboard devices by issuing short-lived tokens bound to a ServiceAccount with this Role.  
- For per-device Pod requests, use a specific `DeviceAgentRequest` CRD and operator reconciliation so device cannot run arbitrary pods without operator mediation.  

Step 12 — Migration & reconciliation rules
- Backfill: when RethinkDB becomes available for a cluster, reconcile local sqlite `devices` rows into presence tables; only insert/overwrite if incoming `lastSeen` is newer.  
- On-host behavior for heartbeat:  
	1. Attempt DeviceParticipant upsert in-cluster (non-blocking).  
	2. Persist payload to presence RethinkDB if available.  
	3. Persist payload to localdb as durable fallback.  
- Reconciliation worker forwards pending CRD creations and outbox items when cluster/RDB become reachable.  

Step 13 — Tests & verification plan
- Unit tests:  
	- Heartbeat handler writes to RDB when available; falls back to localdb when not (mock `inst.RDB`).  
	- DeviceParticipant CRUD helper tests with fake dynamic client.  
- Integration tests:  
	- Start a cluster with RethinkDB; post heartbeats and subscribe to `/api/v1/sites/stream`; verify changefeed events arrive.  
	- Simulate device without kubeconfig: heartbeat should persist to RDB and localdb; pending CRD should be reconciled once credentials are available.  
	- Device with kubeconfig creates DeviceParticipant; GET /api/v1/sites shows participating=true.  
- Manual smoke commands:  
	- curl -k -X POST https://127.0.0.1:8090/api/v1/sites/heartbeat -d '{"id":"deviceX","clusterId":"<cid>","tailnetIPs":["100.64.0.9"]}' -H 'Content-Type: application/json'  
	- curl -k https://127.0.0.1:8090/api/v1/sites/stream?cluster=<cid> and observe events.  
	- kubectl -n guildnet-system get deviceparticipants  

Files to add / modify (summary)
- Add: `internal/db/constants.go` (PresenceDB/Prefix constants).  
- Modify: `internal/api/multidevicesvc_handlers.go` (heartbeat handler, GET /v1/sites, stream endpoint).  
- Modify: `internal/cluster/registry.go` (start changefeed, backfill on RDB availability, CreateOrUpdateDeviceParticipant helper).  
- Add: `internal/db/rethink_replication.go` (changefeed helpers) and optionally extend `internal/db/db.go` with Put/Get for RethinkDB.  
- Add: `internal/k8s/deviceparticipant.go` (CRUD helper).  
- Add: `api/v1alpha1/deviceparticipant_types.go` and generated CRD under `config/crd/bases/`.  
- Modify UI: `ui/src/routes/MultiDevice.tsx` and DB-listing components to subscribe to stream and hide presence DBs.  

Acceptance criteria
-------------------
- DeviceParticipation is authoritative: when DeviceParticipant exists in-cluster, GET /v1/sites reflects participatingInCluster=true.  
- Realtime telemetry: presence RethinkDB changefeeds are forwarded to clients via `/api/v1/sites/stream`.  
- Isolation: presence DBs do not appear in DB management UI.  
- Durable fallback: when RDB/K8s API are down, heartbeats are persisted locally and backlog is forwarded/reconciled after recovery.  

Operational guidance
--------------------
- Monitor: replicator_out size, replication lag, DeviceParticipant reconcile failures.  
- Alerts: backlog > threshold, repeated CRD create failures, RDB auth errors.  
- Rotation: rotate K8s tokens used by devices; revoke ServiceAccount tokens to remove device permission.  

Notes and alternatives
----------------------
- If you prefer a minimal initial rollout, skip per-device CRD creation and rely solely on presence DB + reconciler; enable CRD upserts behind a feature flag.  
- Per-device Pods are opt-in and should be operator-controlled to avoid arbitrary workload creation by devices.  

Next actionable step (recommended)
---------------------------------
Implement Step 0 and Step 1 (constants + DB filter) and Step 2 (heartbeat -> presence RethinkDB with local fallback). These are low-risk, deliver immediate value (presence isolation + durable writes) and set the stage for changefeeds and CRD integration.

Phases & tasks
--------------
Phase 1 — CRD scaffold & RBAC
- Add `api/v1alpha1/deviceparticipant_types.go` defining `DeviceParticipant` spec/status.  
- Add `config/crd/bases/guildnet.io_deviceparticipants.yaml` (generated by controller-gen) and RBAC sample manifest: a Role allowing create/update for DeviceParticipant in `guildnet-system` and a limited ClusterRole for operator/controller.  

Phase 2 — HostApp integration
- Modify heartbeat handler (`internal/api/multidevicesvc_handlers.go`) to attempt creating/updating DeviceParticipant CR via `inst.Dyn` or `inst.K8s` when `inst` is available and credentials permit.  
- Add a helper `internal/k8s/deviceparticipant.go` to encapsulate CRUD operations and retry/backoff behavior.  

Phase 3 — GET /api/v1/sites semantics and UI
- Update `internal/api/multidevicesvc_handlers.go` GET /v1/sites to prefer DeviceParticipant presence as canonical; supplement with presence DB telemetry for lastSeen/capacity.  
- Update UI pages (`ui/src/routes/MultiDevice.tsx`) to show `participatingInCluster` derived from DeviceParticipant existence.  

Phase 4 — Fallback & reconciliation
- If DeviceParticipant cannot be created (no kubeconfig or RBAC), hostapp writes to presence DB and marks device as pending CRD creation; reconcile jobs attempt to reconcile once cluster access is available.  

Tests & verification
- Unit tests for deviceparticipant helper CRUD with fake dynamic client.  
- Integration test: device creates CR and GET /api/v1/sites shows participating=true.  

Acceptance criteria
- DeviceParticipant CRD installs cleanly.  
- When device has kubeconfig & permission, HostApp successfully creates/upserts CR on heartbeat.  
- GET /api/v1/sites prefers DeviceParticipant for participation flag.  

Rollout plan
- Add CRD + RBAC in config/crd and deploy.  
- Update hostapp to create DeviceParticipants optionally behind a feature flag.  

See progress tracker: `docs/implementation/0003-in-cluster-sot-progress.md`.
