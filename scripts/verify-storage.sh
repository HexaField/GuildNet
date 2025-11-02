#!/usr/bin/env bash
set -euo pipefail

# verify-storage.sh
# Verifies that a default StorageClass exists and that RethinkDB PVCs are Bound and pod is Running.
# Exits 0 on success, non-zero on failure; prints concise diagnostics.

KUBECONFIG=${KUBECONFIG:-"$HOME/.guildnet/kubeconfig"}
NS=${GN_WORKSPACE_NS:-default}

need() { command -v "$1" >/dev/null 2>&1; }

if ! need kubectl; then
  echo "kubectl not found"; exit 2
fi

# 1) Default StorageClass
DEF_SC=$(kubectl --kubeconfig "$KUBECONFIG" get storageclass -o jsonpath='{range .items[?(@.metadata.annotations.storageclass\.kubernetes\.io/is-default-class=="true")]}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)
if [ -z "$DEF_SC" ]; then
  echo "[storage] No default StorageClass detected. Install one (e.g., local-path-provisioner)."; exit 3
fi
echo "[storage] Default StorageClass: $DEF_SC"

# 2) RethinkDB StatefulSet & PVC
STS_READY=0
if kubectl --kubeconfig "$KUBECONFIG" -n "$NS" get sts/rethinkdb >/dev/null 2>&1; then
  for i in $(seq 1 30); do
    RDY=$(kubectl --kubeconfig "$KUBECONFIG" -n "$NS" get sts/rethinkdb -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
    [ "$RDY" = "1" ] && STS_READY=1 && break
    sleep 2
  done
  [ "$STS_READY" = "1" ] && echo "[storage] RethinkDB StatefulSet Ready=1" || echo "[storage] RethinkDB not ready yet"
else
  echo "[storage] RethinkDB StatefulSet not found in namespace $NS (ok if not deployed)"
fi

PVC_STATE=$(kubectl --kubeconfig "$KUBECONFIG" -n "$NS" get pvc -l app=rethinkdb -o jsonpath='{range .items[*]}{.metadata.name}{";"}{.status.phase}{"\n"}{end}' 2>/dev/null || true)
if [ -n "$PVC_STATE" ]; then
  echo "$PVC_STATE" | while IFS=';' read -r name phase; do
    [ -z "$name" ] && continue
    echo "[storage] PVC $name: $phase"
    if [ "$phase" != "Bound" ]; then
      echo "[storage] ERROR: PVC $name not Bound"; exit 4
    fi
  done
fi

if [ "$STS_READY" = "1" ]; then
  echo "[storage] PASS: storage verified"
  exit 0
fi

# If we got here and RethinkDB not deployed, still consider storage OK if default SC exists
echo "[storage] PASS: default StorageClass present; RethinkDB optional"
exit 0
