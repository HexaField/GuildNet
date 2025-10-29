#!/usr/bin/env bash
set -euo pipefail

# attach-local-k0s.sh
# Read the locally emitted kubeconfig and attach the cluster to the Host App via /bootstrap.
# Requires the Host App to be reachable (typically https://127.0.0.1:8090 by default).

GN_KUBECONFIG=${GN_KUBECONFIG:-"$HOME/.guildnet/kubeconfig"}
HOSTAPP_URL=${HOSTAPP_URL:-"https://127.0.0.1:8090"}
API_TOKEN=${API_TOKEN:-""}  # optional bearer token for protected hosts

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
curl -sk -X POST "$HOSTAPP_URL/bootstrap" "${HDRS[@]}" --data "$BODY"
set +x

