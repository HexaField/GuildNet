#!/usr/bin/env bash
set -euo pipefail
# Deploy a Tailscale subnet router DaemonSet into the current cluster.
# Requires: kubectl, TS_AUTHKEY; optional TS_LOGIN_SERVER, TS_ROUTES, TS_HOSTNAME.
# Uses hostNetwork and advertises routes for cluster subnets so other tailnet nodes (tsnet hostapp machines)
# can reach the kube‑API and service/pod CIDRs directly.

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
[ -f "$ROOT/.env" ] && . "$ROOT/.env"

export KUBECONFIG="${KUBECONFIG:-${GN_KUBECONFIG:-$HOME/.guildnet/kubeconfig}}"

need() { command -v "$1" >/dev/null 2>&1 || { echo "Missing: $1" >&2; exit 1; }; }
need kubectl

if [ -z "${TS_AUTHKEY:-}" ]; then
  echo "WARN: TS_AUTHKEY not set; skipping tailscale subnet router deploy." >&2
  exit 0
fi

# Normalize TS_AUTHKEY to canonical tskey-<base64url-no-pad> if it looks like raw hex
normalize_authkey() {
  local k="$1"
  k="${k## }"; k="${k%% }"
  if [ -z "$k" ]; then printf "%s" "$k"; return; fi
  case "$k" in
    tskey-*) printf "%s" "$k"; return ;;
  esac
  # detect even-length hex
  if echo "$k" | grep -Eq '^[0-9a-fA-F]+$' && [ $(( ${#k} % 2 )) -eq 0 ]; then
    if command -v xxd >/dev/null 2>&1; then
      local b64
      b64=$(printf "%s" "$k" | xxd -r -p | base64 | tr '+/' '-_' | tr -d '=')
      printf "tskey-%s" "$b64"
      return
    fi
  fi
  # Fallback: add tskey- prefix
  printf "tskey-%s" "$k"
}
AUTH_NORM=$(normalize_authkey "$TS_AUTHKEY")

# Local preflight: if running against a single-node local cluster, ensure host /dev/net/tun is available
# If the host already has a tailscaled or other process holding /dev/net/tun, the DaemonSet pods will crash
# with 'device or resource busy'. Detect that early and give the user a clear remediation path.
if [ -c /dev/net/tun ]; then
  if command -v lsof >/dev/null 2>&1; then
    if lsof /dev/net/tun >/dev/null 2>&1; then
      echo "ERROR: /dev/net/tun appears to be in use on this host (some process holds the TUN device)." >&2
      echo "This will make the tailscale subnet-router pods fail with 'TUN device ... is busy'." >&2
      echo "If this is a local single-node cluster and you intentionally run the host tailscaled, either stop it or remove the device before deploying the DaemonSet." >&2
      echo "Suggested remediation (run on the node hosting the kubelets):" >&2
      echo "  sudo systemctl stop tailscaled || true" >&2
      echo "  sudo pkill tailscaled || true" >&2
      echo "  sudo ip link delete tailscale0 || true" >&2
      echo "  sudo rm -rf /var/lib/tailscale/* || true" >&2
      echo "Then re-run this deploy script." >&2
      exit 1
    fi
  else
    echo "NOTE: /dev/net/tun exists on this host. I couldn't find 'lsof' to check if it's in use." >&2
    echo "If you get 'TUN device ... is busy' in pod logs after deploying, stop host tailscaled (systemctl stop tailscaled) or remove the tailscale0 interface and retry." >&2
  fi
fi

TS_LOGIN_SERVER=${TS_LOGIN_SERVER:-https://login.tailscale.com}
# Determine routes if not provided: PodCIDR from node.spec.podCIDR, Service CIDR from kube-proxy ConfigMap or default 10.96.0.0/12
if [ -z "${TS_ROUTES:-}" ]; then
  POD_CIDR=$(kubectl get node -o jsonpath='{.items[0].spec.podCIDR}' 2>/dev/null || echo "")
  if [ -z "$POD_CIDR" ]; then POD_CIDR="10.244.0.0/16"; fi
  # Try to extract clusterCIDR from kube-proxy config
  SVC_CIDR=""
  if kubectl -n kube-system get cm kube-proxy >/dev/null 2>&1; then
    # Attempt to parse clusterCIDR with awk to avoid external deps
    RAW=$(kubectl -n kube-system get cm kube-proxy -o jsonpath='{.data.config\.conf}' 2>/dev/null || echo "")
    if printf "%s" "$RAW" | grep -q "clusterCIDR"; then
      SVC_CIDR=$(printf "%s" "$RAW" | awk -F': ' '/clusterCIDR/ {gsub("\"","", $2); print $2; exit}')
    fi
  fi
  if [ -z "$SVC_CIDR" ]; then SVC_CIDR="10.96.0.0/12"; fi
  TS_ROUTES="$POD_CIDR,$SVC_CIDR"
fi
TS_ROUTES=${TS_ROUTES}
TS_HOSTNAME=${TS_HOSTNAME:-subnet-router}

cat <<YAML | kubectl apply -f -
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: tailscale-subnet-router
  namespace: kube-system
  labels:
    app: tailscale-subnet-router
spec:
  selector:
    matchLabels:
      app: tailscale-subnet-router
  template:
    metadata:
      labels:
        app: tailscale-subnet-router
    spec:
      hostNetwork: true
      dnsPolicy: ClusterFirstWithHostNet
      tolerations:
      - operator: Exists
      containers:
      - name: tailscale
        image: tailscale/tailscale:stable
        securityContext:
          capabilities:
            add: ["NET_ADMIN", "NET_RAW"]
          privileged: true
        env:
        - name: TS_AUTHKEY
          value: "${AUTH_NORM}"
        - name: TS_LOGIN_SERVER
          value: "${TS_LOGIN_SERVER}"
        - name: TS_ROUTES
          value: "${TS_ROUTES}"
        - name: TS_HOSTNAME
          value: "${TS_HOSTNAME}"
        volumeMounts:
        - name: state
          mountPath: /var/lib/tailscale
        - name: tun
          mountPath: /dev/net/tun
        args:
        - /bin/sh
        - -c
        - |
          set -e
          /usr/local/bin/tailscaled --state=/var/lib/tailscale/tailscaled.state &
          sleep 2
          # Allow headscale/Tailscale auth to be retried without crashing the pod
          set +e
          /usr/local/bin/tailscale up --authkey="${AUTH_NORM}" --login-server="${TS_LOGIN_SERVER}" --advertise-routes="${TS_ROUTES}" --hostname="${TS_HOSTNAME}" --accept-routes
          rc=\$?
          if [ \$rc -ne 0 ]; then
            echo "tailscale up failed with exit \$rc; continuing to run to allow later retries/log inspection"
          fi
          set -e
          tail -f /dev/null
      volumes:
      - name: state
        emptyDir: {}
      - name: tun
        hostPath:
          path: /dev/net/tun
          type: CharDevice
YAML

echo "Waiting for tailscale subnet router to be ready..."
if ! kubectl -n kube-system rollout status ds/tailscale-subnet-router --timeout=300s; then
  echo "DaemonSet not ready; showing pod status and recent logs:" >&2
  kubectl -n kube-system get pods -l app=tailscale-subnet-router -o wide || true
  for p in $(kubectl -n kube-system get pods -l app=tailscale-subnet-router -o name 2>/dev/null | sed 's#pod/##'); do
    echo "--- logs: $p (last 50 lines) ---"
    kubectl -n kube-system logs "$p" --tail=50 || true
  done
  exit 1
fi

# Best-effort: show pod status and logs hint
kubectl -n kube-system get pods -l app=tailscale-subnet-router -o wide || true
echo "Hint: kubectl -n kube-system logs -l app=tailscale-subnet-router -f --tail=100"
