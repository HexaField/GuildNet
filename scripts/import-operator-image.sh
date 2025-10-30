#!/usr/bin/env bash
set -euo pipefail

echo "[import-operator-image] Deprecated: MicroK8s import path removed."
echo "Use Docker registry push (make dind-image-push) or import into the k0s containerd via ctr inside the k0s container if required."
exit 2
