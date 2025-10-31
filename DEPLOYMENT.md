Notes:
- `scripts/verify-e2e.sh` uses a headscale-compatible check (`headscale nodes list`) and follows redirects when probing HostApp proxy endpoints; this makes the verifier robust across Headscale CLI versions and proxied responses.
- Tailscale is required for production and for the repository verifier. Ensure you have a running Headscale or Tailscale control plane and provide `TS_AUTHKEY` (and `TS_LOGIN_SERVER` for Headscale) before running `make verify-e2e`.
- The Tailscale router deploy script normalizes raw-hex preauth keys into canonical `tskey-...` form so `tailscale up` succeeds even when older Headscale CLIs print hex values.
- The MetalLB installer tolerates early webhook startup by temporarily setting failurePolicy=Ignore on the validating webhook and retrying applies; this avoids transient admission failures on fresh k0s clusters.
- The MetalLB installer now also ensures the required `memberlist` Secret exists in `metallb-system` (auto-generates a random key); without it, the `speaker` pods stay in ContainerCreating with `secret "memberlist" not found`.
GuildNet — Production Deployment Guide
Containerized node (Docker-only, k0s)
-------------------------------------

GuildNet now uses a Docker-only runtime using k0s inside a privileged container per device, plus optional Tailscale and Docker-in-Docker for image builds. This path is the default for local and production-style setups.

Quick path (one-liners):

```bash
# Bring up node stack and emit kubeconfig to ~/.guildnet/kubeconfig
scripts/k0s-node-up.sh

# Attach the emitted kubeconfig to the Host App via /api/bootstrap
scripts/attach-local-k0s.sh

# Deploy cluster addons, CRDs, DB, and operator (as before)
make deploy-k8s-addons
make deploy-operator

# Optional: verify node, CRDs/operator, and a test workspace
make verify-k0s
make verify-operator
make smoke-workspace
```

Notes:
- The k0s API is bound locally to 127.0.0.1:16443 by default. Tailnet exposure is layered via the Tailscale container and routing in follow-ups.
- The node stack also starts a DinD container for local image builds and exposes it on localhost (2375 without TLS, 2376 with TLS). A helper env file is written at `~/.guildnet/dind-env.sh`; `source` it to point your Docker client at DinD when needed.
- The `setup-all` target provisions the Docker-only path.
- Kernel modules: the k0s container mounts the host `/lib/modules` read-only to ensure kube-proxy/kube-router and CNI components can load required iptables/nftables kernel modules. This is necessary for stable networking in a containerized control-plane.

Image pipeline smoke (no registry)
----------------------------------
To quickly validate that local image builds run inside the cluster without setting up a registry, use the built-in smoke that builds in DinD, imports into the k0s node's containerd, and deploys a Workspace that uses the image:

```bash
make smoke-image-pipeline
```

What it does:
- Builds a tiny BusyBox+httpd image inside the `guildnet-dind` container and tags it `gn/smoke-app:local`.
- Streams the image tar into the `guildnet-k0s` container and imports it into containerd (`ctr -n k8s.io images import`).
- Creates a `Workspace` CR pointing at `gn/smoke-app:local` (non-latest tag -> imagePullPolicy=IfNotPresent) so the node uses the locally imported image without a pull.

You can override defaults via env vars:
- `GN_WORKSPACE_NS`, `GN_WORKSPACE_NAME`, `GN_WORKSPACE_PORT`
- `GN_SMOKE_IMAGE` (tag built/imported and used for the Workspace)

Tailnet exposure of kube-API (optional)
--------------------------------------
To expose the kube-API privately over the tailnet, configure Tailscale "serve tcp" from the tailscale container (host network mode is assumed):

```bash
make ts-serve-kubeapi
```

Alternatively, you can have `scripts/k0s-node-up.sh` configure this automatically by setting:

```bash
TS_AUTHKEY=tskey-... TS_LOGIN_SERVER=http://<headscale>:8081 TS_SERVE_KUBEAPI=1 scripts/k0s-node-up.sh
```
This will start the `guildnet-tailscale` container, advertise Pod/Service CIDRs (`$K0S_POD_CIDR,$K0S_SVC_CIDR`), and run `tailscale serve tcp` to forward the local kube-API port to the same tailnet port. Tailscale must be configured on each device to participate in federation and to pass the verifier.

TLS SANs for remote kubectl
---------------------------
When accessing the kube-API over tailnet by IP, your client will verify the server certificate against that IP. You can instruct `k0s-node-up.sh` to include the tailscale IP in the API certificate SANs by setting:

```bash
TS_ADD_SANS=1 TS_AUTHKEY=tskey-... TS_LOGIN_SERVER=http://<headscale>:8081 scripts/k0s-node-up.sh
```

This generates a minimal k0s config (`~/.guildnet/k0s/k0s.yaml`) with api.sans including `127.0.0.1`, `localhost`, your hostnames, and the detected tailscale IPv4. The controller is launched with `--config` to use these SANs. Combine with `TS_SERVE_KUBEAPI=1` if you want the port served over tailnet automatically.

DinD TLS and registry push (optional)
-------------------------------------
By default the DinD daemon listens without TLS on 2375, intended for local development within the same host. To enable TLS and a standard 2376 endpoint, set:

```bash
DIND_TLS=1 scripts/k0s-node-up.sh
```

This mounts a cert directory at `~/.guildnet/k0s/dind-certs` and points `DOCKER_TLS_CERTDIR` there. A helper env file is written at `~/.guildnet/dind-env.sh`; source it to configure your Docker client to talk to DinD over TLS:

```bash
source ~/.guildnet/dind-env.sh
docker version
```

For pushing to an in-cluster registry, ensure `make deploy-k8s-addons` created an image pull secret (or configure `K8S_IMAGE_PULL_SECRET`), tag your images with the registry host (LoadBalancer IP/DNS), and push from the DinD client. The “image pipeline smoke” avoids a registry by importing directly into the k0s containerd and remains available as a fast path.

Helper: push from DinD to any registry
--------------------------------------
Use the convenience script to push images that exist inside the DinD daemon to a remote registry (GHCR, Docker Hub, or a private registry):

```bash
# Point your docker client at DinD (optional; the script will also source this)
source ~/.guildnet/dind-env.sh

# Push a local tag built in DinD to GHCR
REGISTRY_USER=<gh-username> REGISTRY_PASS=$GITHUB_TOKEN \
	scripts/dind-registry-push.sh --src gn/smoke-app:local --dest ghcr.io/<user>/gn-smoke:local

# Makefile wrapper (same effect)
make dind-image-push SRC=gn/smoke-app:local DEST=ghcr.io/<user>/gn-smoke:local REGISTRY_USER=<user> REGISTRY_PASS=$GITHUB_TOKEN
```

If you cannot or prefer not to use a registry, continue to use the local import flow:

```bash
make smoke-image-pipeline
```


IMPORTANT NOTE: Local code generation (controller-gen)

This repository previously relied on an automated CI job to run `controller-gen` and commit generated artifacts. The CI workflow has been removed from this branch. Developers should run code generation locally when making API or type changes.

To generate deepcopy files and CRDs locally, ensure you have Go installed and then run:

```bash
make gen
```

The `make gen` target will attempt to install `controller-gen` v0.15.0 into your `GOPATH` if it is not already available. Make sure `$(go env GOPATH)/bin` is on your PATH so `controller-gen` can be invoked.

After running `make gen`, inspect `config/crd/bases/` and any `zz_generated.deepcopy.go` files for changes and commit them alongside your code changes.

Note on recent dependency updates (2025-10-19):
- A small set of safe, patch-level updates were applied to direct modules to pick up bug fixes: `gopkg.in/rethinkdb/rethinkdb-go.v6` -> v6.2.2, `modernc.org/sqlite` -> v1.21.2, `nhooyr.io/websocket` -> v1.8.17. These updates were validated with `go test ./...`.
- Avoid upgrading `golang.org/x/exp` (and similar modules) which require Go >= 1.24 while this repository targets Go 1.23. If you want to move to Go 1.24, run the broader module update in CI or a separate branch.

This document describes a production-first deployment flow for GuildNet: how to install CRDs and the in-cluster operator, bring up durable RethinkDB, deploy Host App instances, and perform basic verification and hardening.

Proxy verification tip
----------------------
When validating the reverse proxy endpoint, force HTTP/1.1 locally and follow redirects. Many UI images (like code-server) return a 302 redirect to `./login` at the base path.

Examples:
```bash
# Expect 302 with Location: ./login
curl -k --http1.1 -sS -D - "https://127.0.0.1:8090/api/cluster/<clusterID>/proxy/server/<service>/"

# Follow to 200 and store HTML
curl -k --http1.1 -sS -L -D - "https://127.0.0.1:8090/api/cluster/<clusterID>/proxy/server/<service>/" -o /tmp/proxy.html
```

Goals

- Deploy the operator in-cluster (no embedded operator in production).
- Ensure durable DB storage and operator RBAC.
- Run Host App instances as long-lived services with proper TLS and secrets.
- Provide verification commands and troubleshooting tips.

Headscale/tsnet versions and preauth keys (compatibility)
--------------------------------------------------------
- The repository targets Headscale v0.27.0 and Tailscale/tsnet v1.90.x.
- Preauth keys can be provided to the Host App or operator in either canonical `tskey-...` form or as raw hex bytes. Keys are persisted canonically as `tskey-...` but are resolved to raw hex at runtime before starting tsnet. This matches Headscale expectations and avoids interactive login flows.
- There is no in-process fallback that deletes `tailscaled.state` or tries multiple encodings. If Headscale logs show "AuthKey not found", ensure the preauth exists and that the value you provided matches the exact key bytes Headscale issued.
- For local Headscale on the same host, the connector may rewrite the control URL host to `127.0.0.1:<port>` when loopback is listening; this avoids hairpin failures.

Prerequisites

- kubectl configured and authenticated to your target cluster.
- A place to run Host App instances (hosts, VMs, or containers) with access to the cluster API or per-cluster kubeconfigs.
- TLS certificates for Host App endpoints (CA-signed or your organization's PKI).
- A secure `GUILDNET_MASTER_KEY` for Host App secrets encryption.

1) Install CRDs, DB, and deploy the operator (single Makefile flow)

This repository provides Makefile targets that bundle the recommended production install steps. Use these to keep the process simple and repeatable. When running the Docker-only node path, ensure `scripts/k0s-node-up.sh` has emitted a valid kubeconfig first (default `~/.guildnet/kubeconfig`).

Install cluster addons, CRDs and DB (RethinkDB):

```bash
make deploy-k8s-addons
```

This target applies MetalLB (if needed), applies CRDs, creates any image pull secret, and provisions the RethinkDB template.

Build and deploy the operator:

```bash
make deploy-operator
```

This will build or ensure the operator image is available to your cluster and then run `./scripts/deploy-operator.sh` to apply the operator manifests to the cluster.
Note: Prefer pushing to a registry or importing into k0s containerd from within the k0s container.

Verify operator status with kubectl (quick checks):

```bash
kubectl -n guildnet-system get deploy,pods -l app=guildnet-operator
kubectl -n guildnet-system logs -l app=guildnet-operator --tail=200
```

Troubleshooting: If the operator logs show RBAC or permission errors, review the manifests created by `scripts/deploy-operator.sh` and ensure the ServiceAccount and ClusterRoleBindings are applied and approved by your cluster admin.

Running the Host App (service) and signals
-----------------------------------------

Start the Host App using the provided script or editor task so it stays supervised and logs are tailed:

- Script: `scripts/run-hostapp.sh` (idempotent; stops any existing instance on the bound port and starts a new one)
- VS Code task: “Run server and tail logs” (uses the same script and tails `/tmp/hostapp.log`)

Signal handling:
- The Host App shuts down gracefully on SIGINT/SIGTERM only. SIGHUP/QUIT are ignored to avoid accidental exits.
- On Linux, the process requests a parent-death signal (SIGTERM). If you start the Host App from a short-lived shell (e.g., a one-off command that exits immediately), the kernel may terminate the Host App when that shell exits. Use `scripts/run-hostapp.sh` or disable this behavior with `GN_DISABLE_PDEATHSIG=1` when launching.


Local image import runbook
--------------------------------------------------

If you cannot push to a container registry from your environment, use the local-import flow:

1) Build a linux/amd64 operator image on a machine that can run Docker (or BuildKit):

```bash
docker build --platform=linux/amd64 -f scripts/Dockerfile.operator -t registry.local/guildnet/hostapp:local-amd64 .
```

2) Export the image and copy it to your k0s host (or run locally there or inside DinD):

```bash
docker save -o /tmp/op-amd64.tar registry.local/guildnet/hostapp:local-amd64
scp /tmp/op-amd64.tar user@k0s-host:/tmp/
```

3) Import into k0s containerd and confirm digest (inside the k0s container):

```bash
ctr -n k8s.io images import /tmp/op-amd64.tar
ctr -n k8s.io images ls | grep guildnet/hostapp
```

4) Patch the operator Deployment to use the imported image tag (or digest) and set imagePullPolicy to IfNotPresent or Never to avoid kubelet attempting to pull from external registries:

```bash
kubectl -n guildnet-system set image deployment/workspace-operator operator=registry.local/guildnet/hostapp:local-amd64
kubectl -n guildnet-system patch deployment workspace-operator -p '{"spec":{"template":{"spec":{"containers":[{"name":"operator","imagePullPolicy":"IfNotPresent"}]}}}}'
kubectl -n guildnet-system rollout restart deployment workspace-operator
```

5) If the operator needs to manage other clusters, mount the control-plane kubeconfig into the operator Deployment and set the environment variable `GN_CONTROL_PLANE_KUBECONFIG` to the mounted path (e.g., `/etc/guildnet/kubeconfig`). The operator will load kubeconfig from this env var first.

Operator certs and test verification
-----------------------------------

During multi-device testing the operator expects TLS cert/key under `/root/.guildnet/state/certs`. For quick tests you can mount the repository `certs/` directory into the operator pod (as a ConfigMap) so the operator finds `server.crt` and `server.key`.

Additionally, for operator to start non-interactively it must be provided a small `operator-config` JSON containing tailscale/headscale hints. The operator will look for `/root/.guildnet/config.json` and expects the following minimal fields:

- `login_server` — URL of the headscale/tailscale control plane (e.g. `http://192.168.1.2:8081`).
- `auth_key` — a reusable preauth key so the operator can perform a non-interactive tsnet login.
- `hostname`, `listen_local`, `dial_timeout_ms` — sensible defaults are acceptable.

To make setup simple there's a helper script included:

- `scripts/k8s/ensure-operator-setup.sh`

What the script does (idempotent):

- Ensures a ConfigMap `operator-config` exists with `login_server` and `auth_key` (it will read `~/.guildnet/headscale/preauth-*.txt` locally if present).
- Creates or updates a `operator-certs` ConfigMap from the repository `certs/` directory (if present).
- Patches the `workspace-operator` Deployment to mount `operator-certs` and restarts the deployment.

Usage (on a machine that can talk to the remote cluster's kube-apiserver):

```bash
# either rely on local headscale-generated preauth (scripts/headscale-bootstrap.sh)
scripts/k8s/ensure-operator-setup.sh

# or provide explicit values:
TS_LOGIN_SERVER=http://192.168.1.2:8081 TS_AUTHKEY=<preauth-key> scripts/k8s/ensure-operator-setup.sh
```

After the script runs, monitor the operator pod logs and ensure tsnet transitions from NeedsLogin to a logged-in state. Once logged in the operator will enable CRD/operator features and federation will proceed.

The `scripts/verify-federation-e2e.sh` performs cross-host checks:

- Validates there is at least one common cluster id exposed by both HostApp instances.
- Spawns code-server workspaces from both devices and verifies both are running and discoverable.

E2E behavior notes (k0s-in-Docker, federated single- or multi-device):

- When the kube-API is bound to localhost inside a k0s container, the remote HostApp may be unable to query cluster state directly. The e2e detects this and will skip “remote perspective” visibility/log checks when the remote server list is empty, treating the local cluster view as source of truth.
- In true single-node clusters (<2 nodes), the e2e always skips remote-perspective checks.
- Placement checks are strict when there are 2+ schedulable nodes and scheduling is deterministic. If both workspaces land on the same node (e.g., due to missing labels/taints or scheduler constraints), the e2e will warn and proceed instead of failing, since the primary goal is cross-device visibility and runtime health.

RBAC: DeviceParticipant CRD

Ensure the ServiceAccount used by HostApp or the operator has permission to manage `deviceparticipants.guildnet.io`. Sample Role and ClusterRole manifests are included under `config/rbac/` to grant minimal verbs for create/update/status operations.

DeviceParticipant CRD
---------------------
The Host App expects the `DeviceParticipant` CustomResourceDefinition to exist in the cluster so it can create per-device presence records in-cluster. If the CRD is missing the Host App will store pending deviceparticipant entries locally and repeatedly attempt to create them; logs will show `create deviceparticipant failed: the server could not find the requested resource` until the CRD is applied.

Apply the CRD manifest at `config/crd/bases/guildnet.io_deviceparticipants.yaml` to enable in-cluster device presence records.

Notes:
- Using digested image references may still make kubelet attempt a registry pull; prefer a local tag + imagePullPolicy=IfNotPresent or Never when importing images locally.
- If you need to re-import, repeat steps 1-3, then set deployment image to the new digest or tag and rollout restart.

Operator image / nginx notes
----------------------------
The operator prefers non-root container images for web workloads. When a Workspace image appears to be an `nginx` image, the operator will prefer an unprivileged variant (configurable via env var `WORKSPACE_NGINX_UNPRIVILEGED_IMAGE`) to avoid requiring root privileges in the container. The default value is `nginxinc/nginx-unprivileged:1.25`.

2) Durable DB (RethinkDB)

The `make deploy-k8s-addons` step includes provisioning steps for RethinkDB (see `k8s/rethinkdb.yaml`). If you prefer to apply the DB manifest separately, you can still do so, but the Makefile target is the simplest path.

To check DB status:

```bash
kubectl -n rethinkdb get sts,pvc,pods
```

Ensure PVCs are Bound and pods become Running before continuing.

3) Provision TLS & secrets

- Place production TLS certs on each Host App host at `./certs/server.crt` and `./certs/server.key` or mount them into containers.
- Set `GUILDNET_MASTER_KEY` on each Host App host (securely). Example generation (store securely):

```bash
head -c 32 /dev/urandom | base64
```

4) Host App: simple make-driven paths

For local or single-host deployment (one-off/manual start), the Makefile provides a convenience target:

```bash
# Build and run Host App locally (runs the `run` flow)
make deploy-hostapp
```

`make deploy-hostapp` delegates to the `run` target, which builds the binary and executes `./scripts/run-hostapp.sh`. This is a convenience for operators and for testing, but for production you typically run Host App as a managed service (systemd, container, etc.).

If you want to run Host App as a systemd service on a host, create a unit (example below) and start it. This is outside the Makefile (intended for long-lived hosts):

```
[Unit]
Description=GuildNet HostApp
After=network.target

[Service]
Type=simple
WorkingDirectory=/opt/guildnet
ExecStart=/opt/guildnet/bin/hostapp serve
Restart=on-failure
RestartSec=5
Environment=GUILDNET_MASTER_KEY=<your-master-key>
# Do NOT set GN_EMBED_OPERATOR in production
User=guildnet
Group=guildnet

[Install]
WantedBy=multi-user.target
```

Enable & start (systemd-managed hosts):

```bash
sudo systemctl daemon-reload
sudo systemctl enable guildnet-hostapp
sudo systemctl start guildnet-hostapp
sudo journalctl -u guildnet-hostapp -f
```

5) Register / attach clusters (bootstrap)

Create a join file or provide kubeconfig and call the Host App bootstrap endpoint. You can still generate a join artifact with the helper script and then POST it to the Host App instance.

Generate join artifact (example):

```bash
bash scripts/generate_join_config.sh --kubeconfig /path/to/kubeconfig --out guildnet.config
```

Attach via API (same flow):

```bash
curl -k -X POST "https://<hostapp-host>:8090/api/bootstrap" -F "file=@guildnet.config"
```

The Host App will persist the kubeconfig and perform a bounded pre-warm check and will roll back on failure.

6) Configure per-cluster proxy settings (only if required)

In production you generally do NOT use a local `kubectl proxy`. If you must, explicitly set per-cluster `APIProxyURL` or set `KUBE_PROXY_ADDR` on the Host App host. Auto-detection is disabled in production.


7) Verify basic flow (easy Makefile shortcuts)

Operational note (2025-10-21):
- During recent local testing the operator image was rebuilt and loaded into k0s (or pulled from registry) with the tag `guildnet/hostapp:local` and the operator Deployment was patched to use that image. Several CRDs in `config/crd/bases/` were applied to the test cluster to ensure all reconcilers are available (federatedclusters, federatedservices, sitestatuses, workspaces, capabilities).

If you follow the local image import flow, remember to set imagePullPolicy to `IfNotPresent` or `Never` for local tags and perform a `kubectl -n guildnet-system rollout restart deployment workspace-operator` after updating the image.

Quick health probe:

```bash
make health
```

Full teardown / reset
---------------------

For local test environments there is a guarded convenience target that performs a full teardown of locally managed artifacts (Headscale container, Tailscale router, Host App process, temporary cluster records, and local GN state):

```bash
# Requires explicit confirmation to avoid accidental data loss
make reset MAKE_RESET_CONFIRM=1 [KEEP_K0S=1]
```

This target is destructive for local state files (by default it removes `~/.guildnet` and the `GN_KUBECONFIG` file). Set `KEEP_K0S=1` to preserve containerized k0s state under `~/.guildnet/k0s` and the emitted kubeconfig while other state is cleaned. The target attempts best-effort cleanup of Docker Headscale and in-cluster subnet router, and deletes test-like clusters using the Host App API. Some remote resources may remain and require manual cleanup.

Run the repository end-to-end verifier (this sequence exercises operator reconciliation and proxying):

```bash
make verify-e2e
```

Notes:
- `scripts/verify-e2e.sh` uses a headscale-compatible check (`headscale nodes list`) and follows redirects when probing HostApp proxy endpoints; this makes the verifier robust across Headscale CLI versions and proxied responses.
- Tailscale is optional. If you have not provided `TS_AUTHKEY`, the verifier will skip the Tailscale subnet-router readiness check rather than fail the run. Provide `TS_AUTHKEY` (and `TS_LOGIN_SERVER` if using Headscale) to enable full tailnet checks.

Storage and tailnet verification
--------------------------------

Quick checks are available to validate storage provisioning and tailnet kube-API exposure:

```bash
# Verify default StorageClass and RethinkDB PVC readiness
make verify-storage

# Verify kube-API is reachable over Tailnet and, if SANs were injected, cert includes tailnet IP
make verify-tailnet-kubeapi
```

If you need to reproduce older controller-gen crashes seen during generator debugging, run `make gen` with different `controller-gen` versions in a hermetic environment.

Manual create-and-check (if you prefer explicit API checks):

```bash
curl -k -X POST "https://127.0.0.1:8090/api/jobs" -H 'Content-Type: application/json' -d '{"image":"codercom/code-server:4.90.3","name":"verify-e2e"}'
kubectl get workspaces -A
kubectl -n <workspace-namespace> get deploy,svc -l guildnet.io/workspace=verify-e2e
```

Check Host App reverse proxy can reach the Workspace via the API or use the `make verify-e2e` helper which captures probe outputs.

Multi-device quickstart (automation)

For a streamlined multi-device setup (Device A host + Device B joiner):

On Device A (host):

```bash
export GUILDNET_MASTER_KEY="$(head -c 32 /dev/urandom | base64)"
export LISTEN_LOCAL="0.0.0.0:8090"
make multi-device-host
```

On Device B (joiner):

```bash
export GUILDNET_MASTER_KEY="$(head -c 32 /dev/urandom | base64)"
export HOSTAPP_URL="https://<deviceA-tailnet-ip>:8090"

If you need to run the repository-built Host App on another local port (for example when a system service already listens on 8090), set `LISTEN_LOCAL` and point the heartbeat poster to it using `GN_HEARTBEAT_URL`:

```bash
export LISTEN_LOCAL=127.0.0.1:18090
export GN_HEARTBEAT_URL="https://127.0.0.1:18090/v1/sites/heartbeat"
./bin/hostapp serve
```
make multi-device-joiner
```

This will:
- Device A: start Headscale, bring up tailscale router, provision k0s-in-Docker, apply CRDs/addons, deploy operator, start Host App, and emit a `guildnet.config` join file.
- Device B: join tailscale, provision k0s-in-Docker, apply CRDs/addons, deploy operator, generate `guildnet.config`, and POST it to Device A’s Host App `/bootstrap`.

Diagnostics and verification:

```bash
make diag-multi-device
make verify-multi-device-failover

Tailscale/Headscale quick verification
--------------------------------------
- After starting the Host App, tail logs and look for `tsnet[<cluster-id>]:` entries indicating a successful tsnet start and status containing a 100.x IP or Self.DNSName.
- If Headscale is reachable and the preauth key is valid, the connector should join non-interactively and the device will appear in Headscale with an assigned IP. The Host App also verifies the device via the Headscale admin API and persists machine ID and IPs into the per-cluster DB.
- If you see `AuthKey not found` in Headscale logs, regenerate a preauth key and update the per-cluster settings (or `~/.guildnet/config.json` for the operator). No fallback restarts occur automatically; fix the configuration and restart the Host App or operator.
```

Multi-device E2E verification (scripted)
---------------------------------------
Use the included script to verify that two devices reference the same deterministic cluster ID, can each spawn a code-server workspace, can see both servers, and can read logs from both. The script also validates placement (each device’s workspace runs on its own node).

Prerequisites:
- Host App running on both devices at https://127.0.0.1:8090 (self-signed TLS is OK).
- SSH access to the remote device without interactive prompts (e.g., key auth).
- curl and jq installed on both devices.

Run from Device A:

```bash
# Replace with your remote user and host
REMOTE_SSH=user@192.168.0.1 \
VERBOSE=1 \
scripts/verify-federation-e2e.sh
```

What it does:
- Compares cluster IDs from both Host Apps and, if missing on the remote, attaches the local kubeconfig via the supported API.
- Spawns a code-server workspace from each device with scheduleNode set to the launcher’s hostname for deterministic placement.
- Waits for both to become running; verifies both devices list both servers; fetches logs for both workspaces from both devices.
- Fails fast with actionable messages if any step cannot be verified.


Connecting multiple devices to the same cluster (explicit steps)

This section shows the manual sequence to attach multiple Host App instances (devices) to the same Kubernetes cluster and to share published services. The `make multi-device-*` targets automate this, but doing the steps manually helps when debugging or customizing the flow.

Prerequisites
- A target Kubernetes cluster with a kubeconfig accessible from at least one device (use scripts/k0s-node-up.sh to provision local k0s and emit ~/.guildnet/kubeconfig).
- On each device: Host App (binary or built from repo), `kubectl`, and Docker for k0s-in-Docker runtime.
- Optional but recommended: tailscale/headscale so devices can reach each other over a secure tailnet.

Steps (manual)
1) Prepare the kubeconfig on the primary device (Device A). For k0s-in-Docker:

```bash
scripts/k0s-node-up.sh
export KUBECONFIG=~/.guildnet/kubeconfig
```

2) Start Host App on Device A. Ensure TLS certs and `GUILDNET_MASTER_KEY` are configured:

```bash
export GUILDNET_MASTER_KEY="$(head -c 32 /dev/urandom | base64)"
export LISTEN_LOCAL="0.0.0.0:8090"
./bin/hostapp serve &
# or for dev
./scripts/run-hostapp.sh
```

3) Produce a join artifact (`guildnet.config`) that contains a kubeconfig or connection hints. Generate it on the joiner (Device B) or Device A and transfer it:

```bash
bash scripts/generate_join_config.sh --kubeconfig /path/to/kubeconfig --out guildnet.config
```

4) From Device B (the joiner), POST the join artifact to Device A's `/api/bootstrap` endpoint:

```bash
curl -k -X POST "https://<deviceA-host-or-tailnet-ip>:8090/api/bootstrap" -F "file=@guildnet.config"
```

5) Verify on Device A the cluster is attached and the kubeconfig is stored in the Host App DB (or visible via `GET /api/deploy/clusters`):

```bash
curl -s "https://127.0.0.1:8090/api/deploy/clusters" | jq .
kubectl --kubeconfig=~/.guildnet/kubeconfig get nodes
```

6) Start Host App on Device B. If Device B cannot reach the cluster API directly, configure per-cluster `APIProxyURL` or rely on tailscale/tsnet connectors so Device B has a network path to the API server.

Device heartbeat (capabilities)
--------------------------------
Devices are the source-of-truth for local capabilities (CPU, RAM, storage, VRAM, tailnet IPs). Each device should POST a heartbeat to the Host App it is attached to so the server can persist device-reported capabilities. The Host App exposes a minimal endpoint:

POST /v1/sites/heartbeat

JSON body example:
```
{
	"clusterId": "<deterministic-cluster-id>",
	"id": "device-a",
	"name": "device-a.home",
	"tailnetIPs": ["100.101.102.103"],
	"cpuMilli": 2000,
	"memoryMB": 4096,
	"storageMB": 32768,
	"vramMB": 2048
}
```

The server stores this under the per-cluster localdb collection `devices`. Subsequent UI calls and the placement planner prefer these device-reported values when present.

Servers list enrichment
-----------------------
The UI lists servers per cluster by calling `GET /api/cluster/{id}/servers`, which returns a simplified view of `Workspace` CRs and enriches each entry with:

- `node`: the Kubernetes node hosting the workspace pod (resolved by listing pods with label `guildnet.io/workspace=<name>`).
- `machineName` and `tailnetIPs`: derived from `DeviceParticipant` CRs in the `guildnet-system` namespace when available.

This metadata helps operators quickly identify which machine a server is running on and how it can be reached over the tailnet.

Sites UI (multi-device)
-----------------------
- The Sites list (Federated services page) calls `GET /api/v1/sites`.
- The server marks the local Host App’s device record with `self: true` and sets `lastSeen` to null, so the UI can hide it when showing “remote” devices.

Stopping a server (UI)
----------------------
From the Servers list and the Server detail page, you can shut down a single server (Workspace) using the "Shutdown" button. This issues a cluster-scoped delete to the backend:

- DELETE /api/cluster/{id}/workspaces/{name}

Authorization follows the same rules as other mutating endpoints (loopback-or-token). On success, the UI refreshes the list or returns to the Servers page.

Per-device placement (same cluster)
-----------------------------------
When a device launches a workspace via `POST /api/cluster/{id}/workspaces`, the Host App adds a metadata label `guildnet.io/schedule-node=<launcher-hostname>`. The in-cluster operator applies this hint by setting a `nodeSelector` on the underlying Pod for `kubernetes.io/hostname`, which results in the Kubernetes scheduler placing the pod on the specified node.

Requirements:
- Each participating device must be joined as a node in the same Kubernetes cluster.
- Node names should match the device hostnames (default). Alternatively, label nodes appropriately and adapt the operator if your naming differs.
- If the specified node is not part of the cluster, Kubernetes will ignore the selector and placement will follow standard scheduling.

Troubleshooting tips
- If scripts fail with "set: Illegal option -o pipefail" or `syntax` errors, make sure you run them under `bash` (not `/bin/sh`). The orchestrator now invokes remote helpers with `bash`.
- If `docker buildx --load` is missing on a host, `scripts/agent-build-load.sh` falls back to `docker build`.
- When importing images to local k0s, use `docker exec guildnet-k0s ctr images import /tmp/image.tar` and set `imagePullPolicy: IfNotPresent` (or `Never` for `:local` tags) on the operator Deployment to prefer local images.
- To confirm cross-device service mirrors, check for ConfigMaps named `guildnet-system/published-<id>` in the cluster; these are the mirrored published service mappings devices use to resync.

Deterministic cluster identity and multi-device attach
-----------------------------------------------------
- Cluster IDs are computed deterministically from kubeconfig attributes (normalized server URL and CA data).
- On a secondary device that should reference the same cluster, POST the kubeconfig to the primary device using:

	`POST /api/deploy/clusters/{any}?action=attach-kubeconfig` with body `{ "kubeconfig": "..." }`.

	The server will compute the canonical cluster ID and, if a record doesn't exist yet, create one with state `imported`. This ensures UIs and agents on all devices refer to the same cluster ID.

DeviceParticipant name sanitization
-----------------------------------
When creating in-cluster `DeviceParticipant` resources, device identifiers are sanitized to valid Kubernetes resource names per RFC 1123 (lowercase, alphanumeric and '-', must start/end with alphanumeric, max length 253). This avoids failures when hostnames contain uppercase or invalid characters.

Security notes
- Use a unique `GUILDNET_MASTER_KEY` per Host App host and store it securely (do not commit to git).
- Prefer the in-cluster operator in production instead of `GN_EMBED_OPERATOR` to centralize reconciliation and reduce host-side complexity.


Operational notes:
- The embedded operator uses controller-runtime leader election (coordination.k8s.io/leases) so multiple devices can run safely; only the leader reconciles at a time.
- Published service mappings are mirrored into an in-cluster ConfigMap `guildnet-system/published-<id>` so devices can resync consistently after restarts.
- To auto-start Host App on reboot on a host, run:

```bash
bash scripts/install-hostapp-service.sh
```

8) Monitoring, logging and alerting

- Configure centralized logs (journald -> ELK/Fluentd) and metrics scraping.
- Ensure operator and Host App metrics are scraped and alert rules exist for PodCrashLoopBackoff, DiskPressure, and RethinkDB availability.

9) Security checklist

- TLS certs are CA-signed and rotated periodically.
- `GUILDNET_MASTER_KEY` stored in a secure secret manager and not checked into git.
- Do not use default passwords for code-server in production.
- Restrict access to Host App admin API endpoints.

Appendix: common `kubectl` checks

```bash
# CRDs
kubectl get crd workspaces.guildnet.io
# Operator
kubectl -n guildnet-system get deploy,svc,pods
# DB
kubectl -n rethinkdb get sts,pvc
# Check workspace reconciliation
kubectl -n <ns> get workspaces
kubectl -n <ns> describe workspace <name>
```


---
Created by automation. Edit as needed to match your production layout and secrets management.

Related design docs:
- ADR: `docs/adr/0001-multi-device-cluster.md`
- Implementation plan: `docs/implementation/0001-multi-device-cluster-implementation.md`
