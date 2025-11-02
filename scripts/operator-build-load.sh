#!/usr/bin/env bash
set -euo pipefail
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
[ -f "$ROOT/.env" ] && . "$ROOT/.env"
OP_IMAGE=${OPERATOR_IMAGE:-ghcr.io/your/module/hostapp:latest}

if ! command -v docker >/dev/null 2>&1; then
  echo "docker required"; exit 2
fi

echo "Building operator image: $OP_IMAGE"
docker build -f "$ROOT/scripts/Dockerfile.operator" -t "$OP_IMAGE" "$ROOT"

echo "Build complete. Ensure the image is available to your cluster:"
echo " - Prefer pushing to a registry accessible by your cluster (e.g., GHCR, Docker Hub)."
echo " - If using a local MicroK8s, configure image pulls from your registry as needed."
exit 0
