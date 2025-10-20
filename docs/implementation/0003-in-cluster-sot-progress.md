# Progress: In-cluster Source-of-Truth (DeviceParticipant CRD)

Live checklist for ADR 0003 implementation.

The following expands the original high-level checklist into step-by-step items. Items already completed or partially implemented are marked with notes.

- [ ] 1) Add `DeviceParticipant` API types under `api/v1alpha1`.
	- Implementation note: include fields: `id` (string), `name` (string), `lastSeen` (RFC3339 timestamp), `hostappVersion` (string), `tailnetIPs` ([]string), `resources` (cpuMilli, memoryMB, storageMB), and optional `endpoint` (host/port).

- [ ] 2) Generate CRD YAML and deepcopy files using `controller-gen` and commit or rely on CI generation.
	- Implementation note: target `guildnet-system` namespace and include sample `kubectl apply -f` instructions in `k8s/`.

- [ ] 3) Add RBAC sample manifest for device write role (namespace-scoped) and operator role (controller).
	- Implementation note: provide a `Role` allowing `create`, `update`, `patch` on `deviceparticipants` for a `ServiceAccount` used by HostApp when it has cluster credentials.

- [ ] 4) Implement `DeviceParticipant` CRUD helper in `internal/k8s`.
	- Implementation note: helper should perform an optimistic `Get` -> `Create` or `Patch` using server-side apply; return a boolean indicating if write succeeded and the returned resource.

- [ ] 5) Modify heartbeat handler to upsert DeviceParticipant when cluster credentials allow.
	- Status: partially implemented — heartbeat handler was updated earlier to require `cluster` and persist telemetry; the upsert to K8s is still pending.

- [ ] 6) Update GET /api/v1/sites to prefer DeviceParticipant existence for participation.
	- Implementation note: when `DeviceParticipant` exists, use its fields as canonical values (name, lastSeen, endpoint). Fall back to presence DB / localdb telemetry when missing.

- [ ] 7) Add reconciliation job for pending DeviceParticipant creation.
	- Implementation note: registry should spawn a reconciliation worker per-instance that checks queued outbox records and attempts to create DeviceParticipant CRs when credentials become available.

- [ ] 8) Add streaming endpoint `/api/v1/sites/stream` and changefeed wiring.
	- Implementation note: wire RethinkDB changefeeds for `presence_<cluster-id>` tables to the stream endpoint and to the UI using SSE or WebSocket with heartbeat keepalive.

- [ ] 9) Add tests: unit tests for `internal/k8s` helper, integration tests for heartbeat->DeviceParticipant lifecycle (mock k8s), and end-to-end verify-federation test.

- [ ] 10) Add sample namespace-scoped install manifests and a `k8s/` README with RBAC guidance.

- [ ] 11) Add UX notes and API docs to `API.md` describing the `DeviceParticipant` CRD and the `/api/v1/sites/stream` contract.

- [ ] 12) Security review: ensure HostApp only receives least-privilege kubeconfigs, document expected RBAC roles, and limit API surfaces for DeviceParticipant writes.

- [ ] 13) Deploy and smoke-test on two physical hosts (this was done during earlier verification): attach kubeconfigs, ensure hostapps running on 8090, post heartbeats and observe `DeviceParticipant` creation when RBAC present.
	- Status: smoke verification of hostapps and heartbeat telemetry across two hosts completed; DeviceParticipant upsert not yet implemented — verification used presence DB/local telemetry instead.

Next immediate actions
----------------------
- Implement `internal/k8s` helper and a minimal `api/v1alpha1` type for `DeviceParticipant` (items 1 and 4). This is a small, verifiable change and will let us implement the heartbeat upsert (item 5) quickly.
- After that, implement reconciliation worker (item 7) and add basic integration tests (item 9).

If you'd like, I can start by creating the `api/v1alpha1/deviceparticipant_types.go` scaffold and a small `internal/k8s/deviceparticipant.go` helper, then run `go test ./...` to ensure the build is green. Mark which branch you prefer for commits (current branch `feat/multi-device-cluster` is fine unless you want a dedicated feature branch).
