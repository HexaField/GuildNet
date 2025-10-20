# Progress: Durable cross-device DB replication

Live checklist for ADR 0002 implementation.

- [ ] Phase A — Add local outbox and forwarder in HostApp.
- [ ] Phase A tests — unit tests for outbox enqueue/forward.
- [ ] Phase B — Add RethinkDB changefeed consumer and per-cluster replication worker.
- [ ] Phase B tests — integration test across two hostapps and RethinkDB.
- [ ] Phase C — Implement conflict resolution policies and CRDTs for required tables.
- [ ] Phase D — Add monitoring, metrics, and DB UI filtering for presence DBs.
- [ ] Acceptance tests — simulate partition and verify convergence.

Notes
- Prioritize Phase A to ensure no writes are lost in transient network failures.  
- Document the presence DB naming policy to avoid accidental exposure (see ADR 0002).  
