#!/usr/bin/env bash
set -euo pipefail

# node-migrate.sh
# Guide migration from a MicroK8s-based setup to k0s-in-Docker.
# Non-destructive by default; requires explicit flags for destructive steps.

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
GN_KUBECONFIG=${GN_KUBECONFIG:-"$HOME/.guildnet/kubeconfig"}
PRESERVE_K0S=${PRESERVE_K0S:-1}

info(){ printf "[migrate] %s\n" "$*"; }
err(){ printf "[migrate:error] %s\n" "$*" >&2; }
need(){ command -v "$1" >/dev/null 2>&1 || { err "Missing: $1"; exit 2; }; }
need bash
need kubectl

# 1) Export current kubeconfig (MicroK8s helper if available)
if command -v microk8s >/dev/null 2>&1; then
  info "Detected microk8s; exporting kubeconfig (non-destructive)"
  mkdir -p "$(dirname "$GN_KUBECONFIG")"
  microk0skc=$(mktemp)
  if microk8s config >"$microk0skc" 2>/dev/null; then
    cp -f "$microk0skc" "$GN_KUBECONFIG"
    chmod 600 "$GN_KUBECONFIG"
    info "Wrote kubeconfig to $GN_KUBECONFIG"
  else
    err "microk8s config failed; continuing"
  fi
  rm -f "$microk0skc" || true
fi

# 2) Bring up k0s-in-Docker (optionally serve kube-API over tailnet)
info "Bringing up k0s node (Docker)"
TS_AUTHKEY=${TS_AUTHKEY:-} TS_LOGIN_SERVER=${TS_LOGIN_SERVER:-} \
  TS_SERVE_KUBEAPI=${TS_SERVE_KUBEAPI:-0} TS_ADD_SANS=${TS_ADD_SANS:-0} \
  bash "$ROOT/scripts/k0s-node-up.sh"

# 3) Apply CRDs, addons, DB, and deploy operator
info "Applying CRDs/addons and deploying operator"
make -C "$ROOT" deploy-k8s-addons || true
make -C "$ROOT" deploy-operator || true
make -C "$ROOT" ensure-operator-setup || true

# 4) Attach kubeconfig to Host App and apply defaults
info "Attaching kubeconfig to Host App and applying defaults"
SET_DEFAULTS=${SET_DEFAULTS:-1} bash "$ROOT/scripts/attach-local-k0s.sh" || true

info "Migration flow completed. Verify with: make verify-k0s && make verify-operator && make verify-e2e"
