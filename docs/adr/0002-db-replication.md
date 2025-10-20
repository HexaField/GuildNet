## ADR 0002: Durable cross-device database replication

Date: 2025-10-21

Status: Proposed

Scope
-----
This ADR documents the design choice for implementing full, durable replication of both system and user databases across participating devices in GuildNet. The goal is to guarantee that no user or system data is lost if a device is offline or permanently lost, while preserving reasonable operational simplicity and safety.

Context
-------
- GuildNet currently uses per-cluster SQLite (`localdb`) for local persistence and RethinkDB for in-cluster/stateful DB usage. Existing code paths include `internal/localdb`, `internal/db` (RethinkDB helpers), and `internal/cluster/registry.go` which wires per-cluster instances.
- The multi-device federation design requires cross-device data durability and eventual convergence across participating hosts.
- Operational constraints: the solution must be deployable in the existing production flow (no separate dev mode), secure by default, and incremental: we should be able to add replication without stopping the system.

Decision
--------
We will implement a hybrid replication architecture with a central, cluster-authoritative store (per-cluster RethinkDB) and durable per-device local replicas (SQLite). Key points:

- All mutating writes are durably persisted locally immediately (local outbox). Devices forward writes to the central RethinkDB when connectivity permits.
- Devices subscribe to RethinkDB changefeeds and apply changes to their local sqlite in an idempotent manner. This provides guaranteed eventual replication from central -> device.
- The local outbox ensures device-origin writes survive network partitions; a forwarder reliably replays outbox entries to central RethinkDB when connectivity returns.
- Conflict resolution defaults to deterministic Last-Write-Wins based on a required `lastModified` timestamp + source tie-breaker. For data needing richer merge semantics, CRDT or domain-specific merge functions will be implemented on a per-collection basis.
- Presence and telemetry data are isolated into dedicated, system-named presence tables (prefixed with `presence_` or colocated in a `guildnet_presence` DB) and explicitly excluded from user-visible DB management screens.

Rationale
---------
- Guarantees durability: every write is persisted locally immediately and forwarded to a central durable store.
- Simpler than full multi-master consensus or global CRDTs for the entire product surface; incremental and actionable given current architecture.
- Uses RethinkDB changefeeds (already in use in repo patterns) for efficient fan-out and device-side replication.

Consequences
------------
- Eventual consistency: devices converge after network recovery; while short-term conflicts can occur, deterministic conflict rules avoid silent data loss in most cases.
- Some data types will need bespoke merging logic; implementers must identify tables that cannot tolerate LWW behavior.
- Operational overhead: outbox monitoring, replication lag metrics, and backlog alerts are required.

Alternatives considered
---------------------
- Full multi-master with consensus (Raft/etcd): Provides strong consistency but is heavy and not practical across unreliable WANed devices.
- CRDT-first approach: Attractive for conflict-free multi-writer semantics, but substantial engineering cost and not necessary for many guildnet objects.

Next steps
----------
1. Implement local outbox and forwarder (hostapp modifications).  
2. Implement RethinkDB replication consumer (changefeed -> localdb applies).  
3. Add presence DB isolation rules and hide system DBs in UI.  
4. Add tests and monitoring (replicator backlog, changefeed lag).  

See implementation plan: `docs/implementation/0002-db-replication-implementation.md` and progress: `docs/implementation/0002-db-replication-progress.md`.
