#!/usr/bin/env bash
set -euo pipefail
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

ENV_FILE="$ROOT/.env"
STATE_DIR="$HOME/.guildnet/headscale"
CONTAINER=${HEADSCALE_CONTAINER_NAME:-"guildnet-headscale"}
USER_NAME=${HEADSCALE_USER:-"guildnet"}

need() { command -v "$1" >/dev/null 2>&1 || { echo "Missing: $1" >&2; exit 1; }; }
need docker

[ -f "$ENV_FILE" ] || { echo ".env not found at $ENV_FILE" >&2; exit 1; }

# Ensure Headscale is up and we know the server URL
bash "$ROOT/scripts/headscale-run.sh" up >/dev/null
SERVER_URL_FILE="$STATE_DIR/server_url"
if [ ! -f "$SERVER_URL_FILE" ]; then
  echo "Headscale server_url not found at $SERVER_URL_FILE" >&2
  exit 1
fi
SERVER_URL=$(cat "$SERVER_URL_FILE" | tr -d '\n\r')

# Ensure user exists
if ! docker exec -i "$CONTAINER" headscale users list 2>/dev/null | awk '{print $2}' | grep -q "^${USER_NAME}$"; then
  echo "[bootstrap] Creating Headscale user: $USER_NAME"
  docker exec -i "$CONTAINER" headscale users create "$USER_NAME" >/dev/null
fi

# Create a reusable preauth key and extract the actual tskey- value (not the numeric ID)
KEY_LINE=$(docker exec -i "$CONTAINER" headscale preauthkeys create --reusable --ephemeral=false --expiration 168h --user "$USER_NAME" | tail -n1)
# Prefer a column matching tskey-; fallback to first token if not found
KEY=$(echo "$KEY_LINE" | awk '{
  for (i=1;i<=NF;i++) { if ($i ~ /^tskey-/) { print $i; exit } }
  print $1
}')
if [ -z "$KEY" ]; then
  echo "Failed to obtain preauth key from headscale." >&2
  exit 1
fi
mkdir -p "$STATE_DIR"
echo "$KEY" > "$STATE_DIR/preauth-${USER_NAME}.txt"

# Update .env entries
tmp=$(mktemp)
sed -E "s#^(TS_LOGIN_SERVER=).*#\\1$SERVER_URL#" "$ENV_FILE" > "$tmp" && mv "$tmp" "$ENV_FILE"
tmp=$(mktemp)
sed -E "s#^(HEADSCALE_URL=).*#\\1$SERVER_URL#" "$ENV_FILE" > "$tmp" && mv "$tmp" "$ENV_FILE"
if grep -q '^TS_AUTHKEY=' "$ENV_FILE"; then
  tmp=$(mktemp)
  sed -E "s#^(TS_AUTHKEY=).*#\\1$KEY#" "$ENV_FILE" > "$tmp" && mv "$tmp" "$ENV_FILE"
else
  echo "TS_AUTHKEY=$KEY" >> "$ENV_FILE"
fi

echo "[bootstrap] TS_LOGIN_SERVER set to $SERVER_URL"
echo "[bootstrap] TS_AUTHKEY set in .env (user=$USER_NAME)"
echo "[bootstrap] Done. You can now run: make router-up"
