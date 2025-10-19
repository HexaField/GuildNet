#!/usr/bin/env bash
set -euo pipefail

# two-device-setup.sh: Automate two-device setup (host/joiner)
# Usage:
#   Host (Device A):
#     LISTEN_LOCAL="0.0.0.0:8090" ./scripts/two-device-setup.sh host
#   Joiner (Device B):
#     HOSTAPP_URL="https://<deviceA-tailnet-ip>:8090" ./scripts/two-device-setup.sh joiner
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

  # Kubernetes (microk8s helper)
  if [[ ! -f "$HOME/.guildnet/kubeconfig" ]]; then
    echo "[host] setting up microk8s and kubeconfig"
    bash ./scripts/microk8s-setup.sh "$HOME/.guildnet/kubeconfig"
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
  echo "[host] To attach this device's cluster elsewhere, POST guildnet.config to /bootstrap."
  echo "[host] Example (from another device):"
  echo "  curl -k -X POST \"https://<HOSTAPP_URL>/bootstrap\" -F \"file=@guildnet.config\""
  exit 0
fi

# Joiner mode
echo "[joiner] installing tailscale and bringing it up"
make setup-tailscale || true

# Kubernetes (microk8s helper)
if [[ ! -f "$HOME/.guildnet/kubeconfig" ]]; then
  echo "[joiner] setting up microk8s and kubeconfig"
  bash ./scripts/microk8s-setup.sh "$HOME/.guildnet/kubeconfig"
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