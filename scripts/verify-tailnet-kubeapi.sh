#!/usr/bin/env bash
set -euo pipefail

# verify-tailnet-kubeapi.sh
# Verifies that kube-API is served over Tailscale (serve tcp) and, if SANs were injected, that
# the server cert includes the tailnet IP. Prints diagnostics and returns non-zero on failure.

GN_KUBECONFIG=${GN_KUBECONFIG:-"$HOME/.guildnet/kubeconfig"}
TAIL_CONT=${TAIL_CONT:-guildnet-tailscale}

need() { command -v "$1" >/dev/null 2>&1; }

if ! need docker; then
  echo "docker not found"; exit 2
fi

if ! docker ps --format '{{.Names}}' | grep -q "^${TAIL_CONT}$"; then
  echo "[tailnet] tailscale container not running (${TAIL_CONT}); start with TS_AUTHKEY and rerun"; exit 3
fi

# Extract local kube-API port from kubeconfig (expects https://127.0.0.1:<port>)
if [ ! -f "$GN_KUBECONFIG" ]; then
  echo "[tailnet] kubeconfig not found at $GN_KUBECONFIG"; exit 4
fi
PORT=$(awk '/server:/ {print $2}' "$GN_KUBECONFIG" | sed -n 's#https://127.0.0.1:\([0-9][0-9]*\).*#\1#p' | head -n1)
[ -z "$PORT" ] && echo "[tailnet] failed to parse kube-API port from kubeconfig" && exit 5

echo "[tailnet] kube-API local port: $PORT"

# Get tailnet IPv4
TAIL_IP=$(docker exec "$TAIL_CONT" tailscale ip -4 2>/dev/null | sed -n '1p' | tr -d '\r' || true)
if [ -z "$TAIL_IP" ]; then
  echo "[tailnet] no tailnet IPv4 detected"; exit 6
fi

echo "[tailnet] tailnet IPv4: $TAIL_IP"

# Attempt TCP connect and TLS handshake via openssl; requires kube-API to be served over tailscale (serve tcp)
set +e
CERT_TXT=$(echo | openssl s_client -connect "${TAIL_IP}:${PORT}" -servername "$TAIL_IP" -tls1_2 2>/dev/null | openssl x509 -noout -text 2>/dev/null)
RC=$?
set -e
if [ $RC -ne 0 ] || [ -z "$CERT_TXT" ]; then
  echo "[tailnet] TLS connect failed to ${TAIL_IP}:${PORT}. Ensure TS_SERVE_KUBEAPI=1 or run: make ts-serve-kubeapi"; exit 7
fi

echo "[tailnet] TLS handshake OK"

# Check SANs for the tailnet IP (best-effort)
if echo "$CERT_TXT" | grep -A1 'Subject Alternative Name' | grep -q "$TAIL_IP"; then
  echo "[tailnet] PASS: Cert SANs include tailnet IP $TAIL_IP"
else
  echo "[tailnet] WARN: Cert SANs do not include $TAIL_IP (set TS_ADD_SANS=1 when bringing up MicroK8s via scripts/microk8s-up.sh)."
fi

exit 0
