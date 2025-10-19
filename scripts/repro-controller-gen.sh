#!/usr/bin/env bash
set -euo pipefail
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
OUT=/tmp/controller-gen-repro
mkdir -p "$OUT"
cd "$ROOT"

VERS=(v0.11.0 v0.12.0)
for v in "${VERS[@]}"; do
  echo "--- Testing controller-gen $v ---"
  # controller-gen installs to GOBIN/controller-gen (no version suffix)
  INST_BIN="$OUT/controller-gen"
  # remove any stale small files from previous attempts
  rm -f "$OUT/controller-gen" "$OUT/controller-gen-$v" || true
  echo "Installing controller-gen $v via 'go install' into $OUT"
  GOFLAGS= GOBIN="$OUT" go install "sigs.k8s.io/controller-tools/cmd/controller-gen@${v}" > "$OUT/install-${v}.log" 2>&1 || true
  echo "Running $INST_BIN (installed for $v) against ./api/... using Makefile-style args..."
  if [ -x "$INST_BIN" ]; then
    {
      set -x
      "$INST_BIN" object:headerFile=./hack/boilerplate.go.txt paths=./api/... || true
      "$INST_BIN" crd:crdVersions=v1 paths=./api/... output:crd:dir=./config/crd/bases || true
    } > "$OUT/controller-gen-${v}.log" 2>&1 || true
  else
    echo "Binary $INST_BIN not found or not executable; see install log $OUT/install-${v}.log" > "$OUT/controller-gen-${v}.log"
  fi
  echo "Log: $OUT/controller-gen-${v}.log"
  tail -n 300 "$OUT/controller-gen-${v}.log" || true
  echo
done

echo "Repro logs saved to $OUT"; ls -la "$OUT" || true
