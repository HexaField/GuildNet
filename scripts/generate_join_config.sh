#!/usr/bin/env bash
set -euo pipefail

# Generate a GuildNet join configuration (JSON) using the same logic as the server
# This script gathers local settings, optional CA, optional kubeconfig, and tailscale hints
# and emits a join bundle JSON to stdout or to --out file (default: guildnet.config).

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

OUT_FILE="guildnet.config"
GN_KUBECONFIG_DEFAULT="${GN_KUBECONFIG:-${KUBECONFIG:-$HOME/.kube/config}}"
KUBECONFIG_PATH="$GN_KUBECONFIG_DEFAULT"
HOSTAPP_URL="${GN_HOSTAPP_URL:-https://127.0.0.1:8090}"
INCLUDE_CA="${GN_INCLUDE_CA:-}"
LOGIN_SERVER="${GN_LOGIN_SERVER:-}"
AUTH_KEY="${GN_AUTH_KEY:-}"
HOSTNAME="${GN_HOSTNAME:-$(hostname)}"
NAME_LABEL="${GN_CLUSTER_NAME:-}"

# Per-cluster optional knobs (can be set via env or CLI later)
NAMESPACE=""
API_PROXY_URL=""
API_PROXY_FORCE_HTTP="0"
DISABLE_API_PROXY="0"
PREFER_POD_PROXY="0"
USE_PORT_FORWARD="0"
INGRESS_DOMAIN=""
INGRESS_CLASS_NAME=""
WORKSPACE_TLS_SECRET=""
CERT_MANAGER_ISSUER=""
INGRESS_AUTH_URL=""
INGRESS_AUTH_SIGNIN=""
IMAGE_PULL_SECRET=""
ORG_ID=""

usage() {
  cat <<USAGE
Usage: generate_join_config.sh [--out FILE] [--kubeconfig PATH]

Environment defaults (can be overridden):
  GN_KUBECONFIG, KUBECONFIG -> kubeconfig to embed (default: ~/.kube/config)
  GN_HOSTAPP_URL -> hostapp URL (default: https://127.0.0.1:8090)
  GN_INCLUDE_CA -> path to CA to include (optional)
  GN_LOGIN_SERVER, GN_AUTH_KEY, GN_HOSTNAME -> tailscale hints
USAGE
}

while [ $# -gt 0 ]; do
  case "$1" in
    --out) OUT_FILE="${2:-}"; shift 2 ;;
    --name) NAME_LABEL="${2:-}"; shift 2 ;;
    --kubeconfig) KUBECONFIG_PATH="${2:-}"; shift 2 ;;
    --help|-h) usage; exit 0 ;;
    *) echo "Unknown arg: $1" >&2; usage; exit 2 ;;
  esac
done

if ! command -v jq >/dev/null 2>&1; then
  echo "ERROR: jq is required" >&2
  exit 2
fi

# Read kubeconfig if present
KC_YAML=""
if [ -n "${KUBECONFIG_PATH:-}" ] && [ -s "${KUBECONFIG_PATH}" ]; then
  KC_YAML="$(cat "${KUBECONFIG_PATH}")"
else
  # fallback to known locations
  if [ -s "$HOME/.guildnet/kubeconfig" ]; then
    KC_YAML="$(cat "$HOME/.guildnet/kubeconfig")"
  elif [ -s "$HOME/.kube/config" ]; then
    KC_YAML="$(cat "$HOME/.kube/config")"
  else
    echo "WARN: no kubeconfig found; cluster.kubeconfig will be empty" >&2
  fi
fi

# Resolve CA to include
CA_PEM=""
if [ -n "$INCLUDE_CA" ] && [ -f "$INCLUDE_CA" ]; then
  CA_PEM="$(cat "$INCLUDE_CA")"
elif [ -f "$REPO_ROOT/certs/server.crt" ]; then
  CA_PEM="$(cat "$REPO_ROOT/certs/server.crt")"
fi

# Metadata
NOW_ISO="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
HOSTNAME_LOCAL="$(hostname 2>/dev/null || echo unknown)"
CREATOR_USER="${USER:-unknown}"

# Build JSON (same shape as server-side join bundle)
jq -n \
  --arg now "$NOW_ISO" \
  --arg host "$HOSTNAME_LOCAL" \
  --arg user "$CREATOR_USER" \
  --arg ui_url "$HOSTAPP_URL" \
  --arg ca_pem "$CA_PEM" \
  --arg hostapp_url "$HOSTAPP_URL" \
  --arg kc "$KC_YAML" \
  --arg name "$NAME_LABEL" \
  --arg login "$LOGIN_SERVER" \
  --arg auth "$AUTH_KEY" \
  --arg tsname "$HOSTNAME" \
  --arg ns "$NAMESPACE" \
  --arg api_proxy "$API_PROXY_URL" \
  --arg api_force "$API_PROXY_FORCE_HTTP" \
  --arg disable_proxy "$DISABLE_API_PROXY" \
  --arg prefer_pod "$PREFER_POD_PROXY" \
  --arg use_pf "$USE_PORT_FORWARD" \
  --arg dom "$INGRESS_DOMAIN" \
  --arg iclass "$INGRESS_CLASS_NAME" \
  --arg tlssec "$WORKSPACE_TLS_SECRET" \
  --arg issuer "$CERT_MANAGER_ISSUER" \
  --arg authurl "$INGRESS_AUTH_URL" \
  --arg authsignin "$INGRESS_AUTH_SIGNIN" \
  --arg imgsec "$IMAGE_PULL_SECRET" \
  --arg org "$ORG_ID" \
  '{
     version: 2,
     created_at: $now,
     creator: {host: $host, user: $user},
     ui: ({} 
          | (if ($ui_url|length)>0 then . + {vite_api_base: $ui_url} else . end)
          | (if ($ca_pem|length)>0 then . + {ca_pem: $ca_pem} else . end)),
     hostapp: ({} 
          | (if ($hostapp_url|length)>0 then . + {url: $hostapp_url} else . end)
          | (if ($ca_pem|length)>0 then . + {ca_pem: $ca_pem} else . end)),
     cluster: (
       {} 
       | . + { name: ($name // ""), kubeconfig: $kc }
       | (if ($ns|length)>0 then . + { namespace: $ns } else . end)
       | (if ($api_proxy|length)>0 then . + { api_proxy_url: $api_proxy } else . end)
       | (if ($api_force|length)>0 and ($api_force!="0") then . + { api_proxy_force_http: true } else . end)
       | (if ($disable_proxy|length)>0 and ($disable_proxy!="0") then . + { disable_api_proxy: true } else . end)
       | (if ($prefer_pod|length)>0 and ($prefer_pod!="0") then . + { prefer_pod_proxy: true } else . end)
       | (if ($use_pf|length)>0 and ($use_pf!="0") then . + { use_port_forward: true } else . end)
       | (if ($dom|length)>0 then . + { ingress_domain: $dom } else . end)
       | (if ($iclass|length)>0 then . + { ingress_class_name: $iclass } else . end)
       | (if ($tlssec|length)>0 then . + { workspace_tls_secret: $tlssec } else . end)
       | (if ($issuer|length)>0 then . + { cert_manager_issuer: $issuer } else . end)
       | (if ($authurl|length)>0 then . + { ingress_auth_url: $authurl } else . end)
       | (if ($authsignin|length)>0 then . + { ingress_auth_signin: $authsignin } else . end)
       | (if ($imgsec|length)>0 then . + { image_pull_secret: $imgsec } else . end)
       | (if ($org|length)>0 then . + { org_id: $org } else . end)
     ),
     tailscale: {
       login_server: ($login // ""),
       preauth_key: ($auth // ""),
       hostname: ($tsname // "")
     }
   }' > "$OUT_FILE"

chmod 600 "$OUT_FILE" || true

echo "join config written: $OUT_FILE"
