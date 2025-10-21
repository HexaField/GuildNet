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

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# Allow passing remote and remote dir as args or via env vars. Do not hardcode.
# Usage: FED_REMOTE=user@host [FED_REMOTE_DIR=~/GuildNet] ./scripts/verify-federation-e2e.sh
if [ "$#" -ge 1 ] && [[ "$1" != "--no-commit" ]]; then
  FED_REMOTE_ARG="$1"
  shift
fi

: ${FED_REMOTE:=${FED_REMOTE_ARG:-}}
if [ -z "${FED_REMOTE}" ]; then
  echo "FED_REMOTE is required. Example: FED_REMOTE=user@192.168.0.1 FED_REMOTE_DIR=~/GuildNet $0" >&2
  exit 2
fi

: ${FED_REMOTE_DIR:=${FED_REMOTE_DIR:-'~/GuildNet'}}

REMOTE="$FED_REMOTE"
REMOTE_DIR="$FED_REMOTE_DIR"

# Resolve remote-dir if it begins with ~/ to an absolute path on the remote user home.
REMOTE_DIR_PATH="$REMOTE_DIR"
if [[ "$REMOTE_DIR" == ~/* ]]; then
  # extract remote user (before @)
  REMOTE_USER="${REMOTE%%@*}"
  # strip leading ~/
  REMOTE_SUFFIX="${REMOTE_DIR#~/}"
  REMOTE_DIR_PATH="/home/${REMOTE_USER}/${REMOTE_SUFFIX}"
fi

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
echo "Ensuring remote directory exists and is writable..."
ssh "$REMOTE" "mkdir -p \"$REMOTE_DIR_PATH\" && test -w \"$REMOTE_DIR_PATH\" || echo 'WARNING: $REMOTE_DIR_PATH may not be writable by $USER' >&2"

rsync -avz --delete --exclude .git --exclude tmp --exclude node_modules "$REPO_ROOT/" "$REMOTE:$REMOTE_DIR_PATH/"

# Copy the remote helper script as well
scp "$REPO_ROOT/scripts/remote-run-verify-federation.sh" "$REMOTE:$REMOTE_DIR_PATH/scripts/"

# Run remote script (invoke with bash to avoid /bin/sh semantics)
echo "Running remote setup on $REMOTE..."
ssh "$REMOTE" "cd $REMOTE_DIR_PATH && bash ./scripts/remote-run-verify-federation.sh"

# After remote returns, run local verify-e2e to ensure HostApp + operator on local cluster are functioning
echo "Running local verify-e2e..."
make verify-e2e

echo "Multi-cluster federation e2e completed."

# Note: This script commits temporary changes locally; consider reverting if needed.

