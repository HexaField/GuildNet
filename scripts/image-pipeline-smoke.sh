#!/usr/bin/env bash
set -euo pipefail

# image-pipeline-smoke.sh
# Build a tiny web image using the DinD daemon, import it into the k0s node's
# containerd, and deploy a Workspace using that image. This validates the
# "build -> load -> run" path without requiring a registry.
#
# Requirements:
# - guildnet-k0s container running (from scripts/k0s-node-up.sh)
# - guildnet-dind container running (from scripts/k0s-node-up.sh)
# - kubectl configured via $GN_KUBECONFIG
#
# Configurable env:
# - GN_KUBECONFIG: kubeconfig path (defaults to ~/.guildnet/kubeconfig)
# - GN_WORKSPACE_NS, GN_WORKSPACE_NAME, GN_WORKSPACE_PORT: override Workspace fields
# - GN_SMOKE_IMAGE: override image tag to build/use (default gn/smoke-app:local)

ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
GN_KUBECONFIG=${GN_KUBECONFIG:-"$HOME/.guildnet/kubeconfig"}
export KUBECONFIG="$GN_KUBECONFIG"

NS=${GN_WORKSPACE_NS:-default}
NAME=${GN_WORKSPACE_NAME:-ws-smoke}
PORT=${GN_WORKSPACE_PORT:-8080}
IMG=${GN_SMOKE_IMAGE:-gn/smoke-app:local}

need() { command -v "$1" >/dev/null 2>&1; }

if ! need docker; then
  echo "[image-pipeline-smoke] docker not found on PATH" >&2
  exit 2
fi

if ! docker ps --format '{{.Names}}' | grep -q '^guildnet-k0s$'; then
  echo "[image-pipeline-smoke] guildnet-k0s not running. Start with scripts/k0s-node-up.sh" >&2
  exit 2
fi
if ! docker ps --format '{{.Names}}' | grep -q '^guildnet-dind$'; then
  echo "[image-pipeline-smoke] guildnet-dind not running. Start with scripts/k0s-node-up.sh" >&2
  exit 2
fi

echo "[image-pipeline-smoke] kubeconfig: $GN_KUBECONFIG"
kubectl --request-timeout=5s get --raw='/readyz?verbose' >/dev/null

# 1) Prepare build context under /tmp and copy into DinD
TMPDIR=$(mktemp -d /tmp/gn-smoke-XXXXXX)
trap 'rm -rf "$TMPDIR"' EXIT
cat >"$TMPDIR/Dockerfile" <<'EOF'
FROM busybox:stable
EXPOSE 8080
RUN mkdir -p /www
COPY index.html /www/index.html
CMD ["sh","-c","httpd -f -p 8080 -h /www"]
EOF
cat >"$TMPDIR/index.html" <<'EOF'
<!doctype html>
<html><head><meta charset="utf-8"><title>GuildNet Smoke</title></head>
<body><h1>GuildNet Image Pipeline Smoke</h1><p>It works.</p></body></html>
EOF

echo "[image-pipeline-smoke] copying build context into DinD"
docker exec guildnet-dind sh -lc 'rm -rf /tmp/smoke && mkdir -p /tmp/smoke'
docker cp "$TMPDIR/." guildnet-dind:/tmp/smoke/

# 2) Build inside DinD
echo "[image-pipeline-smoke] building image $IMG inside DinD"
docker exec -e DOCKER_BUILDKIT=1 guildnet-dind sh -lc "docker build -t '$IMG' /tmp/smoke"

# 3) Stream-save from DinD -> import into k0s containerd
echo "[image-pipeline-smoke] importing image into k0s containerd"
set -o pipefail
docker exec guildnet-dind sh -lc "docker save '$IMG'" | docker exec -i guildnet-k0s sh -lc 'ctr -n k8s.io images import - >/dev/null'
set +o pipefail

# 4) Deploy Workspace using the built image
echo "[image-pipeline-smoke] deploying Workspace $NAME in ns=$NS using image=$IMG"
GN_WORKSPACE_IMAGE="$IMG" GN_WORKSPACE_NAME="$NAME" GN_WORKSPACE_NS="$NS" GN_WORKSPACE_PORT="$PORT" \
  bash "$ROOT/scripts/smoke-workspace.sh"

echo "[image-pipeline-smoke] waiting for rollout"
kubectl -n "$NS" rollout status deploy/"$NAME" --timeout=120s || true
kubectl -n "$NS" get svc "$NAME" -o wide || true

echo "[image-pipeline-smoke] DONE"
