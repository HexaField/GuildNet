#!/usr/bin/env bash
set -euo pipefail

echo "[remote-run-verify-federation] This legacy helper targeted MicroK8s and is removed."
echo "Use k0s-in-Docker on the remote:"
echo "  scripts/k0s-node-up.sh && make deploy-k8s-addons deploy-operator ensure-operator-setup && nohup ./scripts/run-hostapp.sh &"
exit 2

