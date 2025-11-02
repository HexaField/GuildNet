#!/usr/bin/env bash
set -euo pipefail

# Verify HostApp headscale + cluster flows via the HostApp HTTP API.
# This does not provision clusters for you; it submits orchestration jobs via
# POST /api/deploy/headscale and POST /api/deploy/clusters and polls job status
# so we exercise the server-side orchestration handlers.

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
if [ "${RUN_INTEGRATION:-}" != "1" ]; then
  echo "This script is opt-in. Set RUN_INTEGRATION=1 to run the test."
  exit 1
fi

need() { command -v "$1" >/dev/null 2>&1 || { echo "Missing: $1" >&2; exit 2; } }
need curl
need jq

HOSTAPP_URL=${HOSTAPP_URL:-https://127.0.0.1:8090}
API_BASE="$HOSTAPP_URL"

echolog() { printf '[%s] %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }

http_post() {
  local url=$1; shift
  curl -k -sS -X POST -H 'Content-Type: application/json' -d "$@" "$url"
}

poll_job() {
  local jobid=$1
  local timeout=${2:-120}
  local start
  start=$(date +%s)
  echolog "Polling job $jobid (timeout=${timeout}s)"
  while :; do
    resp=$(curl -k -sS "$API_BASE/api/jobs/$jobid" || true)
    if [ -z "$resp" ]; then
      echolog "job $jobid: no response yet"
    else
      status=$(printf '%s' "$resp" | jq -r '.status // empty')
      echolog "job $jobid status=$status"
      if [ "$status" = "succeeded" ]; then
        return 0
      fi
      if [ "$status" = "failed" ] || [ "$status" = "canceled" ]; then
        printf '%s\n' "$resp" | jq -C .
        return 2
      fi
    fi
    now=$(date +%s)
    if [ $((now - start)) -gt $timeout ]; then
      echolog "job $jobid timed out after ${timeout}s"
      return 3
    fi
    sleep 2
  done
}

echolog "HostApp API verifier: $API_BASE"

echolog "Step: create Headscale via API"
hs_create_resp=$(curl -k -sS -X POST -H 'Content-Type: application/json' -d '{"name":"verify-hs"}' "$API_BASE/api/deploy/headscale") || true
hs_id=$(printf '%s' "$hs_create_resp" | jq -r '.id // empty')
hs_job=$(printf '%s' "$hs_create_resp" | jq -r '.jobId // empty')
if [ -z "$hs_job" ]; then
  echolog "Failed to create headscale (no jobId). Response: $hs_create_resp"
  exit 4
fi
echolog "Headscale create jobId=$hs_job id=$hs_id"

if ! poll_job "$hs_job" 120; then
  echolog "Headscale create job failed or timed out"
  # print job detail for diagnostics
  curl -k -sS "$API_BASE/api/jobs/$hs_job" | jq -C . || true
  curl -k -sS "$API_BASE/api/jobs-logs/$hs_job" || true
  exit 5
fi

if [ -n "$hs_id" ]; then
  echolog "Fetching headscale record $hs_id"
  curl -k -sS "$API_BASE/api/deploy/headscale/$hs_id" | jq -C . || true
fi

echolog "Step: create a Cluster record via API"
CLUSTER_NAME="verify-cluster-$(head -c4 /dev/urandom | od -An -tx1 | tr -d ' \n')"
cluster_create_resp=$(curl -k -sS -X POST -H 'Content-Type: application/json' -d "{\"name\":\"$CLUSTER_NAME\"}" "$API_BASE/api/deploy/clusters") || true
cluster_id=$(printf '%s' "$cluster_create_resp" | jq -r '.id // empty')
cluster_job=$(printf '%s' "$cluster_create_resp" | jq -r '.jobId // empty')
if [ -z "$cluster_job" ]; then
  echolog "Failed to create cluster (no jobId). Response: $cluster_create_resp"
  exit 6
fi
echolog "Cluster create jobId=$cluster_job id=$cluster_id"

if ! poll_job "$cluster_job" 180; then
  echolog "Cluster create job failed or timed out"
  curl -k -sS "$API_BASE/api/jobs/$cluster_job" | jq -C . || true
  curl -k -sS "$API_BASE/api/jobs-logs/$cluster_job" || true
  exit 7
fi

echolog "Cluster create succeeded (id=$cluster_id)"

# If a kubeconfig is provided via GN_KUBECONFIG or KUBECONFIG, attach it via API to fully exercise attach endpoint
KC_PATH=${GN_KUBECONFIG:-${KUBECONFIG:-}}
if [ -n "$KC_PATH" ] && [ -f "$KC_PATH" ]; then
  echolog "Attaching kubeconfig from $KC_PATH to cluster id=${cluster_id:-$CLUSTER_NAME}"
  kc=$(cat "$KC_PATH" | python3 -c 'import sys,json; print(json.dumps(sys.stdin.read()))')
  if [ -n "$cluster_id" ]; then
    curl -k -sS -X POST "$API_BASE/api/deploy/clusters/$cluster_id?action=attach-kubeconfig" -H 'Content-Type: application/json' -d "{\"kubeconfig\":$kc}" -o /dev/null -w "%{http_code}\n"
  else
    # if cluster_id empty, try attach to name alias 'default'
    curl -k -sS -X POST "$API_BASE/api/deploy/clusters/default?action=attach-kubeconfig" -H 'Content-Type: application/json' -d "{\"kubeconfig\":$kc}" -o /dev/null -w "%{http_code}\n"
  fi
else
  echolog "No kubeconfig available to attach; skipping attach-kubeconfig step"
fi

echolog "Verify HostApp API run completed successfully"
exit 0
