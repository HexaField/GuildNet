# ADR 0002 — Dockerized k0s stack-runner image

Date: 2025-11-01

Status: proposed

Context
-------
This ADR defines the work and runtime contract required to support deploying k0s-in-Docker clusters via a pre-built Docker image (the "stack-runner"). Instead of documenting a single chosen approach, this file lays out the concrete tasks and requirements to implement an image-based provisioning flow that a HostApp orchestration job can run and consume.

Decision
--------
The implementation should provide the following capabilities and runtime behavior so the HostApp can reliably provision clusters from a prebuilt image:

Rationale
---------
- Single artifact: a registry-publishable image makes provisioning reproducible and versionable.
- Developer UX: "deploy = docker run <image>" is straightforward and minimizes host-side logic.
- DinD inside the image enables the local image pipeline (build/import) without depending on host Docker/socket, improving hermetic behavior.
- In-image addon installation lets the entire cluster come up in one container lifecycle and reduces timing/race issues between HostApp and in-cluster components.

Constraints and consequences
---------------------------
The implementation should provide the following capabilities and runtime behavior so the HostApp can reliably provision clusters from a prebuilt image:
- Platform nuances: cgroups v2, SELinux/AppArmor, and kernel features (ip_tables, nftables, overlayfs) are host-dependent; CI will cover common cases but operators must confirm host compatibility.
- Orchestration changes: HostApp must be able to run containers on the host (Docker CLI or containerd) and stream logs into the job system. It must poll `/out/kubeconfig` and handle timeouts/cancellation.

Implementation plan
---------------------------
- Add `images/stack-runner/Dockerfile` and `scripts/stack-runner-entrypoint.sh` that boot a basic k0s controller+worker and write `/out/kubeconfig` when ready. Include DinD minimal bits for later use but keep PoC focused.
- Add `make stack-runner-build` and `make stack-runner-load` to build and locally load the image.
- Extend HostApp orchestration to accept `image` in `POST /api/deploy/clusters` and, when provided, create a host temp dir, run the container with `-v <temp>:/out -v /lib/modules:/lib/modules:ro --privileged`, stream logs into the job, wait for `/out/kubeconfig`, then attach/persist kubeconfig and set `lastJobId` on the cluster record.
- Update UI `/deploy` to accept an `image` field (prefill with the official image) and submit it with create requests.

When running the stack-runner on a host, the HostApp orchestration will prefer using the host's containerd runtime when available (via `ctr`/containerd API) for image execution and lifecycle management. When containerd is not available, HostApp will fall back to the Docker CLI (`docker run`). This gives better integration for systems that run containerd as the system container runtime while maintaining compatibility with Docker-based environments.
- Add robust DinD integration and image import helpers inside the image so the image pipeline (build/import) works reliably.
- Implement idempotent in-image installers for MetalLB and local-path; enable them via env vars or create-time flags. When enabled, the entrypoint runs installers after the API is ready.
- Add optional tsnet/headscale helper in the entrypoint honoring `TS_AUTHKEY`, `TS_LOGIN_SERVER`, and `TS_SERVE_KUBEAPI` envs to optionally expose the kube-API on the tailnet.
- Implement safety/cancel semantics in the job runner (stop container on cancel, record exit state).
- CI: build image, run smoke container (wait for kubeconfig), run `kubectl version` against the emitted kubeconfig, and push image tags (commit SHA and latest).

Tests, docs, rollout
- Add unit/integration tests for the orchestration runner (mock container runner) and a CI e2e smoke test that starts the image and validates kubeconfig and API reachability.
- Update `DEPLOYMENT.md`, `API.md`, and `architecture.md` to document the image contract, required runtime flags, recommended mounts, and security considerations.

Operational contract (entrypoint expectations)
--------------------------------------------
- Write `/out/kubeconfig` (YAML) once control plane is healthy. Optionally write `/out/ready` as a sentinel.
- Log to stdout/stderr for HostApp job streaming.
- Honor environment variables to toggle features: `ADDONS_LOCALPATH=true`, `ADDONS_METALLB=true`, `TS_AUTHKEY`, `TS_LOGIN_SERVER`, `TS_SERVE_KUBEAPI`, etc.
- Exit/terminate cleanly on SIGTERM (stop k0s and DinD gracefully).

API changes
-----------
- `POST /api/deploy/clusters` accepts optional `image` and `runOptions` fields. Example:

```json
{ "name": "local-k0s", "addons": { "localpath": true, "metallb": true }, "image": "ghcr.io/hexafield/guildnet-stack-runner:latest" }
```

- The created job returns `{ id, jobId }`. The cluster record persists `lastJobId` so the UI can show inline status and provide quick Tail logs.

Acceptance criteria
-------------------
- PoC image builds locally and, when run with recommended flags and a writable `/out` mount, writes `/out/kubeconfig` and the HostApp can attach it.
- UI supports providing an image name and streams job logs for image-based deploys.
- CI builds the image and runs a smoke test that starts the container and validates kubeconfig presence and basic API reachability.
