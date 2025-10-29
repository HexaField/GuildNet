# DeviceParticipant CRD: RBAC & Security Guidance

This document describes the minimal RBAC and operational guidance required to allow GuildNet to write and reconcile `DeviceParticipant` custom resources in a Kubernetes cluster.

Overview
- Resource: `deviceparticipants.guildnet.io` (group `guildnet.io`, version `v1alpha1`)
- Namespace: recommended `guildnet-system` (configurable in code; defaults to `guildnet-system`)

Recommended deployment models
- Operator/HostApp grants: grant the HostApp (or an operator service account) permission to create/update DeviceParticipant objects in the `guildnet-system` namespace.
- Controller vs cluster-wide: prefer a Namespaced Role when HostApp instances only need to write CRs into a single namespace. If components run as a cluster-level operator, grant a ClusterRole with reduced verbs.

Least-privilege Role (namespaced)
```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: guildnet-deviceparticipant-writer
  namespace: guildnet-system
rules:
  - apiGroups: ["guildnet.io"]
    resources: ["deviceparticipants"]
    verbs: ["get", "list", "watch", "create", "update", "patch"]
  - apiGroups: ["guildnet.io"]
    resources: ["deviceparticipants/status"]
    verbs: ["get", "update", "patch"]
```

Least-privilege ClusterRole (when multi-namespace)
```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: guildnet-deviceparticipant-writer
rules:
  - apiGroups: ["guildnet.io"]
    resources: ["deviceparticipants"]
    verbs: ["get", "list", "watch", "create", "update", "patch"]
  - apiGroups: ["guildnet.io"]
    resources: ["deviceparticipants/status"]
    verbs: ["get", "update", "patch"]
```

Binding the role to the HostApp service account
```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: guildnet-deviceparticipant-writer-binding
  namespace: guildnet-system
subjects:
  - kind: ServiceAccount
    name: hostapp
    namespace: guildnet-system
roleRef:
  kind: Role
  name: guildnet-deviceparticipant-writer
  apiGroup: rbac.authorization.k8s.io
```

Operational notes
- If HostApp cannot reach the Kubernetes API directly due to network boundaries, use a short-lived kubeconfig with minimal privileges and rotate credentials frequently.
- Consider using a dedicated ServiceAccount for writing DeviceParticipant resources so privileges can be audited and revoked independently.
- Monitor for failed upserts and ensure reconciler logs surface permission errors so operators can grant correct RBAC bindings.

Related: the HostApp will also write to a local durable queue (`pending_deviceparticipants`) when it cannot reach the Kubernetes API; ensure the HostApp instance has local filesystem persistence configured.
