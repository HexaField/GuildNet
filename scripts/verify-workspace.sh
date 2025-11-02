#!/usr/bin/env bash
set -euo pipefail

# verify-workspace.sh
# Create a workspace via HostApp API, poll until Running and proxyTarget set,
# stream logs until code-server ready line, then probe the proxy root.

ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
HOSTAPP_URL=${HOSTAPP_URL:-https://127.0.0.1:8090}
CLUSTER_ID=${1:-}
WS_NAME=${2:-}
IMAGE=${3:-codercom/code-server:4.9.0}
PASSWORD=${4:-testpass}
TIMEOUT=${TIMEOUT:-300}

if [ -z "$CLUSTER_ID" ] || [ -z "$WS_NAME" ]; then
  echo "Usage: $0 <cluster-id> <workspace-name> [image] [password]"
  exit 2
fi

echolog() { echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] $*"; }

# Create workspace
PAYLOAD=$(jq -nc --arg name "$WS_NAME" --arg image "$IMAGE" --arg passwd "$PASSWORD" '{name:$name, image:$image, env:[{name:"PASSWORD",value:$passwd}], ports:[{containerPort:8080,name:"http"}] }')
echolog "Creating workspace $WS_NAME on cluster $CLUSTER_ID"
HTTP=$(printf '%s' "$PAYLOAD" | curl --http1.1 -k -sS -w "%{http_code}" -o /tmp/ws-create-resp -X POST "$HOSTAPP_URL/api/cluster/$CLUSTER_ID/workspaces" -H "Content-Type: application/json" -d @-)
RESP=$(cat /tmp/ws-create-resp || true)
if [ "$HTTP" != "202" ] && [ "$HTTP" != "200" ]; then
  echolog "Workspace create failed: HTTP=$HTTP resp=$RESP"
  exit 3
fi

echolog "Workspace create accepted: $RESP"

# Poll for workspace status
START=$(date +%s)
while :; do
  if [ $(( $(date +%s) - START )) -gt $TIMEOUT ]; then
    echolog "Timeout waiting for workspace to be Running"
    exit 4
  fi
  sleep 2
  WS_JSON=$(curl --http1.1 -k -sS "$HOSTAPP_URL/api/cluster/$CLUSTER_ID/workspaces/$WS_NAME" || true)
  if [ -z "$WS_JSON" ] || [ "$WS_JSON" = "[]" ]; then
    echolog "Workspace not found yet; retrying..."
    continue
  fi
  PHASE=$(echo "$WS_JSON" | jq -r '.status.phase // empty')
  PROXY=$(echo "$WS_JSON" | jq -r '.status.proxyTarget // empty')
  echolog "Workspace phase=$PHASE proxy=$PROXY"
  if [ "$PHASE" = "Running" ] && [ -n "$PROXY" ]; then
    echolog "Workspace is running and proxyTarget set: $PROXY"
    break
  fi
done

# Stream logs in background for a short period to show startup lines
(echo "--- workspace logs (tail) ---"; curl --http1.1 -k -sS "$HOSTAPP_URL/api/cluster/$CLUSTER_ID/workspaces/$WS_NAME/logs" | jq -r '.[].msg' | sed -n '1,120p') || true

# Try proxied request to root

echolog "Probing proxied code-server root via HostApp proxy"
set +e
# Retry loop to handle transient TLS/streaming errors seen on some systems.
TRY=0
MAX_TRIES=5
SLEEP=1
PCODE=000
while [ $TRY -lt $MAX_TRIES ]; do
  TRY=$((TRY+1))
  echolog "probe attempt $TRY/$MAX_TRIES"
  # Use HTTP/1.1 and a short timeout via --max-time to avoid stuck connections
  curl -k --http1.1 --max-time 15 -sS "$HOSTAPP_URL/api/cluster/$CLUSTER_ID/proxy/server/$WS_NAME/" -o /tmp/proxy-root-resp -w "%{http_code}" > /tmp/proxy-code 2>/tmp/proxy-err || true
  PCODE=$(cat /tmp/proxy-code || true)
  if [ "$PCODE" = "200" ] || [ "$PCODE" = "302" ]; then
    echolog "Proxy successful (HTTP=$PCODE); response saved to /tmp/proxy-root-resp"
    break
  fi
  # Capture curl stderr for debugging
  if grep -qi "SSL_read" /tmp/proxy-err 2>/dev/null; then
    echolog "probe attempt $TRY encountered TLS read error; will retry after backoff"
  else
    echolog "probe attempt $TRY returned HTTP=$PCODE; stderr=$(cat /tmp/proxy-err || true)"
  fi
  if [ $TRY -lt $MAX_TRIES ]; then
    sleep $SLEEP
    SLEEP=$((SLEEP * 2))
    continue
  fi
  echolog "Proxy failed or returned non-OK after $MAX_TRIES attempts: HTTP=$PCODE; see /tmp/proxy-root-resp /tmp/proxy-err"
  exit 5
done
set -e

echolog "verify-workspace completed successfully"
exit 0
