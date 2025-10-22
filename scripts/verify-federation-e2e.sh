#!/usr/bin/env bash
set -euo pipefail

# verify-federation-e2e.sh
# Purpose: Assert two devices (local + remote over SSH) point to the SAME deterministic cluster
# by comparing cluster IDs exposed by each hostapp. If remote is missing the local cluster,
# the script will attach the local kubeconfig to the remote via the supported API.

# Requirements:
# - curl, jq available locally and on the remote
# - SSH access to the remote user@host (REMOTE_SSH), no default
# - Local hostapp listening on https://127.0.0.1:8090 (self-signed OK)
#
# Env:
#   REMOTE_SSH="user@192.168.0.1"
#   REMOTE_HOSTAPP_URL="https://127.0.0.1:8090"   # resolved from remote side
#   LOCAL_KUBECONFIG="$HOME/.guildnet/kubeconfig" # falls back to ~/.kube/config
#   VERBOSE=1

REMOTE_SSH="${REMOTE_SSH}"
REMOTE_HOSTAPP_URL="${REMOTE_HOSTAPP_URL:-https://127.0.0.1:8090}"
LOCAL_HOSTAPP_URL="${LOCAL_HOSTAPP_URL:-https://127.0.0.1:8090}"

log(){ echo "[E2E] $*"; }
vv(){ [ "${VERBOSE:-0}" != "0" ] && echo "[E2E:DBG] $*" || true; }

# Helpers
require(){ command -v "$1" >/dev/null 2>&1 || { echo "ERROR: required tool '$1' not found" >&2; exit 127; }; }
require curl
require jq

# Determine local kubeconfig path
LOCAL_KUBECONFIG="${LOCAL_KUBECONFIG:-}"
if [ -z "$LOCAL_KUBECONFIG" ]; then
  if [ -s "$HOME/.guildnet/kubeconfig" ]; then LOCAL_KUBECONFIG="$HOME/.guildnet/kubeconfig";
  elif [ -s "$HOME/.kube/config" ]; then LOCAL_KUBECONFIG="$HOME/.kube/config";
  else echo "ERROR: no kubeconfig found locally" >&2; exit 2; fi
fi

log "Local kubeconfig: $LOCAL_KUBECONFIG"

# Fetch local cluster IDs
LOCAL_IDS=$(curl -k -s "$LOCAL_HOSTAPP_URL/api/deploy/clusters" | jq -r '.[].id')
if [ -z "$LOCAL_IDS" ]; then echo "ERROR: no clusters on local hostapp" >&2; exit 3; fi
vv "LOCAL_IDS: $LOCAL_IDS"

# Pick canonical local cluster id: prefer 32 hex chars (deterministic id)
LOCAL_DET="$(echo "$LOCAL_IDS" | awk '/^[0-9a-f]{32}$/ {print; exit}')"
if [ -z "$LOCAL_DET" ]; then LOCAL_DET="$(echo "$LOCAL_IDS" | head -n1)"; fi
log "Local deterministic cluster id: $LOCAL_DET"

# Fetch remote cluster IDs
REMOTE_IDS=$(ssh -o BatchMode=yes -o ConnectTimeout=5 "$REMOTE_SSH" -t "curl -k -s '$REMOTE_HOSTAPP_URL/api/deploy/clusters' | jq -r '.[].id'" 2>/dev/null | tr -d '\r') || true
vv "REMOTE_IDS: $REMOTE_IDS"

HAS_REMOTE_DET=$(echo "$REMOTE_IDS" | grep -c "^$LOCAL_DET$") || true
if [ "$HAS_REMOTE_DET" -eq 0 ]; then
  log "Remote missing cluster $LOCAL_DET — attaching local kubeconfig to remote..."
  # Build JSON locally to avoid remote quoting pitfalls
  TMPJSON="/tmp/kc.$$.json"
  jq -n --rawfile kc "$LOCAL_KUBECONFIG" '{"kubeconfig":$kc}' > "$TMPJSON"
  scp -q "$TMPJSON" "$REMOTE_SSH:/tmp/verify-kc.json"
  rm -f "$TMPJSON"
  # Attach on remote using action=attach-kubeconfig (supports deterministic id)
  ssh -o BatchMode=yes "$REMOTE_SSH" -t "curl -k -s -X POST '$REMOTE_HOSTAPP_URL/api/deploy/clusters/placeholder?action=attach-kubeconfig' -H 'Content-Type: application/json' --data-binary @/tmp/verify-kc.json" >/tmp/e2e-attach.out 2>/dev/null || true
  vv "remote attach response: $(cat /tmp/e2e-attach.out 2>/dev/null)"
  # Re-fetch remote IDs
  REMOTE_IDS=$(ssh -o BatchMode=yes "$REMOTE_SSH" -t "curl -k -s '$REMOTE_HOSTAPP_URL/api/deploy/clusters' | jq -r '.[].id'" 2>/dev/null | tr -d '\r') || true
fi

log "Remote cluster ids: $(echo "$REMOTE_IDS" | paste -sd ',' -)"

if echo "$REMOTE_IDS" | grep -q "^$LOCAL_DET$"; then
  log "PASS: Remote now references same cluster id $LOCAL_DET"
else
  echo "FAIL: Remote does not reference local cluster id $LOCAL_DET" >&2
  exit 10
fi

# Optional: verify /v1/sites returns records with clusterId == LOCAL_DET (best-effort)
LOCAL_SITES=$(curl -k -s "$LOCAL_HOSTAPP_URL/api/v1/sites" | jq -r '.[] | .clusterId' 2>/dev/null | sort -u || true)
vv "LOCAL_SITES clusterIds: $LOCAL_SITES"
if echo "$LOCAL_SITES" | grep -q "^$LOCAL_DET$"; then
  log "Local sites reflect cluster $LOCAL_DET"
else
  log "WARN: Local sites did not include clusterId $LOCAL_DET (may be transient)"
fi

ssh -o BatchMode=yes "$REMOTE_SSH" -t "curl -k -s '$REMOTE_HOSTAPP_URL/api/v1/sites' | jq -r '.[] | .clusterId' 2>/dev/null | sort -u" >/tmp/e2e-remote-sites.txt 2>/dev/null || true
REMOTE_SITES=$(tr -d '\r' </tmp/e2e-remote-sites.txt || true)
vv "REMOTE_SITES clusterIds: $REMOTE_SITES"
if echo "$REMOTE_SITES" | grep -q "^$LOCAL_DET$"; then
  log "Remote sites reflect cluster $LOCAL_DET"
else
  log "WARN: Remote sites did not include clusterId $LOCAL_DET (may be transient)"
fi

log "E2E federation verification completed"

# --- Distributed workspace verification: both devices spawn and see code-server ---

WS_IMG="${WS_IMG:-codercom/code-server:4.90.3}"
TSUFFIX="$(date +%s)"
WS_LOCAL_NAME="e2e-codeserver-local-${TSUFFIX}"
WS_REMOTE_NAME="e2e-codeserver-remote-${TSUFFIX}"
TIMEOUT_SEC="${TIMEOUT_SEC:-300}"
SLEEP_SEC="${SLEEP_SEC:-5}"

post_local_workspace(){
  local name="$1"
  vv "Creating local workspace: ${name}"
  local payload
  payload=$(jq -nc --arg img "$WS_IMG" --arg name "$name" '{image:$img,name:$name}')
  curl -k -s -X POST "$LOCAL_HOSTAPP_URL/api/cluster/$LOCAL_DET/workspaces" \
    -H 'Content-Type: application/json' \
    -d "$payload" | jq -r '.' || true
}

post_remote_workspace(){
  local name="$1"
  vv "Creating remote workspace: ${name}"
  local payload
  payload=$(jq -nc --arg img "$WS_IMG" --arg name "$name" '{image:$img,name:$name}')
  ssh -o BatchMode=yes "$REMOTE_SSH" -t \
    "curl -k -s -X POST '$REMOTE_HOSTAPP_URL/api/cluster/$LOCAL_DET/workspaces' -H 'Content-Type: application/json' -d '$payload' | jq -r '.'" \
    >/tmp/e2e-remote-create-${name}.json 2>/dev/null || true
}

workspace_exists_local(){
  local name="$1"
  curl -k -s "$LOCAL_HOSTAPP_URL/api/cluster/$LOCAL_DET/workspaces/$name" | jq -e '.metadata.name=="'$name'"' >/dev/null 2>&1
}

workspace_exists_remote(){
  local name="$1"
  ssh -o BatchMode=yes "$REMOTE_SSH" -t \
    "curl -k -s '$REMOTE_HOSTAPP_URL/api/cluster/$LOCAL_DET/workspaces/$name' | jq -e '.metadata.name==\"$name\"' >/dev/null 2>&1; echo \$?" \
    2>/dev/null | tr -d '\r' | grep -q '^0$'
}

wait_workspace_local(){
  local name="$1"; local waited=0
  while [ $waited -lt $TIMEOUT_SEC ]; do
    if workspace_exists_local "$name"; then return 0; fi
    sleep "$SLEEP_SEC"; waited=$((waited+SLEEP_SEC))
  done
  return 1
}

wait_workspace_remote(){
  local name="$1"; local waited=0
  while [ $waited -lt $TIMEOUT_SEC ]; do
    if workspace_exists_remote "$name"; then return 0; fi
    sleep "$SLEEP_SEC"; waited=$((waited+SLEEP_SEC))
  done
  return 1
}

list_servers_local(){
  curl -k -s "$LOCAL_HOSTAPP_URL/api/cluster/$LOCAL_DET/servers" | jq -r '.[].name' 2>/dev/null || true
}

list_servers_remote(){
  ssh -o BatchMode=yes "$REMOTE_SSH" -t \
    "curl -k -s '$REMOTE_HOSTAPP_URL/api/cluster/$LOCAL_DET/servers' | jq -r '.[].name'" \
    2>/dev/null | tr -d '\r' || true
}

# Helpers to retrieve placement fields from servers list
get_server_field_local(){
  local name="$1"; local field="$2"
  curl -k -s "$LOCAL_HOSTAPP_URL/api/cluster/$LOCAL_DET/servers" | jq -r ".[] | select(.name==\"$name\") | .${field} // \"\"" 2>/dev/null || true
}
get_server_field_remote(){
  local name="$1"; local field="$2"
  ssh -o BatchMode=yes "$REMOTE_SSH" -t \
    "curl -k -s '$REMOTE_HOSTAPP_URL/api/cluster/$LOCAL_DET/servers' | jq -r '.[] | select(.name==\"$name\") | .${field} // \"\"'" \
    2>/dev/null | tr -d '\r' || true
}

fetch_logs_local(){
  local name="$1"
  curl -k -s "$LOCAL_HOSTAPP_URL/api/cluster/$LOCAL_DET/workspaces/$name/logs" | jq -r '.[]?' 2>/dev/null || true
}

fetch_logs_remote(){
  local name="$1"
  ssh -o BatchMode=yes "$REMOTE_SSH" -t \
    "curl -k -s '$REMOTE_HOSTAPP_URL/api/cluster/$LOCAL_DET/workspaces/$name/logs' | jq -r '.[]?'" \
    2>/dev/null | tr -d '\r' || true
}

# Wait until a server reaches desired status using the /servers view
wait_server_status_local(){
  local name="$1"; local want="${2:-running}"; local waited=0
  while [ $waited -lt $TIMEOUT_SEC ]; do
    st=$(curl -k -s "$LOCAL_HOSTAPP_URL/api/cluster/$LOCAL_DET/servers" | jq -r ".[] | select(.name==\"$name\") | .status // \"\"")
    if [ "$st" = "$want" ]; then return 0; fi
    sleep "$SLEEP_SEC"; waited=$((waited+SLEEP_SEC))
  done
  return 1
}

wait_server_status_remote(){
  local name="$1"; local want="${2:-running}"; local waited=0
  while [ $waited -lt $TIMEOUT_SEC ]; do
    st=$(ssh -o BatchMode=yes "$REMOTE_SSH" -t \
      "curl -k -s '$REMOTE_HOSTAPP_URL/api/cluster/$LOCAL_DET/servers' | jq -r '.[] | select(.name==\"$name\") | .status // \"\"'" \
      2>/dev/null | tr -d '\r')
    if [ "$st" = "$want" ]; then return 0; fi
    sleep "$SLEEP_SEC"; waited=$((waited+SLEEP_SEC))
  done
  return 1
}

log "Spawning code-server from local: $WS_LOCAL_NAME"
post_local_workspace "$WS_LOCAL_NAME"
if ! wait_workspace_local "$WS_LOCAL_NAME"; then
  echo "FAIL: Local workspace $WS_LOCAL_NAME not observed within $TIMEOUT_SEC s" >&2; exit 21
fi

log "Spawning code-server from remote: $WS_REMOTE_NAME"
post_remote_workspace "$WS_REMOTE_NAME"
if ! wait_workspace_remote "$WS_REMOTE_NAME"; then
  echo "FAIL: Remote workspace $WS_REMOTE_NAME not observed within $TIMEOUT_SEC s" >&2; exit 22
fi

# Wait for pods to come up to avoid log flakiness
log "Waiting for workspaces to reach running state before fetching logs"
if ! wait_server_status_local "$WS_LOCAL_NAME" running; then
  echo "FAIL: Local workspace $WS_LOCAL_NAME did not reach running state within $TIMEOUT_SEC s" >&2; exit 33
fi
if ! wait_server_status_remote "$WS_REMOTE_NAME" running; then
  echo "FAIL: Remote workspace $WS_REMOTE_NAME did not reach running state within $TIMEOUT_SEC s" >&2; exit 34
fi

# Visibility checks from local perspective
LOCAL_SERVERS=$(list_servers_local)
vv "Local sees servers: $LOCAL_SERVERS"
echo "$LOCAL_SERVERS" | grep -q "^$WS_LOCAL_NAME$" || { echo "FAIL: Local does not list its own workspace $WS_LOCAL_NAME" >&2; exit 23; }
echo "$LOCAL_SERVERS" | grep -q "^$WS_REMOTE_NAME$" || { echo "FAIL: Local does not list remote workspace $WS_REMOTE_NAME" >&2; exit 24; }

# Visibility checks from remote perspective
REMOTE_SERVERS=$(list_servers_remote)
vv "Remote sees servers: $REMOTE_SERVERS"
echo "$REMOTE_SERVERS" | grep -q "^$WS_LOCAL_NAME$" || { echo "FAIL: Remote does not list local workspace $WS_LOCAL_NAME" >&2; exit 25; }
echo "$REMOTE_SERVERS" | grep -q "^$WS_REMOTE_NAME$" || { echo "FAIL: Remote does not list its own workspace $WS_REMOTE_NAME" >&2; exit 26; }

# Logs checks – app should be running now; fetch logs
LOCAL_LOGS_SELF=$(fetch_logs_local "$WS_LOCAL_NAME")
LOCAL_LOGS_PEER=$(fetch_logs_local "$WS_REMOTE_NAME")
REMOTE_LOGS_SELF=$(fetch_logs_remote "$WS_REMOTE_NAME")
REMOTE_LOGS_PEER=$(fetch_logs_remote "$WS_LOCAL_NAME")

if [ -z "$LOCAL_LOGS_SELF" ]; then echo "FAIL: Local could not read logs for its own workspace $WS_LOCAL_NAME" >&2; exit 27; fi
if [ -z "$LOCAL_LOGS_PEER" ]; then echo "FAIL: Local could not read logs for remote workspace $WS_REMOTE_NAME" >&2; exit 28; fi
if [ -z "$REMOTE_LOGS_SELF" ]; then echo "FAIL: Remote could not read logs for its own workspace $WS_REMOTE_NAME" >&2; exit 29; fi
if [ -z "$REMOTE_LOGS_PEER" ]; then echo "FAIL: Remote could not read logs for local workspace $WS_LOCAL_NAME" >&2; exit 30; fi

log "PASS: Distributed cluster verified — both devices spawned code-server, see each other, and can read logs for both."

# Placement checks — verify each workspace is scheduled on the device that launched it
LOCAL_HOSTNAME=$(hostname -s 2>/dev/null || hostname 2>/dev/null || echo "")
REMOTE_HOSTNAME=$(ssh -o BatchMode=yes "$REMOTE_SSH" -t 'hostname -s 2>/dev/null || hostname 2>/dev/null || echo ""' 2>/dev/null | tr -d '\r')
vv "Local hostname: $LOCAL_HOSTNAME, Remote hostname: $REMOTE_HOSTNAME"

# Fetch placement info
LOC_NODE=$(get_server_field_local "$WS_LOCAL_NAME" node)
LOC_MACHINE=$(get_server_field_local "$WS_LOCAL_NAME" machineName)
REM_NODE=$(get_server_field_local "$WS_REMOTE_NAME" node)
REM_MACHINE=$(get_server_field_local "$WS_REMOTE_NAME" machineName)
vv "Local workspace placement: node=$LOC_NODE machine=$LOC_MACHINE"
vv "Remote workspace placement: node=$REM_NODE machine=$REM_MACHINE"

lc(){ echo "$1" | tr '[:upper:]' '[:lower:]'; }

EXPECT_LOCAL=$(lc "$LOCAL_HOSTNAME")
EXPECT_REMOTE=$(lc "$REMOTE_HOSTNAME")

PLACED_LOCAL=$(lc "$LOC_MACHINE")
if [ -z "$PLACED_LOCAL" ]; then PLACED_LOCAL=$(lc "$LOC_NODE"); fi
PLACED_REMOTE=$(lc "$REM_MACHINE")
if [ -z "$PLACED_REMOTE" ]; then PLACED_REMOTE=$(lc "$REM_NODE"); fi

if [ -z "$PLACED_LOCAL" ] || [ "$PLACED_LOCAL" != "$EXPECT_LOCAL" ]; then
  echo "FAIL: Local-launched workspace ($WS_LOCAL_NAME) not placed on local device (expected $EXPECT_LOCAL, got ${PLACED_LOCAL:-<empty>}). Ensure the remote device is a node in the same cluster and node names match hostnames." >&2
  exit 31
fi
if [ -z "$PLACED_REMOTE" ] || [ "$PLACED_REMOTE" != "$EXPECT_REMOTE" ]; then
  echo "FAIL: Remote-launched workspace ($WS_REMOTE_NAME) not placed on remote device (expected $EXPECT_REMOTE, got ${PLACED_REMOTE:-<empty>}). Ensure the remote device is a node in the same cluster and node names match hostnames." >&2
  exit 32
fi

log "PASS: Placement verified — each workspace is running on the device that launched it."
exit 0
