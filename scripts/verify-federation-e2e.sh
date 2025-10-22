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
exit 0
