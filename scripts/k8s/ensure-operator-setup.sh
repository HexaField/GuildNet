#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
NS=${GN_NAMESPACE:-guildnet-system}
DEPLOYMENT=${GN_OPERATOR_DEPLOYMENT:-workspace-operator}

info() { printf "[info] %s\n" "$*"; }
err() { printf "[error] %s\n" "$*" >&2; }

need() { command -v "$1" >/dev/null 2>&1 || { err "missing required: $1"; exit 2; } }
need kubectl
need jq || true

# Discover preauth key and login server. Prefer environment, then local headscale state.
LOGIN_SERVER=${TS_LOGIN_SERVER:-${GN_LOGIN_SERVER:-}}
AUTH_KEY=${TS_AUTHKEY:-${GN_AUTH_KEY:-}}

if [ -z "$AUTH_KEY" ] && [ -f "$HOME/.guildnet/headscale/preauth-guildnet.txt" ]; then
  AUTH_KEY=$(sed -n '1p' "$HOME/.guildnet/headscale/preauth-guildnet.txt" | tr -d '\n\r ')
fi
if [ -z "$LOGIN_SERVER" ] && [ -f "$HOME/.guildnet/headscale/server_url" ]; then
  LOGIN_SERVER=$(sed -n '1p' "$HOME/.guildnet/headscale/server_url" | tr -d '\n\r ')
fi

if [ -z "$LOGIN_SERVER" ] || [ -z "$AUTH_KEY" ]; then
  err "LOGIN_SERVER or AUTH_KEY not set. Export TS_LOGIN_SERVER/TS_AUTHKEY or ensure ~/.guildnet/headscale/* exists."
  exit 3
fi

info "Using login_server=$LOGIN_SERVER (len=$(echo -n $LOGIN_SERVER | wc -c))"
info "Using auth_key (len=$(echo -n $AUTH_KEY | wc -c))"

# Create operator-config JSON file
TMPCFG=$(mktemp)
cat > "$TMPCFG" <<JSON
{
  "login_server": "$LOGIN_SERVER",
  "auth_key": "$AUTH_KEY",
  "hostname": "host-app",
  "listen_local": "127.0.0.1:8090",
  "dial_timeout_ms": 3000
}
JSON

info "Applying ConfigMap operator-config in namespace $NS"
kubectl -n "$NS" create configmap operator-config --from-file=config.json=$TMPCFG --dry-run=client -o yaml | kubectl apply -f -

# operator-certs: create from local certs/ if present
if [ -d "$ROOT/certs" ]; then
  info "Creating operator-certs ConfigMap from $ROOT/certs"
  kubectl -n "$NS" create configmap operator-certs --from-file="$ROOT/certs" --dry-run=client -o yaml | kubectl apply -f -
else
  info "No local certs/ found at $ROOT/certs; skipping operator-certs creation"
fi

# Ensure deployment has the required volume and mount
info "Patching deployment $DEPLOYMENT to ensure operator-certs volume + mount"
PATCH='[
  {"op":"add","path":"/spec/template/spec/volumes/-","value":{"name":"operator-certs","configMap":{"name":"operator-certs"}}},
  {"op":"add","path":"/spec/template/spec/containers/0/volumeMounts/-","value":{"name":"operator-certs","mountPath":"/root/.guildnet/state/certs"}}
]'

set +e
kubectl -n "$NS" patch deployment "$DEPLOYMENT" --type=json -p "$PATCH" 2>/dev/null
PATCH_RC=$?
set -e
if [ $PATCH_RC -ne 0 ]; then
  info "Patch may have failed if volume/mount already existed; proceeding"
fi

info "Restarting deployment $DEPLOYMENT"
kubectl -n "$NS" rollout restart deployment "$DEPLOYMENT"
kubectl -n "$NS" rollout status deployment "$DEPLOYMENT" --timeout=180s || true

info "Deployment patched and restarted; recent logs follow"
kubectl -n "$NS" logs -l app=workspace-operator --tail=300 || true

info "Done. If operator remains in NeedsLogin check headscale server_url, preauth key, and network reachability."

rm -f "$TMPCFG"
