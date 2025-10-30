#!/usr/bin/env bash
set -euo pipefail

# Deprecated: MicroK8s flow removed. This repository is k0s-in-Docker only.
echo "[microk8s-setup] This helper is deprecated. Use scripts/k0s-node-up.sh instead." >&2
echo "[microk8s-setup] Example: bash scripts/k0s-node-up.sh (emits ~/.guildnet/kubeconfig)" >&2
exit 2

if ! try_status; then
  echolog "Starting microk8s (may require sudo)"
  sudo microk8s start 2>&1 | tee -a "$LOGFILE" || echolog "microk8s start returned non-zero"
  echolog "Waiting for microk8s to become ready"
  # If status fails due to permissions, attempt to fix permissions and retry
  OUT=$(sudo microk8s status --wait-ready 2>&1 || true)
  if echo "$OUT" | grep -qi "Insufficient permissions to access MicroK8s"; then
    echolog "Detected MicroK8s permission issue; attempting auto-fix: adding $USER to microk8s group and chowning ~/.kube"
    if sudo usermod -a -G microk8s "$USER"; then
      echolog "Added $USER to microk8s group"
    else
      echolog "Failed to add $USER to microk8s group"
    fi
    if sudo test -d "$HOME/.kube"; then
      sudo chown -R "$USER" "$HOME/.kube" || echolog "chown ~/.kube failed"
    fi
    echolog "Retrying microk8s status after applying permission fixes"
    # Give groups a moment; newgrp won't affect this non-interactive shell, but sudo commands will work
    OUT2=$(sudo microk8s status --wait-ready 2>&1 || true)
    echolog "$OUT2"
  else
    echolog "$OUT"
  fi
fi

echolog "Enabling recommended addons: dns, storage"
sudo microk8s enable dns storage 2>&1 | tee -a "$LOGFILE" || echolog "microk8s enable returned non-zero"

if command -v kubectl >/dev/null 2>&1; then
  echolog "microk8s kubectl available"
fi

# Compose kubeconfig path to emit
OUT_KUBECONFIG=${1:-${GN_KUBECONFIG:-$HOME/.guildnet/kubeconfig}}
mkdir -p "$(dirname "$OUT_KUBECONFIG")"

echolog "Writing microk8s kubeconfig to $OUT_KUBECONFIG"
# Use sudo to read microk8s config when running as non-microk8s user
sudo microk8s config > "$OUT_KUBECONFIG" || microk8s config > "$OUT_KUBECONFIG" || echolog "Failed to write kubeconfig from microk8s"

# Optionally replace server host with TAILSCALE_IP or KUBE_API_SERVER_OVERRIDE so other machines can reach the API
  if [ -n "${KUBE_API_SERVER_OVERRIDE:-}" ] || [ -n "${TAILSCALE_IP:-}" ]; then
  HOST=${KUBE_API_SERVER_OVERRIDE:-${TAILSCALE_IP}}
  # If HOST looks like an IP, set port 16443 for microk8s
  if [[ "$HOST" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    NEW_SERVER="https://$HOST:16443"
  else
    NEW_SERVER="$HOST"
  fi
  echolog "Replacing API server URL in kubeconfig with $NEW_SERVER"
  # Use yq if available, otherwise sed replace server line
  if command -v yq >/dev/null 2>&1; then
    yq eval ".clusters[0].cluster.server = \"$NEW_SERVER\"" -i "$OUT_KUBECONFIG"
  else
    sed -i -E "s#(server:).*#\1 $NEW_SERVER#g" "$OUT_KUBECONFIG" || true
  fi
fi

echolog "microk8s setup complete; kubeconfig at $OUT_KUBECONFIG"
echo "$OUT_KUBECONFIG"
