#!/usr/bin/env bash
set -euo pipefail

echo "[node-migrate] Deprecated: migration helpers removed. This repository is k0s-in-Docker only." >&2
echo "[node-migrate] Use scripts/k0s-node-up.sh to provision local k0s and Makefile targets for addons/operator." >&2
exit 2
