#!/usr/bin/env bash
set -euo pipefail

# verify-k0s.sh
# Quick readiness checks for the Docker-only k0s node stack.

GN_KUBECONFIG=${GN_KUBECONFIG:-"$HOME/.guildnet/kubeconfig"}

echo "[verify-k0s] kubeconfig: $GN_KUBECONFIG"
if [ ! -s "$GN_KUBECONFIG" ]; then
  echo "[verify-k0s] kubeconfig not found" >&2
  exit 2
fi

export KUBECONFIG="$GN_KUBECONFIG"

set +e
kubectl --request-timeout=5s get --raw='/readyz?verbose'
RC1=$?
kubectl get nodes -o wide
RC2=$?
kubectl version --short
RC3=$?
set -e

if [ $RC1 -ne 0 ] || [ $RC2 -ne 0 ]; then
  echo "[verify-k0s] readiness checks failed" >&2
  exit 2
fi

echo "[verify-k0s] OK"
