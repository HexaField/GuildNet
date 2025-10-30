#!/usr/bin/env bash
set -euo pipefail

# k0s-node-up.sh
# Bring up a Docker-only node stack for GuildNet:
# - k0s (controller+worker) inside a privileged container (single-node)
# - optional Tailscale container (if TS_AUTHKEY provided)
# - Docker-in-Docker (DinD) for local image builds (optional but enabled by default)
# - Emit kubeconfig to ~/.guildnet/kubeconfig for Host App and kubectl use
#
# Notes:
# - This script prefers safe, single-purpose commands to avoid CLI stalls.
# - It binds the API server to 127.0.0.1:16443 by default for local access.
#   Tailnet exposure can be layered via a router/serve step in a follow-up.

ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
LOG=${LOGFILE:-/tmp/k0s-node-up-$(date +%s).log}

# Configurable env (sane defaults)
K0S_IMAGE=${K0S_IMAGE:-k0sproject/k0s:latest}
DIND_IMAGE=${DIND_IMAGE:-docker:24-dind}
TAILSCALE_IMAGE=${TAILSCALE_IMAGE:-tailscale/tailscale:stable}
GN_STATE_DIR=${GN_STATE_DIR:-"$HOME/.guildnet"}
GN_K0S_DIR=${GN_K0S_DIR:-"$GN_STATE_DIR/k0s"}
GN_KUBECONFIG=${GN_KUBECONFIG:-"$HOME/.guildnet/kubeconfig"}
K0S_HOST_PORT=${K0S_HOST_PORT:-16443}

# CIDRs (used by tailscale routes if enabled)
K0S_POD_CIDR=${K0S_POD_CIDR:-10.244.0.0/16}
K0S_SVC_CIDR=${K0S_SVC_CIDR:-10.96.0.0/12}
TS_ROUTES=${TS_ROUTES:-"$K0S_POD_CIDR,$K0S_SVC_CIDR"}
TS_HOSTNAME=${TS_HOSTNAME:-"gn-node-$(hostname | tr '[:upper:]' '[:lower:]')"}

mkdir -p "$GN_K0S_DIR" "$GN_K0S_DIR/kubelet" "$GN_K0S_DIR/containerd" "$GN_K0S_DIR/tailscale" "$GN_K0S_DIR/dind"
mkdir -p "$(dirname "$GN_KUBECONFIG")"

need() { command -v "$1" >/dev/null 2>&1; }

if ! need docker; then
  echo "[k0s-node-up] docker not found on PATH" | tee -a "$LOG"; exit 2
fi

# 1) Optional Tailscale (only if TS_AUTHKEY is provided)
if [ -n "${TS_AUTHKEY:-}" ]; then
  echo "[k0s-node-up] starting tailscale container" | tee -a "$LOG"
  docker rm -f guildnet-tailscale >/dev/null 2>&1 || true
  docker run -d \
    --name guildnet-tailscale \
    --cap-add NET_ADMIN --cap-add NET_RAW \
    --device /dev/net/tun \
    --network host \
    -v "$GN_K0S_DIR/tailscale:/var/lib/tailscale" \
    -e TS_AUTHKEY="$TS_AUTHKEY" \
    -e TS_LOGIN_SERVER="${TS_LOGIN_SERVER:-}" \
    -e TS_ROUTES="$TS_ROUTES" \
    -e TS_HOSTNAME="$TS_HOSTNAME" \
    "$TAILSCALE_IMAGE" \
    sh -c "set -e; /usr/sbin/tailscaled --state=/var/lib/tailscale/tailscaled.state & sleep 2; tailscale up --authkey=\"$TS_AUTHKEY\" ${TS_LOGIN_SERVER:+--login-server=\"$TS_LOGIN_SERVER\"} --advertise-routes=\"$TS_ROUTES\" --hostname=\"$TS_HOSTNAME\" --accept-routes; tail -f /dev/null" \
    >/dev/null
else
  echo "[k0s-node-up] TS_AUTHKEY not set; skipping tailscale container (you can add it later)" | tee -a "$LOG"
fi

# 2) k0s single-node controller+worker
# Bind API server for local access only by default: 127.0.0.1:16443 -> container:6443
# (Tailnet exposure will be layered via router/serve in subsequent phases.)

echo "[k0s-node-up] starting k0s container" | tee -a "$LOG"
docker rm -f guildnet-k0s >/dev/null 2>&1 || true

# Select a free localhost port for kube-apiserver if the default is busy
pick_port() {
  local p="$1"; local max=$((p+10));
  while [ "$p" -le "$max" ]; do
    if command -v ss >/dev/null 2>&1; then
      if ! ss -ltn 2>/dev/null | awk '/LISTEN/ {print}' | grep -q ":${p}\\b"; then echo "$p"; return; fi
    elif command -v lsof >/dev/null 2>&1; then
      if ! lsof -nP -iTCP:${p} -sTCP:LISTEN >/dev/null 2>&1; then echo "$p"; return; fi
    else
      # last resort: try to bind with nc
      if ! (echo >/dev/tcp/127.0.0.1/${p}) >/dev/null 2>&1; then echo "$p"; return; fi
    fi
    p=$((p+1))
  done
  echo "$1"
}
K0S_HOST_PORT=$(pick_port "$K0S_HOST_PORT")
echo "[k0s-node-up] using host API port: $K0S_HOST_PORT" | tee -a "$LOG"

# Optional: Generate a minimal k0s config to inject API cert SANs (e.g., tailscale IP)
CONFIG_ARG=""
if [ "${TS_ADD_SANS:-0}" != "0" ]; then
  echo "[k0s-node-up] preparing k0s config with API cert SANs" | tee -a "$LOG"
  TAIL_IP=""
  if [ -n "${TS_AUTHKEY:-}" ] && docker ps --format '{{.Names}}' | grep -q '^guildnet-tailscale$'; then
    TAIL_IP=$(docker exec guildnet-tailscale tailscale ip -4 2>/dev/null | sed -n '1p' | tr -d '\r' || true)
  fi
  HN_SHORT=$(hostname -s 2>/dev/null || hostname 2>/dev/null || echo "")
  HN_FULL=$(hostname -f 2>/dev/null || echo "")
  cat >"$GN_K0S_DIR/k0s.yaml" <<YAML
apiVersion: k0s.k0sproject.io/v1beta1
kind: Cluster
metadata:
  name: guildnet
spec:
  api:
    port: 6443
    sans:
      - 127.0.0.1
      - localhost
      - ${HN_SHORT}
      - ${HN_FULL}
YAML
  if [ -n "$TAIL_IP" ]; then
    printf "      - %s\n" "$TAIL_IP" >>"$GN_K0S_DIR/k0s.yaml"
  fi
  CONFIG_ARG="--config /var/lib/k0s/k0s.yaml"
fi

docker run -d \
  --name guildnet-k0s \
  --privileged \
  --cgroupns=host \
  -v "$GN_K0S_DIR:/var/lib/k0s" \
  -v "$GN_K0S_DIR/kubelet:/var/lib/kubelet" \
  -v "$GN_K0S_DIR/containerd:/var/lib/containerd" \
  -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
  -p 127.0.0.1:${K0S_HOST_PORT}:6443 \
  --entrypoint /bin/sh \
  "$K0S_IMAGE" \
  -c "set -e; mkdir -p /var/lib/k0s; touch /var/lib/k0s/k0s.log; \
    k0s controller --single --data-dir /var/lib/k0s --enable-worker ${CONFIG_ARG} >>/var/lib/k0s/k0s.log 2>&1 & \
    tail -F /var/lib/k0s/k0s.log" \
  >/dev/null

# Wait for API server readiness (best-effort)
READY=0
for i in $(seq 1 90); do
  if docker exec guildnet-k0s /bin/sh -c "k0s kubectl --kubeconfig=/var/lib/k0s/pki/admin.conf get --raw=/readyz" >/dev/null 2>&1; then
    READY=1; break
  fi
  sleep 2
  echo "[k0s-node-up] waiting for kube-apiserver ready ($i)" | tee -a "$LOG"
done

# 3) Emit kubeconfig
# Use admin.conf if k0s kubeconfig helper is unavailable.
TMP_KC=$(mktemp)
if docker exec guildnet-k0s /bin/sh -c "k0s kubeconfig admin >/tmp/admin.kubeconfig" >/dev/null 2>&1; then
  docker cp guildnet-k0s:/tmp/admin.kubeconfig "$TMP_KC" 2>/dev/null || true
else
  docker cp guildnet-k0s:/var/lib/k0s/pki/admin.conf "$TMP_KC" 2>/dev/null || true
fi

if [ -s "$TMP_KC" ]; then
  # Rewrite server to selected localhost port for local access
  sed -E "s#(server:).*#\\1 https://127.0.0.1:${K0S_HOST_PORT}#g" "$TMP_KC" > "$GN_KUBECONFIG"
  chmod 600 "$GN_KUBECONFIG"
  echo "[k0s-node-up] wrote kubeconfig: $GN_KUBECONFIG" | tee -a "$LOG"
else
  echo "[k0s-node-up] WARNING: failed to retrieve kubeconfig from container" | tee -a "$LOG"
fi
rm -f "$TMP_KC" || true

# 3b) Optionally expose kube-API over the tailnet using tailscale 'serve tcp'
if [ -n "${TS_AUTHKEY:-}" ] && [ "${TS_SERVE_KUBEAPI:-0}" != "0" ]; then
  echo "[k0s-node-up] configuring tailscale serve tcp for kube-API" | tee -a "$LOG"
  PORT_LOCAL="$K0S_HOST_PORT" PORT_TAIL="${TS_SERVE_PORT:-$K0S_HOST_PORT}" \
    "$ROOT/scripts/ts-serve-kubeapi.sh" 2>&1 | tee -a "$LOG" || true
fi

# 4) Docker-in-Docker (for image builds)
echo "[k0s-node-up] starting DinD container" | tee -a "$LOG"
docker rm -f guildnet-dind >/dev/null 2>&1 || true
TLS_ARGS="-e DOCKER_TLS_CERTDIR="
if [ "${DIND_TLS:-0}" != "0" ]; then
  mkdir -p "$GN_K0S_DIR/dind-certs"
  TLS_ARGS="-e DOCKER_TLS_CERTDIR=/certs -v $GN_K0S_DIR/dind-certs:/certs"
fi
PORT_ARGS="-p 127.0.0.1:2375:2375"
if [ "${DIND_TLS:-0}" != "0" ]; then
  PORT_ARGS="-p 127.0.0.1:2376:2376"
fi
docker run -d \
  --name guildnet-dind \
  --privileged \
  -v "$GN_K0S_DIR/dind:/var/lib/docker" \
  $PORT_ARGS \
  $TLS_ARGS \
  "$DIND_IMAGE" \
  >/dev/null

# Emit a helper env file for connecting to DinD from the host
DINDF="$GN_STATE_DIR/dind-env.sh"
{
  echo "# Source this file to point docker client to the DinD daemon"
  echo "export DOCKER_HOST=tcp://127.0.0.1:2375"
  if [ "${DIND_TLS:-0}" != "0" ]; then
    echo "export DOCKER_HOST=tcp://127.0.0.1:2376"
    echo "export DOCKER_CERT_PATH=$GN_K0S_DIR/dind-certs/client"
    echo "export DOCKER_TLS_VERIFY=1"
  fi
} > "$DINDF"
chmod 600 "$DINDF"
echo "[k0s-node-up] wrote DinD env helper: $DINDF" | tee -a "$LOG"

echo "[k0s-node-up] done. kubectl context: $GN_KUBECONFIG" | tee -a "$LOG"
if [ "$READY" -eq 1 ]; then
  echo "[k0s-node-up] kube-apiserver is Ready" | tee -a "$LOG"
else
  echo "[k0s-node-up] kube-apiserver readiness not confirmed; it may still be starting" | tee -a "$LOG"
fi

exit 0
