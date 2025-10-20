# Implementation Plan: Durable cross-device DB replication

Date: 2025-10-21

Summary
-------
This document expands ADR 0002 into a concrete implementation plan: file-level tasks, tests, acceptance criteria, and a phased rollout for adding robust cross-device DB replication to GuildNet.

Contract
--------
- Inputs: local mutations (user/system), per-cluster RethinkDB changefeeds, network connectivity state.  
- Outputs: eventual convergence of local sqlite copies on all devices, durable central RethinkDB records, observability metrics for replication health.  
- Error modes: network partitions, transient RDB auth failures, conflicting concurrent updates.  

Phases & tasks
--------------
Phase A — Outbox & write buffering (small, safe)
- Add a local outbox collection in `localdb` (collection name `replicator_out`).  
- Modify mutating API handlers to persist writes to localdb immediately and attempt remote write; on remote failure, enqueue outbox item.  
- Start a local forwarder goroutine to flush outbox items to RethinkDB when available.  

Files to change
- `internal/api/multidevicesvc_handlers.go` — adjust heartbeat/other mutating handlers.  
- `cmd/hostapp/main.go` — ensure bucket creation and start forwarder worker.  
- `internal/localdb/db.go` — no changes required for primitives; document use of `replicator_out`.  

Phase B — Changefeed-based replication consumer (central->device)
- Implement RethinkDB helpers for changefeed subscription and an idempotent apply path to localdb.  
- Start per-cluster replication goroutines when `inst.RDB` is present (in `internal/cluster/registry.go`).  

Files to change
- `internal/db/` (add `rethink_replication.go`): subscribe to tables `presence_*` and application tables.  
- `internal/cluster/registry.go`: start/stop replication goroutines tied to Instance lifecycle.  

Phase C — Reconciliation & conflict resolution
- Implement LWW conflict rules on apply (compare `lastModified`).  
- Define CRDTs or per-table merge functions for tables that require richer semantics.  

Phase D — UI & management polish
- Hide presence DBs/tables from DB management UI.  
- Add metrics and admin endpoints for replicator backlog, changefeed lag, and replication health.  

Tests & verification
- Unit tests for outbox enqueue/forward logic (simulate transient RDB failure).  
- Integration test: two hostapps + RethinkDB; write from host A; verify B receives via changefeed and writes localdb.  
- Partition test: host B writes while RDB down; replay outbox after RDB restored; ensure B's write persists centrally and A converges.  

Acceptance criteria
- Writes are never lost: all local writes are persisted in localdb outbox and eventually present in RethinkDB.  
- Devices converge to the same state after recovery (within defined replication SLA).  
- Presence DBs are excluded from DB management UI.  

Rollout plan
- Step 1: implement Phase A and release a minor hostapp version.  
- Step 2: implement Phase B in a followup release and enable replication for a subset of non-critical tables.  
- Step 3: expand replication to all system and user tables after validating conflict policies.  

Operational notes
- Monitor `replicator_out` size; alert if backlog grows beyond threshold.  
- Add metrics: `replicator_out_items`, `replication_lag_seconds`, `replication_apply_errors`.  

See progress tracker: `docs/implementation/0002-db-replication-progress.md`.
