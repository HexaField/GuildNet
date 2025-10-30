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

echo "Build complete. Load/push using k0s/DinD paths:"
echo " - make dind-image-push (to push from DinD to a registry)"
echo " - or import into the k0s containerd via ctr inside the k0s container if needed"
exit 0
