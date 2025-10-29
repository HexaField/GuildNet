#!/usr/bin/env bash
set -euo pipefail

# verify-crds-operator.sh
# Validate CRDs are installed and the operator is running on the current kube-context.

GN_KUBECONFIG=${GN_KUBECONFIG:-"$HOME/.guildnet/kubeconfig"}
export KUBECONFIG="$GN_KUBECONFIG"

ns=${GN_OPERATOR_NAMESPACE:-guildnet-system}

echo "[verify-crds-operator] kubeconfig: $KUBECONFIG"

# 1) Check CRDs
need_crds=(
  workspaces.guildnet.io
  capabilities.guildnet.io
)

for crd in "${need_crds[@]}"; do
  if ! kubectl get crd "$crd" >/dev/null 2>&1; then
    echo "[verify-crds-operator] MISSING CRD: $crd" >&2
    exit 2
  fi
  echo "[verify-crds-operator] CRD ok: $crd"
done

# 2) Check operator deployment/pod
if ! kubectl -n "$ns" get deploy -l app=guildnet-operator >/dev/null 2>&1; then
  echo "[verify-crds-operator] operator deployment not found in namespace $ns" >&2
  exit 2
fi
kubectl -n "$ns" get deploy -l app=guildnet-operator

# 3) Check pods are ready
kubectl -n "$ns" get pods -l app=guildnet-operator -o wide

# 4) Tail recent logs (best-effort)
kubectl -n "$ns" logs -l app=guildnet-operator --tail=200 --all-containers=true || true

echo "[verify-crds-operator] OK"
