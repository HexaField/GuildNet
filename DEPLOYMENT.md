GuildNet — Production Deployment Guide

IMPORTANT NOTE: Local code generation (controller-gen)

This repository previously relied on an automated CI job to run `controller-gen` and commit generated artifacts. The CI workflow has been removed from this branch. Developers should run code generation locally when making API or type changes.

To generate deepcopy files and CRDs locally, ensure you have Go installed and then run:

```bash
make gen
```

The `make gen` target will attempt to install `controller-gen` v0.12.0 into your `GOPATH` if it is not already available. Make sure `$(go env GOPATH)/bin` is on your PATH so `controller-gen` can be invoked.

After running `make gen`, inspect `config/crd/bases/` and any `zz_generated.deepcopy.go` files for changes and commit them alongside your code changes.

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

Quick health probe:

```bash
make health
```

Run the repository end-to-end verifier (this sequence exercises operator reconciliation and proxying):

```bash
make verify-e2e
```

Manual create-and-check (if you prefer explicit API checks):

```bash
curl -k -X POST "https://127.0.0.1:8090/api/jobs" -H 'Content-Type: application/json' -d '{"image":"codercom/code-server:4.90.3","name":"verify-e2e"}'
kubectl get workspaces -A
kubectl -n <workspace-namespace> get deploy,svc -l guildnet.io/workspace=verify-e2e
```

Check Host App reverse proxy can reach the Workspace via the API or use the `make verify-e2e` helper which captures probe outputs.

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
