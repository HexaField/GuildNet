#!/usr/bin/env bash
set -euo pipefail
KUBECONFIG_PATH="${KUBECONFIG:-$HOME/.guildnet/kubeconfig}"
export KUBECONFIG="$KUBECONFIG_PATH"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
YAML="$REPO_ROOT/k8s/local-path-storage.yaml"

if [[ ! -f "$YAML" ]]; then
  echo "ERROR: manifest not found: $YAML" >&2
  exit 1
fi

echo "Applying local-path-provisioner (namespace, RBAC, ConfigMap, Deployment, StorageClass)..."
kubectl apply -f "$YAML"

echo "Waiting for deployment/local-path-provisioner to be ready..."
kubectl -n local-path-storage rollout status deploy/local-path-provisioner --timeout=120s

echo "Done."
