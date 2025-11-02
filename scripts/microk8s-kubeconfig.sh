#!/usr/bin/env bash
set -euo pipefail
# Print MicroK8s kubeconfig to stdout (Linux) or from Multipass VM (macOS)
OS=$(uname -s)
if [ "$OS" = "Darwin" ]; then
  VM_NAME=${MK8S_VM_NAME:-guildnet-mk8s}
  multipass exec "$VM_NAME" -- sudo microk8s config
else
  if command -v sudo >/dev/null 2>&1; then
    sudo microk8s config
  else
    microk8s config
  fi
fi
