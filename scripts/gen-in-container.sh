#!/usr/bin/env bash
set -euo pipefail

# Run controller-gen inside a pinned Go container and write generated artifacts
# Usage: ./scripts/gen-in-container.sh

ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
echo "Using root: $ROOT_DIR"

IMAGE=golang:1.23-bullseye
CGEN_VER=v0.12.0

docker run --rm -v "$ROOT_DIR":/workspace -w /workspace $IMAGE bash -lc '
  set -euo pipefail
  export PATH=/usr/local/go/bin:$PATH
  echo "go version:"; go version
  go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.12.0
  export PATH="$PATH:$(go env GOPATH)/bin"
  echo "running controller-gen object..."
  controller-gen object:headerFile=./hack/boilerplate.go.txt paths=./api/...
  echo "running controller-gen crd..."
  controller-gen crd:crdVersions=v1 paths=./api/... output:crd:dir=./config/crd/bases
'

echo "controller-gen run complete"

chmod +x "$ROOT_DIR/scripts/gen-in-container.sh" || true
