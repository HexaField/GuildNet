#!/usr/bin/env bash
set -euo pipefail

# remote-run-verify-federation.sh
# Runs on the remote machine (FED_REMOTE) to prepare microk8s, build and deploy the operator and required infra.
# This script assumes it is executed from the repo root on the remote machine.

REPO_ROOT="$(pwd)"

echo "Remote repo root: $REPO_ROOT"

# Install microk8s if needed (best-effort; assume snap exists on Ubuntu)
if ! command -v microk8s >/dev/null 2>&1; then
  echo "microk8s not found; please install microk8s on remote host and re-run."
  exit 1
fi

# Ensure microk8s is running
sudo microk8s status --wait-ready

# Enable classic services required: dns, storage, metallb (we expect metallb config in k8s/)
sudo microk8s enable dns storage

# Apply metallb config if present
if [ -f "$REPO_ROOT/k8s/metallb-example.yaml" ]; then
  sudo microk8s kubectl apply -f $REPO_ROOT/k8s/metallb-example.yaml || true
fi

# Build and load operator image (if Makefile supports it); otherwise build and apply manifests
if make -n agent-build >/dev/null 2>&1; then
  echo "Building operator images and loading into microk8s (if supported)"
  make agent-build || true
fi

# Run basic verify-e2e steps on remote (deploy rethinkdb, tailscale DaemonSet, etc.)
# We reuse scripts/verify-cluster.sh and scripts/deploy-operator.sh if present
if [ -f "$REPO_ROOT/scripts/verify-cluster.sh" ]; then
  bash scripts/verify-cluster.sh || true
fi

# If operator manifests exist, apply them
if [ -f "$REPO_ROOT/k8s/hostapp-deploy-patch.yaml" ]; then
  sudo microk8s kubectl apply -f $REPO_ROOT/k8s/hostapp-deploy-patch.yaml || true
fi

# Ensure remote cluster kubeconfig is accessible to the test orchestrator (we assume SSH tunnel or Tailscale will provide connectivity)
echo "Remote cluster prepared."

