#!/usr/bin/env bash
set -euo pipefail

# verify-federation-e2e.sh
# Orchestrates a multi-cluster E2E test using local microk8s and a remote microk8s host accessible via SSH.
# Requires:
#   FED_REMOTE - user@host for the remote machine
#   FED_REMOTE_DIR - path on remote where the repo should be synced
# Environment:
#   This script will commit local changes, rsync repo to remote, run remote setup script and then
#   run local verify steps. It is destructive and intended for CI or an isolated test environment.

: ${FED_REMOTE:?Need to set FED_REMOTE (e.g. user@remote)}
: ${FED_REMOTE_DIR:?Need to set FED_REMOTE_DIR (remote path to place repo)}

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
REMOTE="$FED_REMOTE"
REMOTE_DIR="$FED_REMOTE_DIR"

echo "Repo root: $REPO_ROOT"
echo "Remote: $REMOTE -> $REMOTE_DIR"

# Allow skipping temporary commit by passing --no-commit
NO_COMMIT=0
if [ "${1:-}" = "--no-commit" ]; then
  NO_COMMIT=1
fi

# Ensure working tree is clean or commit local changes (unless skipped)
if ! git diff --quiet || ! git diff --staged --quiet; then
  if [ "$NO_COMMIT" -eq 1 ]; then
    echo "Uncommitted changes detected; --no-commit specified, will rsync working tree without committing."
  else
    echo "Uncommitted changes detected. Committing with temporary message..."
    git add -A
    git commit -m "ci: temporary commit for verify-federation-e2e" || true
  fi
fi

# Ensure remote dir exists and rsync the repo
echo "Syncing repo to remote..."
rsync -avz --delete --exclude .git --exclude tmp --exclude node_modules "$REPO_ROOT/" "$REMOTE:$REMOTE_DIR/"

# Copy the remote helper script as well
scp "$REPO_ROOT/scripts/remote-run-verify-federation.sh" "$REMOTE:$REMOTE_DIR/scripts/"

# Run remote script (invoke with bash to avoid /bin/sh semantics)
echo "Running remote setup on $REMOTE..."
ssh "$REMOTE" "cd $REMOTE_DIR && bash ./scripts/remote-run-verify-federation.sh"

# After remote returns, run local verify-e2e to ensure HostApp + operator on local cluster are functioning
echo "Running local verify-e2e..."
make verify-e2e

echo "Multi-cluster federation e2e completed."

# Note: This script commits temporary changes locally; consider reverting if needed.

