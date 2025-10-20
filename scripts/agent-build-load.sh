#!/usr/bin/env bash
set -euo pipefail

# Usage:
#   scripts/agent-build-load.sh [image-tag]
# Default tag: agent:dev

TAG=${1:-agent:dev}

ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
IMG_NAME="${TAG}"

echo "Building image ${IMG_NAME} from images/agent..."
if docker buildx version >/dev/null 2>&1; then
	docker buildx build --load -t "${IMG_NAME}" "${ROOT_DIR}/images/agent"
else
	echo "docker buildx not available; using fallback docker build"
	docker build -t "${IMG_NAME}" "${ROOT_DIR}/images/agent"
fi

echo "Done."
