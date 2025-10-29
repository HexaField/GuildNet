#!/usr/bin/env bash
set -euo pipefail

# ts-serve-kubeapi.sh
# Make the local kube-apiserver (bound on 127.0.0.1:16443) available over the tailnet via tailscale 'serve tcp'.
# Assumes the tailscale container (guildnet-tailscale) runs with --network host and is logged in.

PORT_LOCAL=${PORT_LOCAL:-16443}
PORT_TAIL=${PORT_TAIL:-16443}

# Ensure container exists
if ! docker ps --format '{{.Names}}' | grep -q '^guildnet-tailscale$'; then
  echo "[ts-serve-kubeapi] guildnet-tailscale container not running" >&2
  exit 2
fi

# Configure tailscale serve tcp
set -x
docker exec -i guildnet-tailscale tailscale serve tcp "$PORT_TAIL" 127.0.0.1:"$PORT_LOCAL"
set +x

echo "[ts-serve-kubeapi] kube-API served over tailnet on tcp:$PORT_TAIL"
