#!/usr/bin/env bash
set -euo pipefail

# dind-registry-push.sh
# Push a local image from the GuildNet DinD daemon to a registry.
# - Sources ~/.guildnet/dind-env.sh to point the Docker client at DinD (2375 or 2376 when TLS)
# - Tags and pushes the image to the destination reference
#
# Usage:
#   scripts/dind-registry-push.sh --src <image:tag> --dest <registry/repo:tag> [--user <name>] [--pass <secret>] [--insecure]
#   env overrides: SRC_IMG, DEST_IMG, REGISTRY_USER, REGISTRY_PASS, DOCKER_INSECURE=1
#
# Examples:
#   # Push busybox:latest to ghcr.io/youruser/busy:latest
#   REGISTRY_USER=youruser REGISTRY_PASS=$GITHUB_TOKEN \
#     scripts/dind-registry-push.sh --src busybox:latest --dest ghcr.io/youruser/busy:latest
#
#   # Push local tag built in DinD (gn/smoke-app:local) to a private registry
#   REGISTRY_USER=me REGISTRY_PASS=secret \
#     scripts/dind-registry-push.sh --src gn/smoke-app:local --dest registry.local:5000/gn/smoke-app:local

SRC_IMG=${SRC_IMG:-}
DEST_IMG=${DEST_IMG:-}
REGISTRY_USER=${REGISTRY_USER:-}
REGISTRY_PASS=${REGISTRY_PASS:-}
DOCKER_INSECURE=${DOCKER_INSECURE:-0}

while [ $# -gt 0 ]; do
  case "$1" in
    --src) SRC_IMG="$2"; shift 2;;
    --dest) DEST_IMG="$2"; shift 2;;
    --user) REGISTRY_USER="$2"; shift 2;;
    --pass) REGISTRY_PASS="$2"; shift 2;;
    --insecure) DOCKER_INSECURE=1; shift;;
    -h|--help)
      echo "Usage: $0 --src <image:tag> --dest <registry/repo:tag> [--user <name>] [--pass <secret>] [--insecure]"; exit 0;;
    *) echo "unknown arg: $1"; exit 2;;
  esac
done

if [ -z "$SRC_IMG" ] || [ -z "$DEST_IMG" ]; then
  echo "ERROR: --src and --dest are required"; exit 2
fi

# Point docker client at DinD if helper exists
DIND_ENV="$HOME/.guildnet/dind-env.sh"
if [ -f "$DIND_ENV" ]; then
  # shellcheck disable=SC1090
  source "$DIND_ENV"
fi

# Optional insecure registry (useful for plain-http registries on LAN)
if [ "$DOCKER_INSECURE" = "1" ]; then
  export DOCKER_CLI_EXPERIMENTAL=enabled
  echo "[dind-push] WARNING: insecure registry mode enabled (client-side); ensure your registry allows HTTP"
fi

# Verify source image exists in DinD
if ! docker image inspect "$SRC_IMG" >/dev/null 2>&1; then
  echo "[dind-push] Source image not found in DinD: $SRC_IMG"; exit 3
fi

# Login if credentials provided
if [ -n "$REGISTRY_USER" ] && [ -n "$REGISTRY_PASS" ]; then
  REG_HOST=$(echo "$DEST_IMG" | sed -E 's#^([^/]+)/.*#\1#')
  echo "[dind-push] Logging into $REG_HOST as $REGISTRY_USER"
  echo "$REGISTRY_PASS" | docker login "$REG_HOST" --username "$REGISTRY_USER" --password-stdin
else
  echo "[dind-push] No REGISTRY_USER/REGISTRY_PASS provided; attempting anonymous push"
fi

# Tag and push
set -x
docker tag "$SRC_IMG" "$DEST_IMG"
docker push "$DEST_IMG"
set +x

echo "[dind-push] Pushed $DEST_IMG from DinD"
