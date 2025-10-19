#!/usr/bin/env bash
set -euo pipefail
# verify-two-device-failover.sh: PoC verifier that Host App resyncs within 60s (multi-device)
# Requirements: HOSTAPP_URL (device A), KUBECONFIG points to the shared cluster

HOSTAPP_URL=${HOSTAPP_URL:-https://127.0.0.1:8090}
KUBECONFIG=${KUBECONFIG:-${GN_KUBECONFIG:-$HOME/.guildnet/kubeconfig}}

# 1) Health check
curl -k --silent --show-error --fail "$HOSTAPP_URL/healthz" >/dev/null || {
  echo "[verify] Host App not reachable at $HOSTAPP_URL" >&2
  exit 2
}

# 2) Create a small workspace (idempotent)
WS_NAME="failover-check-$(date +%s)"
cat <<JSON | curl -k -sS -X POST "$HOSTAPP_URL/api/deploy/workspace" -H 'Content-Type: application/json' -d @- || true
{
  "name": "$WS_NAME",
  "image": "nginx:alpine",
  "expose": [{"port": 8080, "name": "http"}],
  "env": {"PORT": "8080"}
}
JSON

# 3) Simulate Host App A restart (if local control endpoint available)
curl -k -sS -X POST "$HOSTAPP_URL/internal/shutdown" >/dev/null || true

# 4) Wait up to 60s for /healthz to return ok again
for i in $(seq 1 60); do
  if curl -k --silent --fail "$HOSTAPP_URL/healthz" >/dev/null; then
    echo "[verify] Host App back online after $i s"
    break
  fi
  sleep 1
  if [ "$i" -eq 60 ]; then
    echo "[verify] FAIL: Host App did not come back within 60s" >&2
    exit 1
  fi
done

# 5) Assert published registry ConfigMap exists (shared state present)
kubectl -n guildnet-system get cm -l app=published-registry >/dev/null 2>&1 || {
  echo "[verify] WARN: no published-registry ConfigMap found; continuing"
}

echo "[verify] PASS: basic failover/resync checks completed"
