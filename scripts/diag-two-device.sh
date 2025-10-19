#!/usr/bin/env bash
set -euo pipefail
export KUBECONFIG="${KUBECONFIG:-${GN_KUBECONFIG:-$HOME/.guildnet/kubeconfig}}"

ns=${OPERATOR_NAMESPACE:-guildnet-system}
echo "[diag] Kube API /readyz:"
kubectl --request-timeout=5s get --raw='/readyz?verbose' || true

echo "\n[diag] Nodes:"
kubectl get nodes -o wide || true

echo "\n[diag] CRDs (guildnet.io):"
kubectl get crd | grep guildnet.io || true

echo "\n[diag] Operator deployment and pods:"
kubectl -n "$ns" get deploy,po || true

echo "\n[diag] Tailscale router DS:"
kubectl -n kube-system get ds tailscale-subnet-router -o wide || true

echo "\n[diag] Published registry ConfigMaps:"
kubectl -n guildnet-system get cm -l app=published-registry || true

echo "\n[diag] HostApp healthz:"
curl -k --max-time 2 "${HOSTAPP_URL:-https://127.0.0.1:8090}/healthz" || true

echo "\n[diag] Done."
