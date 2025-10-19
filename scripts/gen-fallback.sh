#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
IMAGE=golang:1.23-bullseye
VERS=(v0.12.0 v0.11.0 v0.10.0)

echo "gen-fallback: attempting controller-gen inside container (versions: ${VERS[*]})"

for ver in "${VERS[@]}"; do
  echo "Trying controller-gen ${ver} inside ${IMAGE}..."
  if docker run --rm -v "$ROOT_DIR":/workspace -w /workspace $IMAGE bash -lc "
    set -euo pipefail
    export PATH=/usr/local/go/bin:$PATH
    go env GOPATH >/dev/null
    go install sigs.k8s.io/controller-tools/cmd/controller-gen@${ver}
    export PATH=\"\$PATH:\$(go env GOPATH)/bin\"
    echo 'running controller-gen object...'
    if controller-gen object:headerFile=./hack/boilerplate.go.txt paths=./api/...; then
      echo 'running controller-gen crd...'
      controller-gen crd:crdVersions=v1 paths=./api/... output:crd:dir=./config/crd/bases
      exit 0
    fi
  "; then
    echo "controller-gen ${ver} succeeded"
    exit 0
  else
    echo "controller-gen ${ver} failed, trying next version..."
  fi
done

echo "All containerized controller-gen attempts failed. Check source types for incompatibilities or run controller-gen locally. The minimal CRD stubs under config/crd/bases/ may not reflect API changes." >&2
exit 2
