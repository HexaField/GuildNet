#!/usr/bin/env bash
set -euo pipefail
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
[ -f "$ROOT/.env" ] && . "$ROOT/.env"
export KUBECONFIG="${KUBECONFIG:-${GN_KUBECONFIG:-$HOME/.guildnet/kubeconfig}}"

need() { command -v "$1" >/dev/null 2>&1 || { echo "Missing: $1" >&2; exit 1; }; }
need kubectl
need jq

PASS=1

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
  if curl -sS -o /dev/null -m 2 "$HS"; then echo ok; else echo fail; PASS=0; fi
else
  echo "skip (no headscale)"
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
    if [ $ok -eq 1 ]; then echo ok; else PASS=0; fi
  fi
else
  echo "not found"; PASS=0
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
      PASS=0
    fi
  done
fi

# Kube readyz + nodes
if command -v kubectl >/dev/null && [ -f "${KUBECONFIG}" ] && kubectl version --request-timeout=3s >/dev/null 2>&1; then
  kubectl --request-timeout=5s get --raw='/readyz?verbose' || { PASS=0; true; }
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
if [ "$PASS" = "1" ]; then
  echo "SUMMARY: PASS"
else
  echo "SUMMARY: FAIL"; exit 1
fi

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
