# GuildNet API Reference

This document lists the Host App HTTP API endpoints, per-cluster services and endpoints, cluster infrastructure components, and configuration options for both the cluster and the Host App server.

Note: the runtime behavior is implemented in `internal/api/router.go`, `internal/settings/settings.go`, and `pkg/config/config.go`.

## Table of Contents

- Host App server API endpoints
- Per-cluster services & endpoints
- Cluster infrastructure & configured components
- Cluster configuration options (per-cluster settings)
- Host App configuration options (global and runtime)
- Examples and notes

Note: a targeted, safe set of patch updates to a few direct modules were applied on 2025-10-19 and validated. Major module bumps (that require Go 1.24+) were intentionally avoided.

Tailscale/Headscale authentication and tsnet connectors
------------------------------------------------------
- Per-cluster embedded tsnet connectors are used for Tailnet connectivity. Each cluster can provide its own Headscale/Tailscale login server and preauth key.
- Preauth key handling is deterministic:
  - Keys are stored canonically as `tskey-<base64url-no-padding>` when persisted.
  - At runtime, the connector resolves the provided value to the raw-hex form Headscale expects and passes that to tsnet. This removes ambiguity across encodings and ensures non-interactive login.
  - If a raw-hex value is provided directly, it is used as-is.
- Non-interactive login only. Interactive login URLs are not used by design.
- No restart/fallback logic: the connector does not delete `tailscaled.state` or attempt opaque restarts. Health is surfaced via status and Headscale lookups.
- The connector normalizes the configured login server and may rewrite to `127.0.0.1:<port>` when it detects the target host is bound on the local machine and loopback is listening on the same port; this avoids hairpin surprises for local Headscale.
- Device IPs and Headscale machine IDs are verified against Headscale and persisted into the per-cluster DB.


## Host App server API endpoints

The Host App exposes an HTTP API (default listen is configurable; see Host App configuration). The important endpoints implemented in `internal/api/router.go` are below.

Authorization model: GET requests are open. Mutating requests require either a configured bearer token (Host App `Deps.Token`) in the `Authorization: Bearer <token>` header or must originate from loopback (127.0.0.1 / ::1) when no token is set. Some endpoints also accept `X-API-Token` header.

- POST /bootstrap
  - Purpose: Accept a join payload (JSON or `guildnet.config`) and persist a cluster record and kubeconfig. Performs a bounded pre-warm (10s) to validate cluster API and RethinkDB (if Registry is present).
  - Request body (JSON):
    - tailscale: optional object matching `settings.Tailscale` (login_server, preauth_key, hostname)
    - cluster: optional object with fields:
      - kubeconfig (string) - required when attaching a cluster
      - name, namespace
      - api_proxy_url, api_proxy_force_http, disable_api_proxy
      - prefer_pod_proxy, use_port_forward
      - ingress_domain, ingress_class_name, workspace_tls_secret
      - cert_manager_issuer, ingress_auth_url, ingress_auth_signin
      - image_pull_secret, org_id
  - Response: JSON { id: <id> } on success when kubeconfig provided. (Clusters now expose a deterministic, canonical `id` field.)

  - Example (attach kubeconfig emitted by `scripts/k0s-node-up.sh` and then set cluster defaults):

    1) POST `/bootstrap` with body `{"cluster":{"kubeconfig":"<yaml>"}}` — response: `{ "id": "<deterministicId>" }`
    2) PUT `/api/settings/cluster/<deterministicId>` with a subset of fields, for example:

    ```json
    {
      "api_proxy_url": "http://127.0.0.1:8001",
      "api_proxy_force_http": true,
      "image_pull_secret": "regcred",
      "use_port_forward": false,
      "prefer_pod_proxy": false
    }
    ```

    The helper `scripts/attach-local-k0s.sh` automates this flow when invoked with `SET_DEFAULTS=1` and reads settings overrides from environment variables.

Multi-device automation: Use `make multi-device-host` on Device A and `make multi-device-joiner` on Device B to bootstrap quickly. The joiner will call this `/bootstrap` endpoint with its generated `guildnet.config`.

  Multi-device note: After successful bootstrap, the Host App mirrors its published service mappings into a shared ConfigMap in the cluster (`guildnet-system/published-<id>`). Other devices reading the same cluster will observe and resync state from this registry.

- GET/PUT /settings/tailscale
  - Get or update global tailscale/tsnet settings. Payload uses `settings.Tailscale`.

- GET/PUT /settings/database
  - Get or update database connection settings (not commonly used in production).

- GET/PUT /settings/global
  - Get or update runtime global settings (`settings.Global`).

- GET/PUT /api/settings/cluster/{clusterID}
  - Purpose: per-cluster settings (persisted into per-cluster DB when available).
  - GET returns `settings.Cluster` for the cluster.
  - PUT accepts `settings.Cluster` fields to update runtime behavior and will write a `ConfigMap` named `guildnet-cluster-settings` into the cluster namespace `guildnet-system` when cluster clients are available. This configmap is used by in-cluster controllers to read runtime preferences.

- GET /api/jobs
  - List submitted jobs (or orchestration tasks).
- POST /api/jobs
  - Submit a job: body { kind: string, spec: map } -> returns jobId (accepted).
- GET /api/jobs/{id}
  - Get job status.
- POST /api/jobs/{id}?action=cancel
  - Cancel job (requires authorization).
- GET /api/jobs-logs/{id}
  - Return NDJSON job logs from local DB.
- WS /ws/jobs?id={jobId}
  - Subscribe to job logs via WebSocket.

- GET /api/audit
  - List audit records (read-only).

- GET /api/health
  - Host-level health summary. Returns collected `headscale` entries and `clusters` status objects, performing lightweight cluster checks for each cluster known in the Host App DB.

- POST/GET/DELETE /api/deploy/headscale and /api/deploy/headscale/{id}
  - Create/manage in-host headscale deployment records and orchestrate creation via jobs. Supports sub-actions via POST `?action=endpoint|preauth-key|health`.

- GET/POST /api/deploy/clusters
  - GET: list clusters persisted in Host App DB.
  - POST: create a cluster record (orchestration job for provisioning).

- GET/DELETE/POST /api/deploy/clusters/{id}
  - GET: cluster record (from Host App DB).
  - DELETE: remove cluster record.
  - POST actions (query param `action`) include:
    - attach-kubeconfig: body { kubeconfig: string } (validates kubeconfig and persists it under `credentials:cl:{id}:kubeconfig`)
    - health: check cluster reachability
    - kubeconfig: returns the persisted kubeconfig as YAML
    - other actions delegated as `cluster.<action>` jobs

Deterministic cluster IDs and attach-kubeconfig behavior
-------------------------------------------------------
- Cluster IDs are deterministic: when a kubeconfig is provided (via POST /bootstrap or attach-kubeconfig), the backend computes a canonical ID from the kubeconfig's normalized server URL and certificate-authority data.
- POST `/api/deploy/clusters/{id}?action=attach-kubeconfig` may be invoked with any placeholder `{id}`. The backend will compute the deterministic ID and, if no record exists yet, create a cluster record with state `imported` so that UIs/agents can reference the same cluster across devices. The response includes `{ id: <deterministicId>, ok: true }` on success.

Docker-only k0s node note
-------------------------
- The helper script `scripts/k0s-node-up.sh` emits a kubeconfig (default `~/.guildnet/kubeconfig`) for the containerized k0s control-plane running on the same device. This kubeconfig can be posted to `/bootstrap` exactly like any other cluster.
- Initial defaults bind the API to `https://127.0.0.1:16443` (port auto-increments if busy). Tailnet-based access can be configured via Tailscale routing/serve (`TS_SERVE_KUBEAPI=1`) and the API cert may include the tailnet IP in SANs when `TS_ADD_SANS=1` is provided. The API surface and bootstrap semantics remain unchanged.

- GET /ui-config
  - UI runtime config placeholder (returns {} in current implementation).

- Cluster proxied APIs (per-cluster path prefix: /api/cluster/{clusterID}/...)
  - GET /api/cluster/{id}/published-services
    - List Host App published services (tailnet/tsnet published ports persisted in local DB).
  - DELETE /api/cluster/{id}/published-services/{service}
    - Remove a published service and stop the tsnet listener for it.
  - GET /api/cluster/{id}/status
    - Quick cluster-local status (internal helper).
  - Proxy endpoint: /api/cluster/{id}/proxy/server/{serviceName}/... -> reverse proxy to the Service (via API proxy path or port-forward fallbacks).
    - This endpoint performs service discovery (Service -> Pod selection) and supports port-forward fallback, tsnet publishing, and streamable websocket proxying.
  - GET /api/cluster/{id}/servers
    - List Workspaces (maps `Workspace` CRs to a simplified Server model: id, name, image, status, ports).
    - Now includes machine identity for each server when available.
- POST /api/cluster/{id}/workspaces
  - Creates a Workspace CR. On create, the Host App injects a metadata label `guildnet.io/schedule-node=<launcher-hostname>` to guide placement.
  - The in-cluster operator honors this hint by setting `podSpec.nodeSelector["kubernetes.io/hostname"]` to the provided value, ensuring the pod is scheduled on the target node.
  - If the target node does not exist in the cluster, normal Kubernetes scheduling applies and placement may differ.
  - Optional scheduling override from client:
    - Provide `scheduleNode: "<node-name>"` in the POST body to explicitly target a device (node). This will override the default launcher hostname.
    - Alternatively, include a label `guildnet.io/schedule-node` in `labels` (array of `{name,value}` or map) and it will be used as the scheduling hint.

    - Response shape (array):
      - id: string — workspace name
      - name: string — workspace name
      - image: string
      - status: 'pending' | 'running' | 'failed' | 'stopped'
      - ports: [{ name?: string, port: number }]
      - node?: string — Kubernetes node name hosting the pod
      - machineName?: string — device name (usually the hostname) derived from DeviceParticipant
      - tailnetIPs?: string[] — tailnet IPs/FQDNs for the hosting device
    - Implementation details:
      - The API lists Workspace CRs and, for each, resolves the node via the associated pod(s).
      - It cross-references DeviceParticipant CRs (guildnet-system namespace) to map node -> device name and tailnet IPs.
      - When DeviceParticipant data is unavailable, node is still returned and machine fields are omitted.
  - POST /api/cluster/{id}/workspaces
    - Create a Workspace CR in target cluster (body: workspace spec with image, env, ports, args, resources, labels). Returns { id, status } accepted if creation succeeded.
  - GET /api/cluster/{id}/workspaces/{name}
    - Fetch Workspace CR object (unstructured) from cluster.
  - GET /api/cluster/{id}/workspaces/{name}/logs
    - Aggregate pod logs for the workspace (returns list of log lines with timestamps).
  - DELETE /api/cluster/{id}/workspaces/{name}
    - Delete workspace CR (auth required for mutating). Used by the UI "Shutdown" action on the Servers list and Server detail pages.
  - GET /api/cluster/{id}/workspaces/{name}/logs/stream
    - SSE / Event-stream of pod logs (text/event-stream)
  - GET /api/cluster/{id}/health
    - Cluster scoped health: checks k8s connectivity and RethinkDB presence (using Registry.RDBPresent).

- Per-cluster DB API (proxied): /api/cluster/{id}/db/... -> internally rewrites to /api/db/... and routes to the Host App DB API implementation (see `internal/httpx.DBAPI`).

- SSE path for changefeeds: /sse/cluster/{id}/db/... -> rewritten to /sse/db/...


## Per-cluster services and endpoints

The Host App interacts with several services in each cluster. The operator reconciles `Workspace` CRs into Kubernetes resources (Deployments, Services, Ingresses, etc.). Key cluster services and their endpoints:

- Kubernetes API server
  - Used directly by Host App per-cluster clients (kubernetes.Clientset) and dynamic client for CRDs. Endpoint = kubeconfig's cluster server URL or per-cluster `APIProxyURL`.
  - When `APIProxyURL` is configured, the Host App will send API requests to that base URL (useful when the cluster API is fronted by an HTTP proxy or `kubectl proxy`).

- RethinkDB (per-cluster optional)
  - If a cluster includes an in-cluster RethinkDB for workspace-level state, the Host App attempts to locate it using Service LB IP, NodePort, or ClusterIP as configured. `Instance.EnsureRDB` performs the connection handshake.
  - The Host App exposes DB-management endpoints that operate on that DB via `/api/cluster/{id}/db/...`.

- Workspace Workloads (created by operator)
  - Deployments and Services created by the operator for each Workspace. Their service endpoints are typical k8s Service ClusterIP or LoadBalancer; the Host App proxies to them via:
    - /api/cluster/{id}/proxy/server/{serviceName}/... (service proxy)
    - If service endpoints are missing or preferPodProxy/usePortForward set, the Host App may port-forward to a pod and publish via tsnet.

- Ingress / LoadBalancer endpoints
  - If a Workspace is exposed via Service.type=LoadBalancer or an Ingress is created, the external ingress or LB IP is considered part of the cluster infra and will be used by clients and the Host App when present.


## Cluster infrastructure & configured components

These components are referenced in code and deployment manifests and are expected to be present or installed as part of `make deploy-k8s-addons` and `make deploy-operator` steps.

- CRDs
  - `workspaces.guildnet.io` (Workspace CRD)
  - `capabilities.guildnet.io` (Capabilities CRD)

- GuildNet Operator
  - Recommended: in-cluster Deployment in namespace `guildnet-system` (managed by `scripts/deploy-operator.sh` or `make deploy-operator`).
  - Functions: reconcile Workspace CRs into Deployments/Services, set Workspace.Status fields, create ConfigMap `guildnet-cluster-settings` for runtime settings.

- RethinkDB
  - Provided as `k8s/rethinkdb.yaml` for clusters that host RethinkDB for workspace persistence.
  - PersistentVolumeClaims must be bound for durability.

- MetalLB (optional for kind/local)
  - Used to provide LoadBalancer IPs for services in local/kind environments. Installed by `scripts/deploy-metallb.sh`.

- Cert-Manager / TLS
  - Clusters may use Cert-Manager to provision TLS for workspace ingress; Host App supports setting per-cluster `CertManagerIssuer` and `WorkspaceTLSSecret` settings.

- Network/Proxy components
  - Calico (or other CNI) — networking plugin; Host App is not dependent on a specific CNI, but debugging references Calico's IPAM issues.
  - Optional `kubectl proxy` / API proxy — Host App supports using a local kubectl proxy or explicit `APIProxyURL` to reach the API server.

- Headscale (optional)
  - Headscale can be orchestrated via Host App jobs to provide a private tailnet for cluster access. Headscale endpoints and preauth keys are stored in local DB and optionally used to configure tsnet connectors.

Local teardown helper (Makefile)
-------------------------------

For convenience during development there is a guarded Makefile target that performs a best-effort full teardown of local GuildNet artifacts (Headscale container, Tailscale router, Host App process, temporary cluster records, and local GN state):

```bash
# Requires explicit confirmation to run
make reset MAKE_RESET_CONFIRM=1
```

This is intended for local/dev workflows and is destructive to local state (it removes `~/.guildnet` and the `GN_KUBECONFIG` file by default). Use with care.


## Cluster configuration options (per-cluster settings)

Per-cluster settings are defined in `internal/settings/settings.go` (type `Cluster`) and persisted via `settings.Manager`.

- Name: human-friendly cluster label
- Namespace: default namespace for Workspace CRs (default `default`)
- APIProxyURL: optional base URL used instead of kubeconfig host (useful for kubectl-proxy or HTTP fronting)
- APIProxyForceHTTP: if true, force HTTP scheme when using APIProxyURL
- DisableAPIProxy: disable API proxy overrides for this cluster
- PreferPodProxy: prefer port-forward/pod proxying for service proxy endpoints
- UsePortForward: allow port-forward fallback when Service endpoints are missing
- IngressDomain: base domain used for creating Ingress resources for workspaces
- IngressClassName: ingress class to annotate ingresses (if creating Ingress)
- WorkspaceTLSSecret: name of TLS secret to use for workspace ingresses (if present)
- CertManagerIssuer: cert-manager issuer name to use for workspace TLS
- IngressAuthURL / IngressAuthSignin: optional OIDC/SSO hints used by the UI
- ImagePullSecret: optional imagePullSecret to attach to workspace pods
- WorkspaceLBEnabled: default to expose workspaces as LoadBalancer type (when true)
- OrgID: optional org scoping for multi-tenant configurations
- TSLoginServer / TSClientAuthKey / TSRoutes / TSStatePath / HeadscaleNS: per-cluster tailscale/headscale related settings for tsnet connectors

Notes on tsnet connector settings
- TSLoginServer: Headscale/Tailscale control URL (http[s]://host:port). The connector will probe `/key?v=1` with short timeouts before start and rewrite to loopback when safe and helpful.
- TSClientAuthKey: Preauth key. Accepted inputs: `tskey-...` or raw hex. Persisted canonically as `tskey-...`. Runtime is resolved to raw hex for compatibility with Headscale v0.27.0.
- TSStatePath: Per-cluster state directory; defaults under `~/.guildnet/tsnet/cluster-<id>` with secure permissions.
- Device IPs (100.x) and FQDNs are read from the local tsnet status and verified against Headscale; verified values are persisted under the per-cluster DB collection `devices`.

Notes:
- `PutCluster` will store `TSClientAuthKey` in the `credentials` bucket to avoid echoing it back in GET responses.
- `PutCluster` writes a `guildnet-cluster-settings` ConfigMap into the cluster namespace `guildnet-system` when Host App has cluster clients, enabling the in-cluster operator to read runtime flags.


## Host App configuration options (global and runtime)

Host App configuration lives in two areas:
- `pkg/config.Config` (persistent config file under `~/.guildnet/config.json`)

Operator requirements (multi-device)
----------------------------------

The operator (workspace-operator) expects the following at runtime when deployed in multi-device/operator mode:

- A valid `~/.guildnet/config.json` mounted or present inside the operator container that contains `login_server` and `auth_key` so tsnet can perform a non-interactive tailscale/headscale login.
- TLS cert and key available under `/root/.guildnet/state/certs/server.crt` and `/root/.guildnet/state/certs/server.key` (the deployment can mount the repository `certs/` directory as a ConfigMap at that path during tests).

When running the operator in Kubernetes, set `GN_CONTROL_PLANE_KUBECONFIG` to point at a mounted kubeconfig file if the operator must act on a control-plane other than the local cluster.
- Environment variables and runtime settings in `cmd/hostapp/main.go` and `internal/settings`.

### Persistent config (`pkg/config.Config` fields)

- LoginServer (string) — Tailscale login server URL (required)
- AuthKey (string) — Tailscale auth/preauth key (required)
- Hostname (string) — Host identifier for tailscale (required)
- ListenLocal (string) — Listener address for Host App (e.g. `127.0.0.1:8090`) (required)
- DialTimeoutMS (int) — dial timeout in milliseconds for outbound connections
- WorkspaceDomain, IngressClassName, WorkspaceTLSSecret, IngressAuthURL, IngressAuthSignin — legacy per-workspace/cluster hints (optional)

The config file path: `~/.guildnet/config.json` (created by tools like the init wizard)

### Environment variables / runtime flags

- GUILDNET_MASTER_KEY — required in production: a symmetric key used to encrypt Host App secrets stored in the local DB. Must be set in environment for the Host App process when running as a service.
- GN_EMBED_OPERATOR — when set to `1` (or truthy), Host App will start an embedded operator in-process. Do NOT set in production; in-cluster operator is recommended.
- GN_USE_GUILDNET_KUBECONFIG — opt-in for dev: when set, scripts like `scripts/run-hostapp.sh` will prefer `~/.guildnet/kubeconfig` as the source for `KUBECONFIG`.
- KUBE_PROXY_ADDR — explicit host:port or URL for a local kubectl proxy (e.g. http://127.0.0.1:8001). When set, the Host App will allow enabling a per-cluster APIProxyURL fallback and will detect local proxy availability.
 - GN_CONTROL_PLANE_KUBECONFIG — when set in the operator Deployment or Host App environment, the operator will load the control-plane kubeconfig from the specified file path inside the process/container (for example `/etc/guildnet/kubeconfig`). This is used when the operator must act on a remote control plane; it is preferred over the standard `KUBECONFIG` location when present.
 - WORKSPACE_NGINX_UNPRIVILEGED_IMAGE — optional environment variable to override the operator's preferred unprivileged nginx image used when a Workspace image appears to be an `nginx` variant. Default: `nginxinc/nginx-unprivileged:1.25`.
- LISTEN_LOCAL (or environment used to override `pkg/config.Config.ListenLocal`) — override the HTTP listener address
- Local cluster image/load variables — used by Makefile to build and load images for local clusters (prefer microk8s imports). See Makefile targets rather than environment-driven behavior for production.

### Runtime settings stored in localdb (via `settings.Manager`)

- `settings.Global` fields (persisted):
  - OrgID — default Org ID for new resources
  - FrontendOrigin — UI origin override
  - EmbedOperator — boolean persisted flag (but note GN_EMBED_OPERATOR environment variable controls startup-time embedded operator behavior)
  - DefaultNamespace — global default namespace for new clusters/workspaces
  - ListenLocal — fallback listener address persisted

- `settings.Cluster` — per-cluster runtime settings (see section above). `PutCluster` writes runtime configmap into cluster and persists to DB.

Heartbeat poster configuration
------------------------------
Devices that run the Host App poster may need to send their heartbeat to a Host App instance running on a different local port (for example when a system-installed hostapp is already running). Use the `GN_HEARTBEAT_URL` environment variable to override the poster target URL. Example:

```bash
export GN_HEARTBEAT_URL="https://127.0.0.1:18090/api/v1/sites/heartbeat"
export GN_HEARTBEAT_INTERVAL=5s

Streaming presence events
------------------------

The server exposes a Server-Sent Events (SSE) endpoint to stream realtime presence changefeed events:

- GET /v1/sites/stream

Optional query parameters:
- `clusterId` (preferred) or `cluster`: limit the stream to a single cluster id (normalized). If omitted the server will stream from all clusters that expose presence feeds.

Each SSE `data` event is a JSON object and includes a canonical `clusterId` field. For backward compatibility the server also sets `cluster`:

{
  "clusterId": "<cluster-id>",
  "event": { /* changefeed event payload */ }
}

The changefeed event payload follows the `ChangefeedEvent` DTO used elsewhere in the HTTP API and can contain `insert`, `update`, `delete` types with `before`/`after` row data.
./bin/hostapp serve
```

When `GN_HEARTBEAT_URL` is unset the poster defaults to `https://127.0.0.1:8090/api/v1/sites/heartbeat`.

Note: Heartbeat payloads accepted by the Host App must include the canonical `clusterId` field. Example: `{"clusterId":"<id>", "id":"device-name", ...}`. The server persists device rows with `clusterId` and cluster-level records use the canonical `id` field.

Sites listing (multi-device UI)
--------------------------------
- GET `/api/v1/sites` returns per-device records aggregated across clusters. Fields include `id`, `name`, `clusterId`, `tailnetIPs`, resource hints, and `lastSeen`.
- Records corresponding to the local Host App device are marked with `self: true` and intentionally set `lastSeen: null` so UIs can hide or de-emphasize the local device when listing “remote” sites.


## Examples and notes


```bash
curl -X POST "https://<host>:8090/api/deploy/clusters/<id>?action=attach-kubeconfig" \
  -H 'Content-Type: application/json' \
  -d '{"kubeconfig": "<base64-or-raw-kubeconfig-content>"}'
```

## Notes

The repository includes `scripts/verify-e2e.sh` (used by `make verify-e2e`) which has been updated to be compatible with multiple Headscale CLI versions and to follow redirects when probing HostApp proxy endpoints.


## Multi-device federation (ADR)

This repository contains an architecture decision and an implementation plan for multi-device federated clusters (cross-device service balancing).
- ADR: `docs/adr/0001-multi-device-cluster.md`
- Implementation plan: `docs/implementation/0001-multi-device-cluster-implementation.md`

- Create a workspace (simple job route delegates to Workspace CR creation):

```bash
curl -k -X POST "https://127.0.0.1:8090/api/cluster/<clusterID>/workspaces" -H 'Content-Type: application/json' -d '{"image":"codercom/code-server:4.90.3","name":"verify-e2e"}'
```

- Proxy to a workspace service (example in-browser path):
  - https://hostapp.example.com/api/cluster/<clusterID>/proxy/server/<serviceName>/

- Important operational notes:
  - In production prefer in-cluster operator and do not rely on `GN_EMBED_OPERATOR`.
  - Do not rely on automatic local `kubectl proxy` detection in production; configure `APIProxyURL` per-cluster or set `KUBE_PROXY_ADDR` intentionally.
  - TLS certificates and `GUILDNET_MASTER_KEY` are required for secure production runs.


Connecting multiple devices

For step-by-step instructions and examples for attaching multiple devices to the same cluster (join artifact, bootstrap flow, troubleshooting and sample commands) see the authoritative guide in `DEPLOYMENT.md` — the "Connecting multiple devices to the same cluster" section. API.md keeps the API reference concise; DEPLOYMENT.md is the how-to.

Device capabilities
-------------------
Devices are considered the authoritative source for local capabilities (CPU, memory, storage, VRAM, tailnet IPs). The Host App exposes a small heartbeat endpoint (`POST /v1/sites/heartbeat`) that devices use to report these values. The server persists the payload in the per-cluster localdb under collection `devices` and the UI/placement logic will prefer these values when making placement decisions.

> Note: the federation endpoints are mounted under `/api`, so the effective path for heartbeats is `POST /api/v1/sites/heartbeat`.

RBAC note: DeviceParticipant CRD

The Host App may create/update `DeviceParticipant` custom resources in the `guildnet-system` namespace as an in-cluster source-of-truth for device presence. Operators should grant the Host App a namespaced Role (or a ClusterRole for cluster-wide deployments) with verbs `get,list,watch,create,update,patch` on `deviceparticipants` and `get,update,patch` on `deviceparticipants/status`. Device IDs used as Kubernetes resource names are sanitized to valid RFC 1123 DNS names (lowercase, alnum and '-', start/end alnum, max 253 chars).

Sample Role and ClusterRole YAML are available under `config/rbac/`.


---

This file was generated from code inspections of `internal/api/router.go`, `internal/settings/settings.go` and `pkg/config/config.go`, and the repository's `DEPLOYMENT.md` and `architecture.md`. If you want changes to the format or additional details (example payloads per endpoint, HTTP response shapes, or OpenAPI generation), I can add them.

Recent operational changes (2025-10-21):
- The operator image was rebuilt and loaded into local clusters as `guildnet/hostapp:local` during testing.
- Several GuildNet CRDs (federatedclusters, federatedservices, sitestatuses, workspaces, capabilities) were applied to the test cluster to ensure all reconcilers can operate.
- A controller naming collision was fixed by explicitly naming the proxy reconciler (`proxy-reconciler`) to avoid duplicate controller registration when multiple controllers are registered.
- The Host App implements a synchronous fallback for Workspace creation: if the in-cluster operator does not reconcile a newly-created Workspace within a bounded timeout, Host App will create a Deployment and Service and update the Workspace.status so the UI and end-to-end tests remain functional.

See `internal/controller/proxy/controller.go` for the controller naming change and `internal/api/router.go` for the Workspace create/fallback implementation.