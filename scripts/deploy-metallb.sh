#!/usr/bin/env bash
set -euo pipefail
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
[ -f "$ROOT/.env" ] && . "$ROOT/.env"
export KUBECONFIG="${KUBECONFIG:-${GN_KUBECONFIG:-$HOME/.guildnet/kubeconfig}}"

POOL_NAME=${METALLB_POOL_NAME:-workspaces}
POOL_RANGE=${METALLB_POOL_RANGE:-}

need() { command -v "$1" >/dev/null 2>&1 || { echo "Missing: $1" >&2; exit 1; }; }
need kubectl

# Skip silently if Kubernetes API is not reachable or kubeconfig is invalid
if ! kubectl --request-timeout=3s get --raw=/readyz >/dev/null 2>&1; then
  echo "[metallb] Kubernetes API not reachable or kubeconfig invalid; skipping"
  exit 0
fi

if [ -z "$POOL_RANGE" ]; then
  # If running on a machine with a 'kind' docker network, auto-detect a pool. Otherwise require explicit pool
  # Try to auto-detect a local Docker network subnet (useful for local test clusters).
  # If detection fails, require explicit METALLB_POOL_RANGE and assume a real cluster.
  SUBNET=$(docker network ls --format '{{.Name}}' 2>/dev/null | while read -r net; do docker network inspect "$net" -f '{{range .IPAM.Config}}{{.Subnet}}{{"\n"}}{{end}}' 2>/dev/null | grep -E '^[0-9]+\.' | head -n1 && break; done || true)
  if [ -n "$SUBNET" ]; then
    ipv4=$(echo "$SUBNET" | cut -d'/' -f1)
    bits=$(echo "$SUBNET" | cut -d'/' -f2)
    o1=$(echo "$ipv4" | awk -F. '{print $1}')
    o2=$(echo "$ipv4" | awk -F. '{print $2}')
    o3=$(echo "$ipv4" | awk -F. '{print $3}')
    if [ "${bits:-16}" -ge 24 ] 2>/dev/null; then
      p3=$o3
    else
      p3=255
    fi
    base="$o1.$o2.$p3"
    POOL_RANGE="${base}.200-${base}.250"
    echo "[metallb] Auto-selected pool (IPv4): $POOL_RANGE"
  else
    echo "[metallb] METALLB_POOL_RANGE not set and no local Docker network detected for auto-selection."
    echo "Assuming this is a real cluster with external LoadBalancer support; skipping MetalLB installation."
    echo "If you want MetalLB installed, set METALLB_POOL_RANGE to an appropriate L2 range and re-run."
    exit 0
  fi
fi

# Install MetalLB manifests (idempotent)
kubectl apply -f https://raw.githubusercontent.com/metallb/metallb/v0.13.12/config/manifests/metallb-native.yaml >/dev/null || true

# Wait for CRDs to be established before using them (avoid races)
kubectl wait --for=condition=Established crd/ipaddresspools.metallb.io --timeout=180s || true
kubectl wait --for=condition=Established crd/l2advertisements.metallb.io --timeout=180s || true

# Wait for controller/speaker rollouts to complete (best-effort)
kubectl -n metallb-system rollout status deploy/controller --timeout=180s || true
kubectl -n metallb-system rollout status ds/speaker --timeout=180s || true

# Prepare pool + L2Advertisement manifest
MANIFEST=$(cat <<YAML
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata:
  name: ${POOL_NAME}
  namespace: metallb-system
spec:
  addresses:
    - ${POOL_RANGE}
---
apiVersion: metallb.io/v1beta1
kind: L2Advertisement
metadata:
  name: ${POOL_NAME}-l2
  namespace: metallb-system
spec:
  ipAddressPools:
    - ${POOL_NAME}
YAML
)

# Retry a server-side dry-run until the webhook is ready to accept the resource
tries=${METALLB_APPLY_RETRIES:-60}
delay=${METALLB_APPLY_DELAY:-3}
while (( tries > 0 )); do
  if printf '%s' "$MANIFEST" | kubectl apply --dry-run=server -f - >/dev/null 2>&1; then
    break
  fi
  tries=$((tries-1))
  sleep "$delay"
done

# Apply pool + L2Advertisement for real with retries to tolerate webhook startup races
tries=${METALLB_APPLY_RETRIES_FINAL:-60}
delay=${METALLB_APPLY_DELAY_FINAL:-3}
ok=0
while (( tries > 0 )); do
  if printf '%s' "$MANIFEST" | kubectl apply -f -; then
    ok=1
    break
  fi
  tries=$((tries-1))
  sleep "$delay"
done

if [ "$ok" -ne 1 ]; then
  echo "[metallb] WARN: Failed to apply IPAddressPool/L2Advertisement after retries; continuing" >&2
else
  echo "MetalLB installed with pool ${POOL_NAME}=${POOL_RANGE}"
fi
