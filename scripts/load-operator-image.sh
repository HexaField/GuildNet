#!/usr/bin/env bash
set -euo pipefail

echo "[load-operator-image] Deprecated: MicroK8s import path removed."
echo "Use Docker-in-Docker + registry push (make dind-image-push) or import into k0s containerd manually via ctr if needed."
exit 2
