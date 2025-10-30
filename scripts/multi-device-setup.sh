#!/usr/bin/env bash
set -euo pipefail

# multi-device-setup.sh: Automate multi-device setup (host/joiner)
# Usage:
#   Host (Device A):
#     LISTEN_LOCAL="0.0.0.0:8090" ./scripts/multi-device-setup.sh host
#   Joiner (Device B):
#     HOSTAPP_URL="https://<deviceA-tailnet-ip>:8090" ./scripts/multi-device-setup.sh joiner
#
# Env knobs:
#   CLUSTER=default (cluster id label for helpers)
#   LISTEN_LOCAL (host only): e.g. 0.0.0.0:8090
#   HOSTAPP_URL (joiner): e.g. https://100.x.y.z:8090 (Device A tailscale IP)

MODE=${1:-}
if [[ -z "${MODE}" || ("${MODE}" != "host" && "${MODE}" != "joiner") ]]; then
  echo "Usage: $0 <host|joiner>" >&2
  exit 2
fi

ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT_DIR"

# Ensure tools
need() { command -v "$1" >/dev/null 2>&1 || { echo "Missing required tool: $1" >&2; exit 2; }; }
need bash
need curl

CLUSTER="${CLUSTER:-default}"

if [[ "$MODE" == "host" ]]; then
  echo "[host] starting Headscale (LAN bind)"
  make headscale-up
  echo "[host] bootstrapping Headscale user + preauth"
  make headscale-bootstrap
  echo "[host] installing tailscale router on host"
  make router-install || true
  make router-up || true

  # Kubernetes (k0s-in-Docker helper)
  if [[ ! -f "$HOME/.guildnet/kubeconfig" ]]; then
    echo "[host] bringing up k0s (Docker) and writing kubeconfig"
    TS_SERVE_KUBEAPI=${TS_SERVE_KUBEAPI:-0} TS_ADD_SANS=${TS_ADD_SANS:-0} bash ./scripts/k0s-node-up.sh
  fi

  echo "[host] deploying k8s addons (CRDs, MetalLB, RethinkDB)"
  make deploy-k8s-addons || true

  echo "[host] building + loading operator image"
  make operator-image-build || true
  make operator-image-load || true
  echo "[host] deploying operator"
  make deploy-operator || true

  echo "[host] preparing headscale namespace + keys for cluster=$CLUSTER"
  CLUSTER="$CLUSTER" make headscale-namespace
  echo "[host] ensuring tailscale router in cluster=$CLUSTER"
  CLUSTER="$CLUSTER" make router-ensure || true

  # Run Host App
  export LISTEN_LOCAL="${LISTEN_LOCAL:-0.0.0.0:8090}"
  echo "[host] running Host App on $LISTEN_LOCAL"
  make deploy-hostapp

  # Emit join config + instructions
  echo "[host] generating join file (guildnet.config) for convenience"
  make generate-join-config
  # Suggest HOSTAPP_URL candidates
  LAN_IP=$(hostname -I | awk '{print $1}') || true
  TS_IP=$(command -v tailscale >/dev/null 2>&1 && tailscale ip -4 2>/dev/null | head -n1 || true)
  echo "[host] To attach this cluster from another device, POST guildnet.config to /bootstrap."
  if [[ -n "$TS_IP" ]]; then echo "[host] Tailscale URL candidate: https://$TS_IP:8090"; fi
  if [[ -n "$LAN_IP" ]]; then echo "[host] LAN URL candidate: https://$LAN_IP:8090"; fi
  echo "[host] Example (from another device): curl -k -X POST 'https://<HOSTAPP_URL>/bootstrap' -F 'file=@guildnet.config'"
  exit 0
fi

# Joiner mode
echo "[joiner] installing tailscale and bringing it up"
make setup-tailscale || true

# Kubernetes (k0s-in-Docker helper)
if [[ ! -f "$HOME/.guildnet/kubeconfig" ]]; then
  echo "[joiner] bringing up k0s (Docker) and writing kubeconfig"
  TS_SERVE_KUBEAPI=${TS_SERVE_KUBEAPI:-0} TS_ADD_SANS=${TS_ADD_SANS:-0} bash ./scripts/k0s-node-up.sh
fi

echo "[joiner] deploying k8s addons (CRDs, MetalLB, RethinkDB)"
make deploy-k8s-addons || true

echo "[joiner] building + loading operator image"
make operator-image-build || true
make operator-image-load || true
echo "[joiner] deploying operator"
make deploy-operator || true

echo "[joiner] preparing headscale namespace + keys for cluster=$CLUSTER"
CLUSTER="$CLUSTER" make headscale-namespace
echo "[joiner] ensuring tailscale router in cluster=$CLUSTER"
CLUSTER="$CLUSTER" make router-ensure || true

echo "[joiner] generating join file: guildnet.config"
make generate-join-config

if [[ -z "${HOSTAPP_URL:-}" ]]; then
  # Try to auto-detect tailscale ip
  if command -v tailscale >/dev/null 2>&1; then
    HIP=$(tailscale ip -4 2>/dev/null | head -n1 || true)
    if [[ -n "$HIP" ]]; then HOSTAPP_URL="https://$HIP:8090"; fi
  fi
fi

if [[ -n "${HOSTAPP_URL:-}" ]]; then
  echo "[joiner] attaching cluster to Host App at: $HOSTAPP_URL"
  curl -k -X POST "$HOSTAPP_URL/bootstrap" -F "file=@guildnet.config" || {
    echo "[joiner] attach failed; you can retry manually with:" >&2
    echo "curl -k -X POST '$HOSTAPP_URL/bootstrap' -F 'file=@guildnet.config'" >&2
    exit 1
  }
  echo "[joiner] attach complete"
else
  echo "[joiner] HOSTAPP_URL not set; to attach, run on this device:"
  echo "  curl -k -X POST 'https://<deviceA-tailnet-ip>:8090/bootstrap' -F 'file=@guildnet.config'"
fi

echo "[joiner] done"
