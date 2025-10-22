GuildNet — Production Deployment Guide

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

Goals

- Deploy the operator in-cluster (no embedded operator in production).
- Ensure durable DB storage and operator RBAC.
- Run Host App instances as long-lived services with proper TLS and secrets.
- Provide verification commands and troubleshooting tips.

Prerequisites

- kubectl configured and authenticated to your target cluster.
- A place to run Host App instances (hosts, VMs, or containers) with access to the cluster API or per-cluster kubeconfigs.
- TLS certificates for Host App endpoints (CA-signed or your organization's PKI).
- A secure `GUILDNET_MASTER_KEY` for Host App secrets encryption.

1) Install CRDs, DB, and deploy the operator (single Makefile flow)

This repository provides Makefile targets that bundle the recommended production install steps. Use these to keep the process simple and repeatable.

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
Import the operator image into microk8s prior to running the deploy script.

Verify operator status with kubectl (quick checks):

```bash
kubectl -n guildnet-system get deploy,pods -l app=guildnet-operator
kubectl -n guildnet-system logs -l app=guildnet-operator --tail=200
```

Troubleshooting: If the operator logs show RBAC or permission errors, review the manifests created by `scripts/deploy-operator.sh` and ensure the ServiceAccount and ClusterRoleBindings are applied and approved by your cluster admin.

Local image import runbook
--------------------------------------------------

If you cannot push to a container registry from your environment, use the local-import flow:

1) Build a linux/amd64 operator image on a machine that can run Docker (or BuildKit):

```bash
docker build --platform=linux/amd64 -f scripts/Dockerfile.operator -t registry.local/guildnet/hostapp:local-amd64 .
```

2) Export the image and copy it to your microk8s host (or run locally there):

```bash
docker save -o /tmp/op-amd64.tar registry.local/guildnet/hostapp:local-amd64
scp /tmp/op-amd64.tar user@microk8s-host:/tmp/
```

3) Import into microk8s containerd and confirm digest:

```bash
sudo microk8s ctr images import /tmp/op-amd64.tar
sudo microk8s ctr images ls | grep guildnet/hostapp
```

4) Patch the operator Deployment to use the imported image tag (or digest) and set imagePullPolicy to IfNotPresent or Never to avoid kubelet attempting to pull from external registries:

```bash
sudo microk8s kubectl -n guildnet-system set image deployment/workspace-operator operator=registry.local/guildnet/hostapp:local-amd64
sudo microk8s kubectl -n guildnet-system patch deployment workspace-operator -p '{"spec":{"template":{"spec":{"containers":[{"name":"operator","imagePullPolicy":"IfNotPresent"}]}}}}'
sudo microk8s kubectl -n guildnet-system rollout restart deployment workspace-operator
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
- Deploys a small test deployment (`verify-sample`) to each cluster and verifies the same image is running on both.

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
curl -k -X POST "https://<hostapp-host>:8090/bootstrap" -F "file=@guildnet.config"
```

The Host App will persist the kubeconfig and perform a bounded pre-warm check and will roll back on failure.

6) Configure per-cluster proxy settings (only if required)

In production you generally do NOT use a local `kubectl proxy`. If you must, explicitly set per-cluster `APIProxyURL` or set `KUBE_PROXY_ADDR` on the Host App host. Auto-detection is disabled in production.


7) Verify basic flow (easy Makefile shortcuts)

Operational note (2025-10-21):
- During recent local testing the operator image was rebuilt and imported into microk8s with the tag `guildnet/hostapp:local` and the operator Deployment was patched to use that image. Several CRDs in `config/crd/bases/` were applied to the test cluster to ensure all reconcilers are available (federatedclusters, federatedservices, sitestatuses, workspaces, capabilities).

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
make reset MAKE_RESET_CONFIRM=1
```

This target is destructive for local state files (by default it removes `~/.guildnet` and the `GN_KUBECONFIG` file). It attempts best-effort cleanup of Docker Headscale and in-cluster subnet router, and deletes test-like clusters using the Host App API. Some remote resources may remain and require manual cleanup.

Run the repository end-to-end verifier (this sequence exercises operator reconciliation and proxying):

```bash
make verify-e2e
```

Note: `scripts/verify-e2e.sh` now uses a headscale-compatible check (`headscale nodes list`) and follows redirects when probing HostApp proxy endpoints; this makes the verifier robust across Headscale CLI versions and proxied responses.

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
- Device A: start Headscale, bring up tailscale router, provision microk8s, apply CRDs/addons, deploy operator, start Host App, and emit a `guildnet.config` join file.
- Device B: join tailscale, provision microk8s, apply CRDs/addons, deploy operator, generate `guildnet.config`, and POST it to Device A’s Host App `/bootstrap`.

Diagnostics and verification:

```bash
make diag-multi-device
make verify-multi-device-failover
```

Connecting multiple devices to the same cluster (explicit steps)

This section shows the manual sequence to attach multiple Host App instances (devices) to the same Kubernetes cluster and to share published services. The `make multi-device-*` targets automate this, but doing the steps manually helps when debugging or customizing the flow.

Prerequisites
- A target Kubernetes cluster with a kubeconfig accessible from at least one device.
- On each device: Host App (binary or built from repo), `kubectl`, and optionally `microk8s` for single-node testing.
- Optional but recommended: tailscale/headscale so devices can reach each other over a secure tailnet.

Steps (manual)
1) Prepare the kubeconfig on the primary device (Device A). For microk8s:

```bash
sudo microk8s status --wait-ready
mkdir -p ~/.guildnet
microk8s config > ~/.guildnet/kubeconfig
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

4) From Device B (the joiner), POST the join artifact to Device A's `/bootstrap` endpoint:

```bash
curl -k -X POST "https://<deviceA-host-or-tailnet-ip>:8090/bootstrap" -F "file=@guildnet.config"
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

Troubleshooting tips
- If scripts fail with "set: Illegal option -o pipefail" or `syntax` errors, make sure you run them under `bash` (not `/bin/sh`). The orchestrator now invokes remote helpers with `bash`.
- If `docker buildx --load` is missing on a host, `scripts/agent-build-load.sh` falls back to `docker build`.
- When importing images to microk8s use `sudo microk8s ctr images import /tmp/image.tar` and set `imagePullPolicy: IfNotPresent` on operator Deployment to prefer local images.
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
