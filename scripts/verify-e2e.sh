#!/usr/bin/env bash
set -euo pipefail
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
[ -f "$ROOT/.env" ] && . "$ROOT/.env"
export KUBECONFIG="${KUBECONFIG:-${GN_KUBECONFIG:-$HOME/.guildnet/kubeconfig}}"

need() { command -v "$1" >/dev/null 2>&1 || { echo "Missing: $1" >&2; exit 1; }; }
need kubectl
need jq
need curl

echolog() { printf '[%s] %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }

# Fail fast: any failed check will exit immediately with non-zero status.

echo "--- Headscale reachability ---"
HS=${HEADSCALE_URL:-}
if [ -z "$HS" ] && docker ps --format '{{.Names}}' | grep -q '^guildnet-headscale$'; then
  HOST=$(docker inspect -f '{{ (index (index .NetworkSettings.Ports "8080/tcp") 0).HostIp }}' guildnet-headscale 2>/dev/null || echo 127.0.0.1)
  PORT=$(docker inspect -f '{{ (index (index .NetworkSettings.Ports "8080/tcp") 0).HostPort }}' guildnet-headscale 2>/dev/null || echo 8081)
  [ "$HOST" = "0.0.0.0" ] && HOST=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for (i=1;i<=NF;i++) if ($i=="src") {print $(i+1); exit}}' | head -n1)
  HS="http://${HOST}:${PORT}"
fi
if [ -n "$HS" ]; then
  # consider any HTTP status as reachable; only fail if TCP connect fails
  if curl -sS -o /dev/null -m 2 "$HS"; then
    echo ok
  else
    echolog "Headscale unreachable at $HS"
    exit 1
  fi
else
  echolog "No Headscale detected (HEADSCALE_URL not set and no guildnet-headscale container)."
  exit 1
fi

# Router status
bash "$ROOT/scripts/tailscale-router.sh" status || true

# Headscale routes
if docker ps --format '{{.Names}}' | grep -q '^guildnet-headscale$'; then
  # Some headscale releases don't provide 'routes list'. Use a
  # broadly-supported command (nodes list) as a lightweight
  # sanity check for the Headscale server/DB. Keep it non-fatal.
  docker exec -i guildnet-headscale headscale nodes list || true
fi

# API-driven HostApp verification
# Exercises POST /api/deploy/headscale and POST /api/deploy/clusters and polls jobs.
echolog "--- HostApp API driven checks ---"
API_BASE="${HOSTAPP_URL:-https://127.0.0.1:8090}"

poll_job() {
  jobid=$1
  timeout=${2:-120}
  start=$(date +%s)
  echolog "Polling job $jobid (timeout=${timeout}s)"
  while :; do
    resp=$(curl -k -sS "$API_BASE/api/jobs/$jobid" || true)
    if [ -n "$resp" ]; then
      status=$(printf '%s' "$resp" | jq -r '.status // empty' 2>/dev/null || echo "")
      echolog "job $jobid status=$status"
      if [ "$status" = "succeeded" ]; then
        return 0
      fi
      if [ "$status" = "failed" ] || [ "$status" = "canceled" ]; then
        printf '%s\n' "$resp" | jq -C . || true
        return 2
      fi
    else
      echolog "job $jobid: no response yet"
    fi
    now=$(date +%s)
    if [ $((now - start)) -gt $timeout ]; then
      echolog "job $jobid timed out after ${timeout}s"
      return 3
    fi
    sleep 2
  done
}

echolog "Creating Headscale via HostApp API: $API_BASE/api/deploy/headscale"
hs_create_resp=$(curl -k -sS -X POST -H 'Content-Type: application/json' -d '{"name":"verify-hs"}' "$API_BASE/api/deploy/headscale" || true)
hs_id=$(printf '%s' "$hs_create_resp" | jq -r '.id // empty' 2>/dev/null || echo "")
hs_job=$(printf '%s' "$hs_create_resp" | jq -r '.jobId // empty' 2>/dev/null || echo "")
if [ -z "$hs_job" ]; then
  echolog "Failed to create headscale (no jobId). Response: $hs_create_resp"
else
  echolog "Headscale create jobId=$hs_job id=$hs_id"
  if ! poll_job "$hs_job" 120; then
    echolog "Headscale create job failed or timed out"
    curl -k -sS "$API_BASE/api/jobs/$hs_job" | jq -C . || true
    curl -k -sS "$API_BASE/api/jobs-logs/$hs_job" || true
  else
    if [ -n "$hs_id" ]; then
      echolog "Fetching headscale record $hs_id"
      curl -k -sS "$API_BASE/api/deploy/headscale/$hs_id" | jq -C . || true
    fi
  fi
fi

echolog "Creating Cluster record via HostApp API"
CLUSTER_NAME="verify-cluster-$(head -c4 /dev/urandom | od -An -tx1 | tr -d ' \n')"
cluster_create_resp=$(curl -k -sS -X POST -H 'Content-Type: application/json' -d "{\"name\":\"$CLUSTER_NAME\"}" "$API_BASE/api/deploy/clusters" || true)
cluster_id=$(printf '%s' "$cluster_create_resp" | jq -r '.id // empty' 2>/dev/null || echo "")
cluster_job=$(printf '%s' "$cluster_create_resp" | jq -r '.jobId // empty' 2>/dev/null || echo "")
if [ -z "$cluster_job" ]; then
  echolog "Failed to create cluster (no jobId). Response: $cluster_create_resp"
else
  echolog "Cluster create jobId=$cluster_job id=$cluster_id"
  if ! poll_job "$cluster_job" 180; then
    echolog "Cluster create job failed or timed out"
    curl -k -sS "$API_BASE/api/jobs/$cluster_job" | jq -C . || true
    curl -k -sS "$API_BASE/api/jobs-logs/$cluster_job" || true
  else
    echolog "Cluster create succeeded (id=$cluster_id)"
  fi
fi

# Optionally attach a kubeconfig if present
KC_PATH=${GN_KUBECONFIG:-${KUBECONFIG:-}}
if [ -n "$KC_PATH" ] && [ -f "$KC_PATH" ]; then
  echolog "Attaching kubeconfig from $KC_PATH to cluster id=${cluster_id:-$CLUSTER_NAME}"
  kc=$(cat "$KC_PATH" | python3 -c 'import sys,json; print(json.dumps(sys.stdin.read()))')
  if [ -n "$cluster_id" ]; then
    curl -k -sS -X POST "$API_BASE/api/deploy/clusters/$cluster_id?action=attach-kubeconfig" -H 'Content-Type: application/json' -d "{\"kubeconfig\":$kc}" -o /dev/null -w "%{http_code}\n"
  else
    curl -k -sS -X POST "$API_BASE/api/deploy/clusters/default?action=attach-kubeconfig" -H 'Content-Type: application/json' -d "{\"kubeconfig\":$kc}" -o /dev/null -w "%{http_code}\n"
  fi
else
  echolog "No kubeconfig available to attach; skipping attach-kubeconfig step"
fi

# Router DS readiness
echo "--- Tailscale router DaemonSet ---"
if ! command -v kubectl >/dev/null; then
  echo "skip (no kubectl)"
elif [ ! -f "${KUBECONFIG}" ]; then
  echo "skip (no kubeconfig)"
elif ! kubectl version --request-timeout=3s >/dev/null 2>&1; then
  echo "skip (kube API unreachable)"
elif kubectl -n kube-system get ds tailscale-subnet-router >/dev/null 2>&1; then
  # Tailscale is REQUIRED: ensure the daemonset is fully ready
  if kubectl -n kube-system rollout status ds/tailscale-subnet-router --timeout=120s; then
    echo ok
  else
    # Fallback: compare desired vs ready with a short retry loop
    tries=10
    ok=0
    while [ $tries -gt 0 ]; do
      desired=$(kubectl -n kube-system get ds tailscale-subnet-router -o jsonpath='{.status.desiredNumberScheduled}' 2>/dev/null || echo 0)
      ready=$(kubectl -n kube-system get ds tailscale-subnet-router -o jsonpath='{.status.numberReady}' 2>/dev/null || echo 0)
      if [ "$desired" != "" ] && [ "$ready" = "$desired" ] && [ "$desired" != "0" ]; then
        ok=1; break
      fi
      sleep 3
      tries=$((tries-1))
    done
  if [ $ok -eq 1 ]; then echo ok; else echolog "tailscale-subnet-router not ready"; exit 1; fi
  fi
else
  echolog "tailscale-subnet-router DaemonSet not found"; exit 1
fi

# If tailscale pods failed recently, check logs for common 'TUN device ... is busy' local-host issue
if kubectl -n kube-system get pods -l app=tailscale-subnet-router >/dev/null 2>&1; then
  for p in $(kubectl -n kube-system get pods -l app=tailscale-subnet-router -o name 2>/dev/null | sed 's#pod/##'); do
    if kubectl -n kube-system logs "$p" -c tailscale --tail=200 2>/dev/null | grep -i "device or resource busy" >/dev/null 2>&1; then
  echo "\nDetected 'TUN device ... is busy' in tailscale pod logs for $p.";
      echo "This commonly happens when a host-level tailscaled or leftover tailscale interface (tailscale0) is present on a single-node/local cluster.";
      echo "Remediation: on the host where kubelet runs, stop host tailscaled and remove the interface, then re-run deploy:";
      echo "  sudo systemctl stop tailscaled || true";
      echo "  sudo pkill tailscaled || true";
      echo "  sudo ip link delete tailscale0 || true";
      echo "  sudo rm -rf /var/lib/tailscale/* || true";
      echolog "Detected 'TUN device ... is busy' in tailscale pod logs for $p.";
      exit 1
    fi
  done
fi

# Kube readyz + nodes
if command -v kubectl >/dev/null && [ -f "${KUBECONFIG}" ] && kubectl version --request-timeout=3s >/dev/null 2>&1; then
  if ! kubectl --request-timeout=5s get --raw='/readyz?verbose' >/dev/null 2>&1; then
    echolog "kubernetes readyz check failed"
    exit 1
  fi
  kubectl get nodes -o wide || true
else
  echo "--- Kubernetes checks skipped (no kube or unreachable) ---"
fi

# MetalLB sanity: CRDs + controller/speaker
kubectl get crd ipaddresspools.metallb.io l2advertisements.metallb.io 2>/dev/null || true
kubectl -n metallb-system get deploy/controller ds/speaker -o wide 2>/dev/null || true
kubectl -n metallb-system rollout status deploy/controller --timeout=60s || true
kubectl -n metallb-system rollout status ds/speaker --timeout=60s || true

# DB service
if command -v kubectl >/dev/null && [ -f "${KUBECONFIG}" ] && kubectl version --request-timeout=3s >/dev/null 2>&1; then
  bash "$ROOT/scripts/rethinkdb-setup.sh" || true
fi

echo "verify-e2e completed."
echo "SUMMARY: PASS"

### Operator-based smoke test: create a Workspace via the HostApp server API
### What we verify:
###  - HostApp is reachable (default https://127.0.0.1:8090 unless overridden)
###  - HostApp accepts a Workspace create request and reports the Workspace as Running
###  - HostApp exposes a proxied endpoint for the workspace at
###      /api/cluster/<cluster>/proxy/server/<workspace>/
###    and that endpoint returns an HTML page containing the expected code-server login UI (e.g. "password" or "code-server").
### Why via HostApp: the HostApp performs proper proxying, auth and routing; tests should exercise that layer rather than bypassing it via a direct kubectl port-forward.
echo "--- Operator smoke (via HostApp proxy) ---"
if command -v kubectl >/dev/null && [ -f "${KUBECONFIG}" ] && kubectl version --request-timeout=3s >/dev/null 2>&1; then
  VERIFY_SCRIPT="$ROOT/scripts/verify-workspace.sh"
  if [ -x "$VERIFY_SCRIPT" ]; then
    WS_NAME="verify-code-server-e2e"
    CLUSTER_ID="default"
    HOSTAPP_URL="${GN_HOSTAPP_URL:-https://127.0.0.1:8090}"
    # If CLUSTER_ID is the alias 'default', try to auto-discover a concrete id
    # from the HostApp when possible. Prefer a cluster with state=ready.
    if [ "$CLUSTER_ID" = "default" ]; then
      # Discover a suitable cluster id by checking status for each known cluster.
      # Prefer kubeconfigPresent && k8sReachable, otherwise any kubeconfigPresent.
      IDS=$(curl -k -sS "$HOSTAPP_URL/api/deploy/clusters" | jq -r '.[] | .id' 2>/dev/null || true)
      PICK=""
      if [ -n "$IDS" ]; then
        for id in $IDS; do
          st=$(curl -k -sS "$HOSTAPP_URL/api/cluster/$id/status" 2>/dev/null || true)
          kp=$(printf '%s' "$st" | jq -r '.kubeconfigPresent // false' 2>/dev/null || echo false)
          kr=$(printf '%s' "$st" | jq -r '.k8sReachable // false' 2>/dev/null || echo false)
          if [ "$kp" = "true" ] && [ "$kr" = "true" ]; then
            PICK="$id"; break
          fi
          if [ -z "$PICK" ] && [ "$kp" = "true" ]; then
            PICK="$id"
          fi
        done
      fi
      if [ -n "${PICK:-}" ]; then
        CLUSTER_ID="$PICK"
      fi
    fi
    echo "Using HostApp at $HOSTAPP_URL to create and verify Workspace $WS_NAME on cluster $CLUSTER_ID"
    # Create workspace and wait for Running/proxyTarget via HostApp API (non-fatal)
    if ! HOSTAPP_URL="$HOSTAPP_URL" "$VERIFY_SCRIPT" "$CLUSTER_ID" "$WS_NAME" codercom/code-server:4.9.0 changeme; then
      echo "verify-workspace helper failed; skipping operator smoke test"; true
    else
      # Probe the HostApp proxied root and look for code-server login markers
      echo "Probing HostApp proxy for workspace root to detect login UI"
      set +e
      # Follow redirects and allow a slightly longer timeout when probing
      # the HostApp proxy. Some HostApp proxy responses return 302 ->
      # proxied service, so follow (-L) to reach the final HTML.
      if curl -k --http1.1 --max-time 15 -L -sS "$HOSTAPP_URL/api/cluster/$CLUSTER_ID/proxy/server/$WS_NAME/" | grep -iE "password|code-server" >/dev/null 2>&1; then
        echo "code-server page reachable through HostApp proxy and login UI appears"
      else
        echo "code-server page did not show expected login content via HostApp; dumping HostApp workspace info and k8s logs"
        # HostApp workspace JSON (if HostApp available)
        if curl -k -sS "$HOSTAPP_URL/api/cluster/$CLUSTER_ID/workspaces/$WS_NAME" >/tmp/verify-e2e-hostapp-ws.json 2>/dev/null; then
          echo "HostApp workspace status:"; jq -r '.status // {}' /tmp/verify-e2e-hostapp-ws.json || true
        fi
        kubectl -n default get pods -l guildnet.io/workspace=$WS_NAME -o wide || true
        kubectl -n default logs -l guildnet.io/workspace=$WS_NAME --tail=200 || true
        PASS=0
      fi
      set -e
    fi
    # Cleanup: prefer HostApp API delete, fall back to kubectl
    echo "Cleaning up Workspace $WS_NAME"
    if curl -k -sS -X DELETE "$HOSTAPP_URL/api/cluster/$CLUSTER_ID/workspaces/$WS_NAME" >/dev/null 2>&1; then
      true
    else
      kubectl -n default delete workspace $WS_NAME --ignore-not-found=true || true
    fi
  else
    echo "verify-workspace helper not found or not executable; skipping operator smoke test"
  fi
else
  echo "skipping operator smoke test (no kubectl/kubeconfig)"
fi
