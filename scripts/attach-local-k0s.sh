#!/usr/bin/env bash
set -euo pipefail

# attach-local-k0s.sh
# Read the locally emitted kubeconfig and attach the cluster to the Host App via /bootstrap.
# Requires the Host App to be reachable (typically https://127.0.0.1:8090 by default).

GN_KUBECONFIG=${GN_KUBECONFIG:-"$HOME/.guildnet/kubeconfig"}
HOSTAPP_URL=${HOSTAPP_URL:-"https://127.0.0.1:8090"}
API_TOKEN=${API_TOKEN:-""}  # optional bearer token for protected hosts

# Optional: apply default per-cluster settings after attach when SET_DEFAULTS=1.
# Supported env overrides for settings JSON:
#   API_PROXY_URL, API_PROXY_FORCE_HTTP (true/false), IMAGE_PULL_SECRET,
#   USE_PORT_FORWARD (true/false), PREFER_POD_PROXY (true/false)
SET_DEFAULTS=${SET_DEFAULTS:-0}

if [ ! -s "$GN_KUBECONFIG" ]; then
  echo "[attach-local-k0s] kubeconfig not found: $GN_KUBECONFIG" >&2
  exit 2
fi

KUBE_B64=$(awk 'BEGIN {ORS="\\n"} {print}' "$GN_KUBECONFIG" | sed -e 's/\r$//' )
# Post JSON body: { cluster: { kubeconfig: "<raw yaml>" } }
BODY=$(jq -n --arg kc "$KUBE_B64" '{cluster:{kubeconfig:$kc}}')

HDRS=(-H 'Content-Type: application/json')
if [ -n "$API_TOKEN" ]; then
  HDRS+=(-H "Authorization: Bearer $API_TOKEN")
fi

set -x
RESP=$(curl -sk -X POST "$HOSTAPP_URL/bootstrap" "${HDRS[@]}" --data "$BODY")
RC=$?
set +x
if [ $RC -ne 0 ] || [ -z "$RESP" ]; then
  echo "[attach-local-k0s] bootstrap request failed" >&2
  echo "$RESP" >&2 || true
  exit 3
fi

CLUSTER_ID=$(echo "$RESP" | jq -r '.cluster.id // .id // empty' 2>/dev/null || true)
if [ -z "$CLUSTER_ID" ]; then
  echo "[attach-local-k0s] WARNING: could not determine cluster id from response" >&2
fi

if [ "$SET_DEFAULTS" = "1" ] && [ -n "$CLUSTER_ID" ]; then
  echo "[attach-local-k0s] applying default per-cluster settings for $CLUSTER_ID" >&2
  # Build settings JSON from provided envs; only include fields that are non-empty
  TMP=$(mktemp)
  jq -n \
    --arg api_proxy_url "${API_PROXY_URL:-}" \
    --argjson api_proxy_force_http "${API_PROXY_FORCE_HTTP:-false}" \
    --arg image_pull_secret "${IMAGE_PULL_SECRET:-}" \
    --argjson use_port_forward "${USE_PORT_FORWARD:-false}" \
    --argjson prefer_pod_proxy "${PREFER_POD_PROXY:-false}" \
    '{api_proxy_url: ( $api_proxy_url | select(length>0) ),
      api_proxy_force_http: $api_proxy_force_http,
      image_pull_secret: ( $image_pull_secret | select(length>0) ),
      use_port_forward: $use_port_forward,
      prefer_pod_proxy: $prefer_pod_proxy }' \
    > "$TMP"
  # Prune nulls to avoid overwriting with null
  jq 'with_entries(select(.value != null))' "$TMP" > "$TMP.json" && mv "$TMP.json" "$TMP"
  set -x
  curl -sk -X PUT "$HOSTAPP_URL/api/settings/cluster/$CLUSTER_ID" "${HDRS[@]}" --data-binary @"$TMP"
  set +x
  rm -f "$TMP" || true
fi

