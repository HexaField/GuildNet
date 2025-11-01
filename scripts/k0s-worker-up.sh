#!/usr/bin/env bash
set -euo pipefail

# k0s-worker-up.sh
# Bring up a k0s worker inside a privileged Docker container and join a controller using a token file.
#
# Usage:
#   scripts/k0s-worker-up.sh --token-file /path/to/k0s-worker.token --state-dir /tmp/guildnet/k0s-worker [--image k0sproject/k0s:latest] [--host-network 0|1]
#
# Notes:
# - Uses /run/systemd/resolve bind-mount if present to satisfy CNI/kubelet resolv.conf expectations.
# - Writes logs under <state-dir>/k0s.log inside the container as /var/lib/k0s/k0s.log.
# - Default networking is host network (compatibility with CNI/iptables). If ports 10248/10250 are busy on the host,
#   pass --host-network 0 to run with normal container networking and avoid port conflicts.

IMAGE="k0sproject/k0s:latest"
TOKEN_FILE=""
STATE_DIR=""
HOST_NETWORK=1

while [[ $# -gt 0 ]]; do
  case "$1" in
    --token-file)
      TOKEN_FILE="$2"; shift 2 ;;
    --state-dir)
      STATE_DIR="$2"; shift 2 ;;
    --image)
      IMAGE="$2"; shift 2 ;;
    --host-network)
      HOST_NETWORK="$2"; shift 2 ;;
    *) echo "Unknown arg: $1" >&2; exit 2 ;;
  esac
done

if [[ -z "$STATE_DIR" ]]; then echo "--state-dir is required" >&2; exit 2; fi
if [[ -n "$TOKEN_FILE" && ! -s "$TOKEN_FILE" ]]; then echo "token file not found: $TOKEN_FILE" >&2; exit 2; fi

mkdir -p "$STATE_DIR" "$STATE_DIR/kubelet" "$STATE_DIR/containerd"
if [[ -n "$TOKEN_FILE" ]]; then
  cp -f "$TOKEN_FILE" "$STATE_DIR/token"
fi

docker rm -f guildnet-k0s-worker >/dev/null 2>&1 || true

RESOLVE_MOUNT=""
if [[ -d /run/systemd/resolve ]]; then
  RESOLVE_MOUNT="-v /run/systemd/resolve:/run/systemd/resolve:ro"
fi

set -x
HN=$(hostname -s 2>/dev/null || hostname)
NET_ARGS="--network host"
if [[ "$HOST_NETWORK" == "0" ]]; then
  NET_ARGS=""
fi
docker run -d \
  --name guildnet-k0s-worker \
  --privileged \
  --cgroupns=host \
  $NET_ARGS \
  -h "$HN" \
  -v /lib/modules:/lib/modules:ro \
  -v "$STATE_DIR/cni-bin:/opt/cni/bin" \
  -v "$STATE_DIR/cni-conf:/etc/cni/net.d" \
  -v "$STATE_DIR:/var/lib/k0s" \
  -v "$STATE_DIR/kubelet:/var/lib/kubelet" \
  -v "$STATE_DIR/containerd:/var/lib/containerd" \
  -v /dev:/dev \
  -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
  $RESOLVE_MOUNT \
  --entrypoint sleep \
  "$IMAGE" \
  3650d

# Pre-create common hostPath targets expected by DaemonSets (e.g., kube-router)
docker exec guildnet-k0s-worker /bin/sh -lc "mkdir -p /etc/cni/net.d /opt/cni/bin /var/lib/k0s; [ -e /run/xtables.lock ] || touch /run/xtables.lock"

# Start the worker and detach the process to keep the container as the node OS
docker exec guildnet-k0s-worker /bin/sh -lc "k0s worker --token-file /var/lib/k0s/token --data-dir /var/lib/k0s >>/var/lib/k0s/k0s.log 2>&1 &"

echo "k0s worker started (container: guildnet-k0s-worker)"
