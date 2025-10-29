# GuildNet

GuildNet is a private self-hostable stack that puts human-in-the-loop with agent prompting on top of a knowledge gardening and coding version control foundation. For end users, it has simple interfaces that attempt to bring the cost to experiment down whilst bringing the capacity to experiment up, so non-engineers can have appropriate guardrails and higher level tools that upgrade them to collaborate fast, whilst allowing engineers and agents to ensure architectural integrity across time. Eventually it will have a DAG for tasks, prompting templates & infrastructure for robust agentic workflows, and much more.

## Key components

### Distributed private network cluster

- **Host App**: A local server that runs on all machines and exposes the UI to the network via tsnet as well as reverse-proxies traffic into Kubernetes.
- **Kubernetes Cluster**: Your existing real-node Kubernetes. Each cluster can have its own Tailnet and router.
- **Tailscale/Headscale**: Used for secure networking. Each cluster can use a per‑cluster embedded tsnet connector and an in-cluster subnet router.
- **UI**: A web interface for users to interact with the system, manage clusters, and access code servers.
- **Image Registry**: A private Docker image registry running within the cluster to store and manage container images.

## Services

- **Persistent Storage**: Provides persistent storage for code-server instances, ensuring data is retained across restarts.
- **Event Bus**: Facilitates communication between servers and hosts, enabling scheduling, notifications and updates.
- **Scheduler**: Distributes workloads across available server instances based on current load and availability.
- **Load Balancer**: Manages incoming requests and routes them to the appropriate server instance.
- **Public Tunnel**: Exposes services to the internet securely, allowing remote access to code servers and the UI.

### Agent coding workflow

- **code-server**: Provides a web-based VSCode environment for coding and interaction.
- **Ollama**: Runs locally on the host machines to provide LLM capabilities for agents.
- **OpenAI Codex**: Used within code-server instances to assist with coding tasks.
- **Radicle**: A decentralized git hosting solution for managing code repositories within the cluster.
- **Knowledge Base**: A system for storing and managing knowledge, integrated with agent workflows.

## Quickstart

### Headscale
Start or use Headscale as your Tailnet controller and create a reusable pre-auth key (local helper: `make headscale-up` and `make headscale-bootstrap`).

Verification:

- Check Headscale container status: `make headscale-status`
- Confirm preauth key and env sync: `make headscale-bootstrap` (writes keys into `.env`) and `make env-sync-lan`

### Tailscale


Join devices to the Tailnet using the Headscale login server and pre-auth key; deploy the in-cluster Tailscale subnet router to advertise cluster CIDRs when needed (`make router-ensure CLUSTER=<id>`).

Verification:

- Ensure local tailscaled / router is running: `make router-daemon` or check status with `make router-status`
- Verify routes are approved and visible in Headscale: `make headscale-approve-routes`

### Deploy cluster


Create or point to a Kubernetes cluster. For local development we recommend `microk8s` (use `bash ./scripts/microk8s-setup.sh` which writes a kubeconfig to `$(GN_KUBECONFIG)`). After the cluster is ready install addons and RethinkDB with `make deploy-k8s-addons` and deploy the operator with `make deploy-operator`.

Verification:

- Verify Kubernetes API connectivity & nodes: `make diag-k8s`
- Verify deployed addons and operator reconciliation: `make verify-e2e`

### Launch Host App server


Build and run the Host App (one-time setup: `make setup`), then start it with `make run`; the UI is served at https://localhost:8090 (ensure TLS certs/SANs if accessed from other devices).

Verification:

- Check backend health endpoint: `make health` or `make db-health`
- Inspect TLS certificate presented by the server: `make tls-check-backend`

### Connect from another device


From any device on the Tailnet open the Host App URL (https://localhost:8090), use the cluster Settings to "Download join config" and transfer that `guildnet.<cluster>.config` to another Host App instance or POST it to `/bootstrap` to register the cluster.

Verification:

- Generate a join config locally (same format the UI emits): `make generate-join-config` (writes `guildnet.config` by default)
- Register a joiner by POSTing the config to the host app (server must be reachable): `curl -k -X POST https://<hostapp>:8090/bootstrap -H 'Content-Type: application/json' --data @guildnet.config`

### Participate in a cluster

Short flow (federated / multi-device):

- Device A (host): run the Host App and ensure Headscale + cluster/operator are set up (quick helpers: `make multi-device-host`).
- Device B (joiner): generate a join config (`make generate-join-config`) and either use the Host App UI "Download join config" on Device A or POST the generated `guildnet.config` to Device A's `/bootstrap` endpoint to register the device. There is also a helper target `make multi-device-joiner` for an automated joiner flow.

Notes:

- The Host App persists per-device capabilities and uses controller leader election so only one device reconciles operator actions at a time. After bootstrap the operator and Host App exchange state; to validate the multi-device flow use `make verify-federation-e2e` or the quick failover verifier `make verify-multi-device-failover`.
- For development, convenience targets: `make multi-device-host` (bootstrap host) and `make multi-device-joiner` (bootstrap joiner) automate the common steps.

## Troubleshooting (quick)

- UI not reachable on another device:
  - Ensure you joined the same Tailnet and the server’s tsnet listener is up (default :443).
  - Ensure the certificate includes the tailnet hostname/IP or accept the self-signed cert for dev.
- Cluster services not reachable:
  - Verify a subnet router is advertising the cluster CIDRs and that routes are approved in Headscale.
- Kubernetes access errors:
  - Confirm `~/.guildnet/kubeconfig` points to the intended cluster and your RBAC permits operations.

## Useful commands

- `make help` – show available targets
- `make headscale-namespace` – create per‑cluster Headscale namespace and emit keys JSON
- `make router-ensure` – deploy per‑cluster Tailscale router DS (reads the keys JSON)
- `make verify-e2e` – end-to-end checks: headscale reachability, router DS readiness, kube API
- `make clean` – remove build artifacts
- `make stop-all` – delete managed workloads via the Server API

## Contributing / Extending

Focus areas that add immediate value:

- Add env + ports mapping from JobSpec into Workspace spec.
- Improve pod selection & multi-replica log aggregation.
- Introduce basic Prometheus metrics (proxy request count, workspace phase gauge).
- Harden error responses with structured JSON codes.

# Progress

- [x] Join/create Headscale/Tailscale network
- [x] Create dashboard server to run scripts and report status
- [x] Create UI for dashboard server to join/create network, manage clusters and observe code servers
- [x] Ensure multi-user support with orgs/clusters
- [ ] Proper per-user TLS certs for tailnet access
- [ ] Fully generic and configurable docker deploys via subdomain on tailnet
- [ ] Run Ollama on host machine and OpenAI Codex inside code servers, opening terminal to interact with agent via web UI
- [ ] Event bus for agent-host communication (e.g. notify users of PR created, code pushed, etc) with web UI
- [ ] Add persistent storage to cluster via Longhorn, save code server data there
- [ ] Add Radicle for git hosting inside cluster, hook up to agent workflow for PR creation
- [ ] Create UI for code review and PR management
- [ ] Prompt engineering for agent workflows, provide templates and examples
- [ ] Add MCPs for agent integration/interaction/memory/thinking etc
- [ ] Add Obsidian or similar for personal and collective knowledge management inside cluster
- [ ] Add task management system inside cluster, hook up to agent workflows, with 2D/3D graphical interface
