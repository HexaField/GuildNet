#!/usr/bin/env bash
set -euo pipefail
# Bring up a MicroK8s cluster and write kubeconfig to ~/.guildnet/kubeconfig
# - Linux: installs/enables microk8s via snap (requires sudo)
# - macOS: uses Multipass VM named 'guildnet-mk8s' and installs microk8s inside
# Addons: dns, storage, metrics-server; optional metallb via MK8S_METALLB_RANGE
# Env:
#  GN_STATE_DIR (~/.guildnet), GN_KUBECONFIG (defaults to ~/.guildnet/kubeconfig)
#  MK8S_METALLB_RANGE (e.g., 192.168.64.240-192.168.64.250)
#  MK8S_VM_CPUS/MK8S_VM_MEM/MK8S_VM_DISK (for macOS multipass)

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
[ -f "$ROOT/.env" ] && . "$ROOT/.env"
GN_STATE_DIR=${GN_STATE_DIR:-"$HOME/.guildnet"}
GN_KUBECONFIG=${GN_KUBECONFIG:-"$GN_STATE_DIR/kubeconfig"}
MK8S_METALLB_RANGE=${MK8S_METALLB_RANGE:-}
OS=$(uname -s)

mkdir -p "$(dirname "$GN_KUBECONFIG")"

log(){ printf '[microk8s-up] %s\n' "$*"; }
need(){ command -v "$1" >/dev/null 2>&1 || { echo "Missing: $1" >&2; exit 2; }; }

if [ "$OS" = "Darwin" ]; then
  # macOS path: Multipass VM
  need multipass
  VM= guildnet-mk8s
  VM_NAME=${MK8S_VM_NAME:-guildnet-mk8s}
  CPUS=${MK8S_VM_CPUS:-4}
  MEM=${MK8S_VM_MEM:-8G}
  DISK=${MK8S_VM_DISK:-40G}
  if ! multipass list --format csv | cut -d, -f1 | grep -qx "$VM_NAME"; then
    log "launching multipass VM $VM_NAME (cpus=$CPUS, mem=$MEM, disk=$DISK)"
    multipass launch --name "$VM_NAME" --cpus "$CPUS" --mem "$MEM" --disk "$DISK"
  else
    STATE=$(multipass info "$VM_NAME" | awk -F': ' '/State/ {print $2}')
    if [ "$STATE" != "Running" ]; then
      log "starting VM $VM_NAME"
      multipass start "$VM_NAME"
    fi
  fi
  log "installing microk8s inside VM (snap)"
  multipass exec "$VM_NAME" -- bash -lc "sudo snap install microk8s --classic || true"
  log "waiting for microk8s ready"
  multipass exec "$VM_NAME" -- bash -lc "sudo microk8s status --wait-ready"
  log "enabling addons: dns storage metrics-server"
  multipass exec "$VM_NAME" -- bash -lc "sudo microk8s enable dns storage metrics-server || true"
  if [ -n "$MK8S_METALLB_RANGE" ]; then
    log "enabling metallb: $MK8S_METALLB_RANGE"
    multipass exec "$VM_NAME" -- bash -lc "yes | sudo microk8s enable metallb:$MK8S_METALLB_RANGE || true"
  else
    log "metallb skipped (MK8S_METALLB_RANGE not set)"
  fi
  log "fetching kubeconfig"
  multipass exec "$VM_NAME" -- sudo microk8s config > "$GN_KUBECONFIG"
  chmod 600 "$GN_KUBECONFIG"
  log "kubeconfig written: $GN_KUBECONFIG"
  exit 0
else
  # Linux path: Snap
  if ! command -v microk8s >/dev/null 2>&1; then
    need sudo
    log "installing microk8s via snap (requires sudo)"
    sudo snap install microk8s --classic
  fi
  log "waiting for microk8s ready"
  if command -v sudo >/dev/null 2>&1; then
    sudo microk8s status --wait-ready
  else
    microk8s status --wait-ready
  fi
  log "enabling addons: dns storage metrics-server"
  if command -v sudo >/dev/null 2>&1; then
    sudo microk8s enable dns storage metrics-server || true
  else
    microk8s enable dns storage metrics-server || true
  fi
  if [ -n "$MK8S_METALLB_RANGE" ]; then
    log "enabling metallb: $MK8S_METALLB_RANGE"
    if command -v sudo >/dev/null 2>&1; then
      yes | sudo microk8s enable metallb:$MK8S_METALLB_RANGE || true
    else
      yes | microk8s enable metallb:$MK8S_METALLB_RANGE || true
    fi
  else
    log "metallb skipped (MK8S_METALLB_RANGE not set)"
  fi
  log "fetching kubeconfig"
  if command -v sudo >/dev/null 2>&1; then
    sudo microk8s config > "$GN_KUBECONFIG"
  else
    microk8s config > "$GN_KUBECONFIG"
  fi
  chmod 600 "$GN_KUBECONFIG"
  log "kubeconfig written: $GN_KUBECONFIG"
fi
