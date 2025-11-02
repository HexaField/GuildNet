#!/usr/bin/env bash
set -euo pipefail

# smoke-workspace.sh
# Build a tiny test image (optional), ensure CRDs/operator, and deploy a Workspace CR from template.
# Relies on scripts/quick-workspace.yaml.tmpl and emits basic status.

ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
GN_KUBECONFIG=${GN_KUBECONFIG:-"$HOME/.guildnet/kubeconfig"}
export KUBECONFIG="$GN_KUBECONFIG"

NS=${GN_WORKSPACE_NS:-default}
NAME=${GN_WORKSPACE_NAME:-ws-smoke}
IMAGE=${GN_WORKSPACE_IMAGE:-nginxinc/nginx-unprivileged:1.25}
PORT=${GN_WORKSPACE_PORT:-8080}

# 0) Verify kube API
kubectl --request-timeout=5s get --raw='/readyz?verbose' >/dev/null

# 1) Ensure CRDs/operator present (idempotent)
make -C "$ROOT" deploy-k8s-addons >/dev/null || true
make -C "$ROOT" deploy-operator >/dev/null || true

# 2) Render template and apply
T="$ROOT/scripts/quick-workspace.yaml.tmpl"
if [ ! -f "$T" ]; then
  echo "[smoke-workspace] missing template: $T" >&2
  exit 2
fi
Y="/tmp/ws-${NAME}-$(date +%s).yaml"
sed -e "s#{{NAME}}#$NAME#g" \
    -e "s#{{NAMESPACE}}#$NS#g" \
    -e "s#{{IMAGE}}#$IMAGE#g" \
    -e "s#{{PORT}}#$PORT#g" \
    "$T" > "$Y"

kubectl apply -f "$Y"

# 3) Show CR and related k8s objects
sleep 2
kubectl -n "$NS" get workspace "$NAME" -o yaml || true
kubectl -n "$NS" get deploy,svc,ingress -l workspace="$NAME" || true

echo "[smoke-workspace] applied: $Y"
exit 0
