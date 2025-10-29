# ADR: Fully Containerized GuildNet Architecture with Cross-Device Federation over Tailscale

**Status:** Proposed
**Date:** 2025-10-23
**Author:** ChatGPT (GPT-5)
**Audience:** GuildNet Core & Systems Architecture Team
**Supersedes:** ADR-001 (MicroK8s Host-Based Design)

---

## 1. Context

GuildNet’s current prototype relies on **MicroK8s** installed directly on the host system, with **Tailscale** running at the OS level to provide secure networking between devices.
While functional, this approach introduces several limitations:

* Requires **system-level dependencies** (Snap, systemd, etc.), which are incompatible with minimal or immutable host environments.
* Increases operational complexity for multi-device deployment.
* Makes it difficult to achieve a **self-contained, portable “Docker-only”** runtime.
* Restricts automation and scalability across heterogeneous devices.

The project’s vision—“a private distributed cloud where resources across devices are shared seamlessly”—requires a **portable, host-agnostic deployment** that can operate on any device with Docker installed.

---

## 2. Decision

We will **transition GuildNet from a host-based Kubernetes and networking model** to a **fully containerized architecture** that operates entirely within Docker.
Each device will host a set of containers that together form part of a larger, federated cluster connected via **Tailscale**.

The new architecture must allow multiple devices, each running only Docker, to automatically discover and federate into a **single distributed resource pool** over the tailnet.

---

## 3. Goals and Requirements

### 3.1 High-Level Goals

* Enable GuildNet to run on any system that provides Docker, **with no host-level dependencies**.
* Support federation of multiple Docker hosts into a single, logical cluster.
* Maintain **private, secure communication** between all nodes over Tailscale.
* Provide consistent **workload scheduling, service exposure, and data sharing** across devices.
* Preserve the existing developer experience and core concepts (Host App, Operator, Workspaces, Participants, Bootstrap flow).

### 3.2 Functional Requirements

**Cluster Formation**

* Each device should autonomously join or form a cluster using Tailscale-based discovery.
* The cluster should appear as a unified Kubernetes environment (or equivalent orchestration layer).
* Nodes must use their **Tailscale IPs or MagicDNS names** for all internal control and data plane communication.

**Network Federation**

* All inter-device communication must occur over Tailscale.
* No reliance on host networking or static port mappings.
* Support both single-cluster and multi-cluster federation models (configurable).

**Service Exposure**

* GuildNet workloads and APIs should be exposable privately via **Tailscale ingress** or equivalent mechanisms.
* MagicDNS should provide stable, private addressing for all exposed endpoints.

**State and Storage**

* Persistent workloads must use **distributed or replicated storage** capable of operating across the tailnet.
* Data should remain within the tailnet boundary, ensuring privacy and locality.

**Cluster Management**

* The **GuildNet Operator** remains responsible for orchestrating cluster-level configuration, scheduling policies, and service synchronization.
* **Leader election** must ensure consistent control across multiple devices.
* **Bootstrap flow** must support deterministic cluster IDs and secure node enrollment.

**Security and Isolation**

* Each containerized component should require minimal privileges.
* Authentication and authorization should leverage existing Tailscale identity and ACLs where possible.
* Secrets and credentials must be handled through secure in-container mechanisms (e.g., mounted configs, env vars, or secrets).

---

## 4. Non-Functional Requirements

| Category          | Requirement                                                                                                                  |
| ----------------- | ---------------------------------------------------------------------------------------------------------------------------- |
| **Portability**   | Must run on any device supporting Docker Engine (no dependency on systemd, Snap, or custom OS features).                     |
| **Resilience**    | Cluster should tolerate device churn and transient network failures.                                                         |
| **Scalability**   | Support for increasing number of devices and workloads without manual reconfiguration.                                       |
| **Security**      | End-to-end encryption over tailnet; isolation of workloads between devices.                                                  |
| **Simplicity**    | Minimal setup steps: Docker install → configuration → bootstrap.                                                             |
| **Observability** | Cluster state, device health, and connectivity should be visible via standard metrics and APIs.                              |
| **Consistency**   | Federation behavior must preserve the same abstractions (Workspaces, Participants, Resources) as in current GuildNet design. |

---

## 5. Architectural Overview

1. **Containerized Node Stack**
   Each device runs a minimal set of containers responsible for:

   * Node orchestration (lightweight Kubernetes or equivalent).
   * Tailscale connectivity (overlay networking).
   * GuildNet Host App and Operator services.

2. **Tailnet as the Unified Network Layer**

   * All devices communicate exclusively via Tailscale.
   * The tailnet provides addressing, encryption, and peer discovery.
   * Node IPs and service endpoints are bound to tailnet addresses.

3. **Federated Control Plane**

   * One or more nodes act as control-plane participants.
   * Worker nodes on other devices register and share resources.
   * Optional: multiple independent clusters can interconnect via the same tailnet for redundancy.

4. **Service Exposure & Routing**

   * Application endpoints are published to the tailnet rather than the public internet.
   * Routing and ingress are managed via a Tailscale-aware operator component.

5. **Storage & Data Layer**

   * Shared storage mechanisms operate over the tailnet.
   * Local volumes are replicated or synchronized across devices when needed.

6. **Management Plane**

   * The GuildNet Operator oversees synchronization of workloads, participants, and published services.
   * The Host App maintains device identity, configuration, and runtime status.

---

## 6. Constraints and Assumptions

* Only Docker is available on the host.
* Containers may require elevated privileges (e.g., for networking).
* Tailscale authentication and identity management remain external to GuildNet.
* DNS, routing, and ACL behavior depend on the user’s Tailscale configuration.
* The solution must work in heterogeneous environments (e.g., mixed CPU architectures).

---

## 7. Expected Outcomes

* Fully self-contained GuildNet deployment per device.
* Seamless cross-device federation into a single, secure, tailnet-based cluster.
* Private exposure of workloads without public ingress configuration.
* Simplified deployment for end users (“just install Docker and join the network”).
* Architectural parity with existing GuildNet abstractions (no redesign of Operator, Host App, or CRDs required).

---

## 8. Out of Scope

* Integration with non-Docker container runtimes.
* Alternative overlay networks (e.g., Zerotier, Nebula).
* Host-level package or system service installations.
* Edge device provisioning or lifecycle management tooling.

---

## 9. Next Steps

* Validate Kubernetes distribution suitable for containerized operation.
* Define container responsibilities and compose relationships.
* Specify cluster formation and bootstrap protocols over tailnet.
* Update documentation to reflect new runtime model.
* Establish performance and reliability baselines under tailnet federation.

---
