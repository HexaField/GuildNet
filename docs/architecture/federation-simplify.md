# Federation simplification (ADR companion)

This document summarizes pragmatic, low-risk simplifications to the current federation/multi-device implementation. They are intended to reduce complexity, improve observability, and make the in-cluster DeviceParticipant CRD the canonical Source-of-Truth (SOT) for connected devices.

Goals
- Make the cluster-hosted DB the authoritative surface for device presence.
- Reduce duplication between presence feeds and DeviceParticipant records.
- Simplify the retry/outbox/pending queue logic.
- Reduce dynamic client churn and centralize reconciliation logic per-Instance.

Suggested changes

1) Unified cluster overview endpoint

- Provide `/api/cluster/{id}/overview` that returns a single JSON blob: cluster record (if any), current sites list (from per-cluster local DB), and federated services summary. This reduces client roundtrips and simplifies the Settings UI.

2) Make DeviceParticipant CR the canonical SOT (in-cluster)

- Heartbeats continue to populate the per-cluster presence table (fast, local write).
- A reconciler ensures a corresponding DeviceParticipant CR exists (best-effort). The reconciler writes CRs via the dynamic client; failures are stored in a small pending queue in the per-cluster local DB and retried.
- The DeviceParticipant CR should be the authoritative record used by cluster-facing controllers and operators.

3) Simplified pending queue / outbox

- Keep the pending items in a single per-cluster table named `pending_deviceparticipants`.
- Store the minimal payload required to upsert the CR and a retry counter/timestamp.
- Reconciliation is performed by a single goroutine per Instance which drains the table and attempts idempotent upserts (Create or Update) using the dynamic client. If the dynamic client isn't available (no kubeconfig), keep entries for later.

4) Normalize changefeeds to emit enough context for the UI

- Changefeed messages should include the cluster id as `clusterId` and the standard `event` object containing `new_val` and `old_val` (or explicit op: added/updated/removed). This allows the UI to perform granular in-place updates instead of refetching entire lists.

5) Reduce dynamic client duplication and permissions surface

- Use a single dynamic client per Instance instead of creating many ad-hoc clients.
- Provide explicit RBAC examples (Role/ClusterRole) that grant the minimum: create/update/get/list/watch/patch for the DeviceParticipant CRD and update/status when necessary.

Operational notes
- Keep the reconciler best-effort and non-blocking: presence writes must never block because of Kubernetes API availability.
- Monitor the size of `pending_deviceparticipants` and alert when backlog increases.
- Agent images should include a small debug endpoint to surface pending queue size and last error for easier operator troubleshooting.

Acceptance criteria
- Client-side Settings UI uses `/api/cluster/{id}/overview` for initial load.
- SSE messages use `clusterId` and event new_val/old_val semantics; UI updates incrementally.
- Reconciler drains pending entries and removes them on success. Backlog is observable.

Notes
- These changes are intentionally incremental; they do not require a coordinated migration. Client-side normalization (accepting `cluster` or `clusterId`) lets the server adopt `clusterId` when ready.
# Federation simplification — architecture notes & implementation plan

Goal
- Reduce concept duplication between per-cluster local DB presence rows and the in-cluster DeviceParticipant CRD.
- Provide a single, simple API that the UI and other components can use to show cluster overview (health, devices, federated services).

Summary of recommended changes
1. Unified cluster overview endpoint
   - Provide `/api/cluster/{id}/overview` that returns cluster record, health summary, devices (site rows) and federated services in one request.
   - Benefits: UI only needs one call for Settings; fewer roundtrips and simpler client code.

2. Canonical source-of-truth
   - Treat DeviceParticipant CR (when available) as the canonical authoritative record for device presence and metadata.
   - Keep per-cluster `devices` localdb as a cache for fast reads and offline scenarios; use it as read-through only.
   - Benefit: clear authority and auditability via Kubernetes resources and RBAC/audit logs.

3. Simplified outbox & reconcilier
   - Replace `pending_deviceparticipants` with a compact outbox schema: { id, payload, attempts, nextRetryAt, lastError }.
   - Centralize retry logic and expose metrics/logs about permission failures vs transient errors.

4. Single changefeed channel
   - Normalize RethinkDB changefeeds and DeviceParticipant change events into a single `sites` changefeed topic.
   - UI subscribes to `/v1/sites/stream` and receives normalized events of the shape: { type: 'device.updated'|'device.added'|'device.removed'|'federatedservice.*', cluster, payload }.

Implementation plan (safe incremental steps)
- Step 1 (low-risk): add `/api/cluster/{id}/overview` (done). Update UI to use it in cluster Settings (done).
- Step 2 (medium-risk): Add unified changefeed normalization layer in the HostApp that consumes per-cluster RethinkDB feeds and emits normalized events to `/v1/sites/stream`.
- Step 3 (medium-risk): Introduce compact outbox schema and a centralized reconcilier with exponential backoff and error classification.
- Step 4 (higher risk): Migrate read paths to prefer DeviceParticipant CRs where available and treat localdb as cache. Provide a migration script/mode to backfill CRs where appropriate.

Observability & operational notes
- Surface permission failures and reconcile metrics via logs and a small `/metrics` or `/debug/reconciler` endpoint to ease operator troubleshooting.
- Document RBAC requirements and provide `config/rbac` examples (already added).

Acceptance criteria
- UI Settings loads with a single call and shows cluster devices and services.
- SSE events keep the UI near real-time with normalized event payloads.
- Reconciler health and permission errors are observable and actionable.
