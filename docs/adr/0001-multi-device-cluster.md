## ADR 0001: Multi-device federated cluster with cross-device service balancing

Date: 2025-10-17

Status: Proposed

Scope
-----
This ADR documents the design for launching a Kubernetes cluster across multiple devices (hosts) so that workloads, servers and services can be shared and load-balanced across the contributing devices. It covers control-plane choices, networking (using existing Tailscale/Headscale integrations), service discovery and load balancing, placement heuristics, failure modes, security considerations, and an implementation/migration plan that integrates with the existing GuildNet architecture.

Context
-------
- GuildNet already provides functionality to run a Headscale server, create a Tailscale network, launch local clusters (microk8s, kind, etc.), run a local Host App UI/portal and to share/join clusters between devices. See `README.md`, `AGENTS.md`, `cmd/hostapp/main.go`, and `cmd/tsnet-subnet-router/main.go` for current behavior.
- The repo contains utilities to bring up microk8s (`scripts/microk8s-setup.sh`), deploy an in-cluster Tailscale subnet router (`scripts/deploy-tailscale-router.sh`), and tsnet-based forwarders (`cmd/tsnet-forward`).
- Goals: enable launching a distributed/federated cluster that spans multiple physical hosts (each potentially running a local Kubernetes runtime) and provide cross-device scheduling and balancing so services are resilient and can use resources across all participating devices.

Constraints and non-goals
-------------------------
- This design must work with the existing production-focused flow (no separate "dev" mode). Defaults should be sensible and configurable via environment variables or config files.
- Do not require a single vendor-locked solution; prefer composable, well-understood open-source components.
- Non-goal: replace upstream Kubernetes control-plane for large clusters. The target is small-to-medium multi-device clusters and edge/federated scenarios (developer machines, lab clusters, small edge fleets).

Decision
--------
We will implement a federated multi-device cluster capability built from three cooperating layers:

1. Secure network overlay and per-device connectors (existing): use Tailscale/Headscale and `tsnet` to provide L3 connectivity for control and data plane between devices and cluster services. Reuse and standardize per-cluster Headscale namespaces and the in-cluster subnet router model already present in the repo.

2. Multi-cluster control-plane federation: introduce a lightweight federation control-plane in the Host App Operator that can treat each device-local Kubernetes runtime (microk8s) as a worker "zone" or "site" under a single logical cluster name. The operator will manage cluster membership, sync minimal cluster metadata (node capacity, labels, endpoints), and coordinate a cross-device service placement controller.

3. Cross-device service balancing and placement: implement a service controller that exposes a logical Service API (via the Host App REST/Operator APIs) that maps to instance placements across available sites. Traffic balancing will be achieved via a combination of (a) clustered DNS / service discovery for routing to healthy endpoints; (b) per-host Envoy (or small L7 proxy) sidecars/agents + global traffic policy; and (c) optional advertised routes and per-node external LoadBalancer implementation using MetalLB semantics where applicable.

Rationale
---------
- Reusing Tailscale/tsnet keeps networking secure and avoids NAT and firewall issues for cross-device traffic. GuildNet already supports this approach (`scripts/deploy-tailscale-router.sh`, `cmd/tsnet-subnet-router`).
- A full upstream Kubernetes federation (KubeFed) is heavyweight and complex for the target use-case; a tailored, GitOps-friendly operator in `hostapp` that coordinates per-device runtimes is simpler and more maintainable.
- Per-host proxies (Envoy or a lightweight proxy) allow L7-aware routing and granular health checks without modifying pods or app code. They also integrate well with tsnet forwarding (see `cmd/tsnet-forward`).

Architecture overview
---------------------

Components to add/modify
- Federation/Coordinator controller (part of Host App operator)
  - Tracks member sites (devices) and their kubeconfigs/kube API endpoints. Stores health, capacity, labels and advertised routes.
  - Maintains a global desired state for logical clusters and services.
  - Responsible for placement decisions and reconciling per-site manifests (e.g., Deployments/DaemonSets/Service objects) based on placement policies.

- Placement & heuristic engine
  - Inputs: node/host resource advert (CPU, memory, disk, special hardware), current workload metrics (pod CPU/memory), device-level availability, network latency/bandwidth estimates, user constraints (anti-affinity, preferred site), cost models and site priorities.
  - Outputs: per-site placement plan for replicas and services.
  - Heuristics: bin-packing (first-fit-decreasing), spread-aware scheduling for high-availability (spread replicas across sites), latency-aware placement for low-latency services, and soft-load rebalancing when host load changes.

- Cross-device Service abstraction
  - Logical service manifests (new CRD: FederatedService or MultiDeviceService) declare the desired service, replica count, and policy (performance/availability/cost tuning).
  - The controller creates per-site Service endpoints and advertises them via DNS + per-site proxies. Optionally, it can create headless services plus global DNS entries that map to per-site endpoints.

- Per-host proxy/agent
  - Lightweight proxy (Envoy or similar) runs on each host (as a DaemonSet in local runtime or a host process managed by GuildNet agent). Exposes a local listening endpoint and routes to local pods or remote sites based on controller-provided endpoint lists.
  - Integrates with tsnet forwarding to provide secure access outside the local host.

- Data plane networking
  - Use existing tailscale subnet router approach to make cluster pod/service CIDRs reachable across devices when requested. For service traffic, prefer application-layer routing via proxies to avoid reliance on full L3 routing for every flow.

Data model & API
----------------
- New CRDs / API additions (hostapp operator):
  - MultiDeviceCluster (metadata: clusterName, members[], desiredState)
  - MultiDeviceService (spec: selector, ports, replicas, placementPolicy)
  - SiteStatus (node counts, capacity, lastSeen, network metrics)

- Host App REST API extensions
  - Endpoints to join/leave a site, query site metrics, request federation control, and create MultiDeviceService.
  - Integrate with existing bootstrap/join flow (`scripts/join.sh`, `guildnet.config`) so devices can advertise themselves as joinable sites.

Placement heuristics and algorithms
----------------------------------
- Initial version: rule-based placement using prioritized constraints.
  - Policy examples: "spread across N sites", "prefer high CPU sites", "co-locate cache with DB replicas".
  - Use a scoring model: score(site) = a*availableCPU + b*freeMemory - c*latency - d*currentLoad
  - Rebalance thresholds: only move replicas when benefit > cost (rollout/traffic migration cost).

- Future: augment with ML-based profiles or capacity prediction; for now keep deterministic and explainable heuristics.

Load balancing and service discovery
-----------------------------------
- DNS & service discovery: Host App will maintain a global DNS map for logical services (hosted within the Host App's UI/HTTP API). Clients (UI and other services) will resolve a service to a list of endpoints.
- Proxy-based L7 balancing: per-host proxies route to the nearest healthy endpoints and can perform retries, circuit-breaking, and observability.
- L4 balancing options: when using MetalLB or cloud LB, controller will create LoadBalancer services per-site and configure a global front (optional) that can be a geographically-distributed anycast via Tailscale+external IP management.

Security
--------
- Continue to rely on Tailscale/Headscale for secure connectivity between sites (mutual-authenticated mesh).
- Host App operator will authenticate site join requests with pre-auth keys (existing Headscale workflows) and require kubeconfig proofs for cluster membership.
- All control-plane traffic between Host App and per-site controllers uses mTLS where possible; access to kube API uses kubeconfigs and RBAC.

Failure modes and resilience
---------------------------
- Device offline: controller de-schedules non-critical replicas from the failed site according to policy and spins them up elsewhere if resources allow.
- Network partition: conservative policy — prefer availability over split-brain for stateful workloads; use quorum-based controllers for stateful components.
- Controller failure: Host App operator is expected to be run redundantly (e.g., multiple hostapp instances in different nodes). Persistence stored in GuildNet DB (internal/localdb) for recovery.

Migration and rollout plan
-------------------------
Phase 0 (experiment/proof-of-concept)
- Implement a read-only discovery mode: Host App can list candidate sites and display node capacities. No scheduling or CRDs yet.
- Add per-site metrics collection and a simple placement API.

Phase 1 (CRD + controller)
- Implement `MultiDeviceService` CRD and controller that can deploy per-site Deployments/ReplicaSets and register endpoints with a per-service DNS.
- Add per-host proxy as a simple sidecar/DaemonSet and a controller to manage its config (endpoint lists).

Phase 2 (policy, rebalancing, HA)
- Add placement policies, rebalancing controller, health checks and graceful migration tooling (drain + recreate + traffic weight shifting).

Phase 3 (operator hardening)
- Performance tuning, failover policies, observability (metrics/tracing) and upgrades.

Integration with repository
---------------------------
- Host App: extend `cmd/hostapp/main.go` and operator code to include the federation coordinator and new CRDs.
- Networking: reuse `cmd/tsnet-subnet-router` and `cmd/tsnet-forward` for secure routing needs.
- Scripts: extend `scripts/join.sh`, `scripts/deploy-tailscale-router.sh`, and `scripts/microk8s-setup.sh` to support site enrollment and kubeconfig emission for Host App.
- Docs: add new docs/ADR (this file), update `architecture.md`, `API.md`, and `DEPLOYMENT.md` with a summary and links.

Testing and validation
----------------------
- Unit tests for placement engine and controllers.
- Integration tests using VMs/containers that simulate multiple devices (use existing tests/ integration_quickstart_test.go as a template).
- E2E smoke tests that verify cross-device service creation, failover and rebalancing (extend `tests/verify-workspace.sh` and `verify-cluster.sh`).

Operational concerns
--------------------
- Resource accounting: sites must report accurate capacity; controller must be able to apply soft quotas.
- Visibility: UI must show per-site health, resource usage and placement decisions.
- Upgrades: operator must support rolling upgrades with operator-managed manifests and migration hooks.

Alternatives considered
-----------------------
- KubeFed (Kubernetes Federation v2): powerful but complex to operate and integrate with local single-node microk8s instances. We rejected it due to operational complexity and heavy control-plane requirements.
- Crossplane: good for multi-cloud resource management but orthogonal — Crossplane could be used later for provisioning infrastructure, not for short-lived per-host scheduling.
- Service mesh-only approach (Istio multi-cluster): useful for networking but doesnt solve scheduling and placement decisions; could be integrated later for advanced traffic management.

Consequences
------------
- Adds additional operator complexity and requires careful testing for partition and failure handling.
- Improves availability and resource utilization for multi-device clusters; unlocks cross-device workload sharing for development and edge scenarios.


References
----------

- GuildNet code references: `cmd/hostapp/main.go`, `cmd/tsnet-subnet-router/main.go`, `cmd/tsnet-forward/main.go`, `scripts/microk8s-setup.sh`, `scripts/deploy-tailscale-router.sh`, `scripts/join.sh`.

- Industry references and notes:
  - KubeFed (Kubernetes Cluster Federation): historically the canonical project for Kubernetes federation. Note: the official `kubefed` repo is archived and federation v2 is no longer under active development in the same repository; this increases operational complexity and makes it a less attractive option for small multi-device fleets. See https://github.com/kubernetes-retired/kubefed and Kubernetes SIG Multicluster for status.
  - KubeEdge: a CNCF project that extends Kubernetes to the edge, supports cloud-edge coordination and lightweight edge components. Useful where an autonomous edge control-plane and metadata sync are required; heavier than a simple federation controller but worth considering for edge-specific device management. https://kubeedge.io/
  - Submariner: provides cross-cluster L3 connectivity and service discovery across clusters, designed to interconnect clusters' networking (including overlapping CIDRs). It is network-focused and pairs well with a federation controller for service discovery. https://submariner.io/
  - MetalLB: a bare-metal LoadBalancer implementation for Kubernetes. Useful to provide LoadBalancer IPs where cloud LB isn't available; MetalLB can be used per-site to offer stable external endpoints. https://metallb.universe.tf/
  - Envoy: modern edge/L7 proxy used for traffic routing, retries, circuit-breaking and observability. Running per-host Envoy/proxies enables advanced L7 balancing across device endpoints. https://www.envoyproxy.io/
  - Service mesh references (Istio/Linkerd): multi-cluster service mesh patterns provide advanced traffic management but do not by themselves implement scheduling/placement. They are complementary and can be integrated later for sophisticated L7 routing.

These references informed the design trade-offs documented in this ADR (favor a lightweight Host App federation operator + per-host proxy approach rather than adopting heavyweight Kubernetes federation projects).

Next steps
----------
1. Add CRD skeletons and scaffolding for the Federation controller in the Host App operator. (low-risk repository change)
2. Implement per-site metrics/heartbeat and site registration via existing join flow.
3. Prototype per-host proxy configuration (Envoy) and test basic traffic routing across two devices.
4. Implement placement heuristics and unit tests.

---

Appendix: example MultiDeviceService spec (v0)

```yaml
apiVersion: guildnet.io/v1alpha1
kind: MultiDeviceService
metadata:
  name: hello
spec:
  selector:
    app: hello
  ports:
    - name: http
      port: 80
      targetPort: 8080
  replicas: 3
  placementPolicy:
    spreadAcrossSites: 2
    prefer: ["high-cpu", "low-latency"]
```
