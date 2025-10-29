#!/usr/bin/env bash
set -euo pipefail

# k0s-node-down.sh
# Stop the Docker-only node stack for GuildNet (k0s, tailscale, DinD).
# By default this does NOT remove persisted state under ~/.guildnet/k0s.
# Pass --purge to delete state (destructive).

GN_STATE_DIR=${GN_STATE_DIR:-"$HOME/.guildnet"}
GN_K0S_DIR=${GN_K0S_DIR:-"$GN_STATE_DIR/k0s"}
PURGE=0

if [ "${1:-}" = "--purge" ] || [ "${1:-}" = "-p" ]; then
  PURGE=1
fi

stop() { 
  local name="$1"; 
  docker rm -f "$name" >/dev/null 2>&1 || true; 
}

stop guildnet-dind
stop guildnet-k0s
stop guildnet-tailscale

echo "[k0s-node-down] containers stopped (guildnet-*)"

if [ "$PURGE" -eq 1 ]; then
  echo "[k0s-node-down] PURGE requested: removing $GN_K0S_DIR"
  rm -rf "$GN_K0S_DIR"
fi

echo "[k0s-node-down] done"
