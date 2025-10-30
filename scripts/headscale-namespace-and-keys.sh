#!/usr/bin/env bash
set -euo pipefail
# Create (or ensure) a Headscale namespace for a cluster and emit router/client preauth keys.
# Outputs: tmp/cluster-<id>-headscale.json with fields: namespace, loginServer, routerAuthKey, clientAuthKey

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
# Preserve explicitly provided environment values across .env sourcing
_USER_CLUSTER=${CLUSTER:-}
_USER_HEADSCALE_URL=${HEADSCALE_URL:-}
[ -f "$ROOT/.env" ] && . "$ROOT/.env"
# Restore user-provided values if set
[ -n "$_USER_CLUSTER" ] && CLUSTER="$_USER_CLUSTER"
[ -n "$_USER_HEADSCALE_URL" ] && HEADSCALE_URL="$_USER_HEADSCALE_URL"

CLUSTER=${CLUSTER:-${1:-default}}
HEADSCALE_URL=${HEADSCALE_URL:-}

mkdir -p "$ROOT/tmp"

need() { command -v "$1" >/dev/null 2>&1 || { echo "Missing: $1" >&2; exit 1; }; }
need docker

# Determine headscale URL
if [ -z "$HEADSCALE_URL" ]; then
  if docker ps --format '{{.Names}}' | grep -q '^guildnet-headscale$'; then
    # Query mapped host:port
    HOST=$(docker inspect -f '{{ (index (index .NetworkSettings.Ports "8080/tcp") 0).HostIp }}' guildnet-headscale 2>/dev/null || echo 127.0.0.1)
    PORT=$(docker inspect -f '{{ (index (index .NetworkSettings.Ports "8080/tcp") 0).HostPort }}' guildnet-headscale 2>/dev/null || echo 8081)
    [ "$HOST" = "0.0.0.0" ] && HOST=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for (i=1;i<=NF;i++) if ($i=="src") {print $(i+1); exit}}' | head -n1)
    HEADSCALE_URL="http://${HOST}:${PORT}"
  else
    echo "[headscale] Not running; starting via make headscale-up" >&2
    make headscale-up
    # Recurse to pick up container URL
    exec "$0"
  fi
fi

ns="cluster-${CLUSTER}"

echo "[headscale] Using server: ${HEADSCALE_URL} namespace: ${ns}"

# Ensure user/namespace exists; Headscale uses users as namespaces
docker exec -i guildnet-headscale headscale users create "$ns" >/dev/null 2>&1 || true

# Resolve Headscale user ID for namespace (some headscale builds require numeric --user id)
resolve_user_id() {
  local user_name="$1"
  docker exec -i guildnet-headscale headscale users list \
    | awk -F'\|' -v target="$user_name" 'NR>1{uid=$1; uname=$3; gsub(/^[ \t]+|[ \t]+$/,"",uid); gsub(/^[ \t]+|[ \t]+$/,"",uname); if (uname==target) {print uid; exit}}'
}

USER_ID=$(resolve_user_id "$ns")
if [ -z "${USER_ID:-}" ]; then
  echo "[headscale] ERROR: could not resolve user id for namespace $ns" >&2
  exit 1
fi

# Generate keys
gen_key() {
  local tag="$1"
  docker exec -i guildnet-headscale headscale preauthkeys create --reusable --ephemeral=false --expiration 24h --user "$USER_ID" | tr -d '\r' | tr -d '\n'
}

routerKey=$(gen_key router)
clientKey=$(gen_key client)

out="$ROOT/tmp/cluster-${CLUSTER}-headscale.json"
cat >"$out" <<JSON
{
  "namespace": "${ns}",
  "loginServer": "${HEADSCALE_URL}",
  "routerAuthKey": "${routerKey}",
  "clientAuthKey": "${clientKey}"
}
JSON

chmod 600 "$out" || true
echo "[headscale] Wrote $out"
