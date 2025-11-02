#!/usr/bin/env bash
set -euo pipefail
# Tear down MicroK8s cluster
# - macOS: delete Multipass VM
# - Linux: reset microk8s and stop (optionally remove)

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
[ -f "$ROOT/.env" ] && . "$ROOT/.env"
OS=$(uname -s)
log(){ printf '[microk8s-down] %s\n' "$*"; }

if [ "$OS" = "Darwin" ]; then
  VM_NAME=${MK8S_VM_NAME:-guildnet-mk8s}
  if multipass list --format csv | cut -d, -f1 | grep -qx "$VM_NAME"; then
    log "deleting multipass VM $VM_NAME"
    multipass delete "$VM_NAME" || true
    multipass purge || true
  else
    log "no VM named $VM_NAME found"
  fi
else
  if command -v microk8s >/dev/null 2>&1; then
    log "resetting microk8s"
    sudo microk8s reset || true
    log "stopping microk8s"
    sudo microk8s stop || true
    if [ "${REMOVE_MICROK8S:-0}" != "0" ]; then
      log "removing microk8s snap"
      sudo snap remove microk8s || true
    fi
  else
    log "microk8s not installed"
  fi
fi
