#!/usr/bin/env bash
set -euo pipefail
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
[ -f "$ROOT/.env" ] && . "$ROOT/.env"
export KUBECONFIG="${KUBECONFIG:-${GN_KUBECONFIG:-$HOME/.guildnet/kubeconfig}}"

NAMESPACE=${OPERATOR_NAMESPACE:-guildnet-system}
IMG_PULL_SECRET=${K8S_IMAGE_PULL_SECRET:-}
IMAGE=${OPERATOR_IMAGE:-ghcr.io/your/module/hostapp:latest}

# Normalize local dev image names: when using a ':local' tag and loading into a
# local runtime (MicroK8s/containerd), keep refs consistent. If OPERATOR_IMAGE looks
# like a local tag (ends with ':local') and doesn't already have a registry
# prefix (i.e. contains no '.' in the first path segment), prefer the
# docker.io/<repo>:local form so the runtime resolves the same ref we imported.
if echo "$IMAGE" | grep -q ":local$"; then
  # If image already starts with a domain like 'ghcr.io' or 'docker.io', keep it
  if ! echo "$IMAGE" | grep -qE "^[^/]+\.[^/]+/"; then
    IMAGE="docker.io/$IMAGE"
  fi
fi

# Prefer 'Never' for local dev images (e.g. tags that include ':local') so
# container runtimes don't attempt to pull from a registry when the image is
# loaded locally (MicroK8s containerd import). Default to IfNotPresent.
case "$IMAGE" in
  *:local)
    IMAGE_PULL_POLICY=Never
    ;;
  *)
    IMAGE_PULL_POLICY=IfNotPresent
    ;;
esac

need() { command -v "$1" >/dev/null 2>&1 || { echo "Missing: $1" >&2; exit 1; }; }
need kubectl

# Skip silently if Kubernetes API is not reachable or kubeconfig is invalid
if ! kubectl --request-timeout=3s get --raw=/readyz >/dev/null 2>&1; then
  echo "[operator] Kubernetes API not reachable or kubeconfig invalid; skipping"
  exit 0
fi

kubectl get ns "$NAMESPACE" >/dev/null 2>&1 || kubectl create ns "$NAMESPACE"

# Minimal RBAC for controller-runtime manager across namespaces
cat <<RBAC | kubectl apply -f -
apiVersion: v1
kind: ServiceAccount
metadata:
  name: workspace-operator
  namespace: ${NAMESPACE}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: workspace-operator
rules:
  - apiGroups: [""]
    resources: ["configmaps","secrets"]
    verbs: ["get","list","watch","create","update","patch"]
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["create","patch","update","list","watch"]
  - apiGroups: ["apps"]
    resources: ["deployments"]
    verbs: ["get","list","watch","create","update","patch","delete"]
  - apiGroups: [""]
    resources: ["services"]
    verbs: ["get","list","watch","create","update","patch","delete"]
  - apiGroups: ["networking.k8s.io"]
    resources: ["ingresses"]
    verbs: ["get","list","watch","create","update","patch","delete"]
  - apiGroups: ["guildnet.io"]
    resources: ["workspaces","workspaces/status","deviceparticipants","deviceparticipants/status"]
    verbs: ["get","list","watch","create","update","patch","delete"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: workspace-operator
subjects:
- kind: ServiceAccount
  name: workspace-operator
  namespace: ${NAMESPACE}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: workspace-operator
RBAC

# Operator Deployment with explicit operator mode (override ENTRYPOINT)
# Only include imagePullSecrets if the named secret actually exists in the target namespace.
USE_IMG_PULL_SECRET=0
if [ -n "$IMG_PULL_SECRET" ]; then
  if kubectl -n "$NAMESPACE" get secret "$IMG_PULL_SECRET" >/dev/null 2>&1; then
    USE_IMG_PULL_SECRET=1
  else
    echo "[operator] WARNING: requested image pull secret '$IMG_PULL_SECRET' not found in namespace $NAMESPACE; skipping imagePullSecrets"
    USE_IMG_PULL_SECRET=0
  fi
fi

if [ "$USE_IMG_PULL_SECRET" -eq 1 ]; then
cat <<YAML | kubectl apply -n "$NAMESPACE" -f -
apiVersion: apps/v1
kind: Deployment
metadata:
  name: workspace-operator
  labels:
    app: workspace-operator
spec:
  replicas: 1
  selector:
    matchLabels:
      app: workspace-operator
  template:
    metadata:
      labels:
        app: workspace-operator
    spec:
      serviceAccountName: workspace-operator
      imagePullSecrets:
      - name: ${IMG_PULL_SECRET}
      containers:
      - name: operator
        image: ${IMAGE}
        imagePullPolicy: ${IMAGE_PULL_POLICY}
        command: ["/usr/local/bin/hostapp"]
        args: ["operator"]
        env:
        - name: K8S_NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.namespace
        - name: WORKSPACE_LB_DEFAULT
          value: "true"
        resources: {}
YAML
else
cat <<YAML | kubectl apply -n "$NAMESPACE" -f -
apiVersion: apps/v1
kind: Deployment
metadata:
  name: workspace-operator
  labels:
    app: workspace-operator
spec:
  replicas: 1
  selector:
    matchLabels:
      app: workspace-operator
  template:
    metadata:
      labels:
        app: workspace-operator
    spec:
      serviceAccountName: workspace-operator
      containers:
      - name: operator
        image: ${IMAGE}
        imagePullPolicy: ${IMAGE_PULL_POLICY}
        command: ["/usr/local/bin/hostapp"]
        args: ["operator"]
        env:
        - name: K8S_NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.namespace
        resources: {}
YAML
fi

echo "Operator deployment applied in namespace ${NAMESPACE}"

# If no image pull secret was configured, ensure any previous imagePullSecrets
# are removed from the Deployment so clusters that only use local images
# don't attempt to fetch from a registry using a missing secret.
if [ -z "${IMG_PULL_SECRET:-}" ] || [ "$USE_IMG_PULL_SECRET" -eq 0 ]; then
  echolog() { echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] $*"; }
  echolog "IMG_PULL_SECRET not configured or not usable; ensuring deployment has no imagePullSecrets"
  kubectl -n "$NAMESPACE" patch deployment workspace-operator --type=json -p='[{"op":"remove","path":"/spec/template/spec/imagePullSecrets"}]' >/dev/null 2>&1 || true
fi

# If running in a local cluster and the image appears to be a remote GHCR image, warn the user
if echo "${IMAGE}" | grep -q "ghcr.io" 2>/dev/null; then
  echo "[operator] NOTE: operator image appears to be hosted on ghcr.io; ensure the target cluster can pull this image or import it into MicroK8s (e.g., microk8s ctr -n k8s.io images import /path/image.tar) before deploying"
fi
