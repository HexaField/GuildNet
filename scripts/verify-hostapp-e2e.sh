#!/usr/bin/env bash
set -euo pipefail

# Run the opt-in HostApp integration/e2e test. This test starts a real hostapp
# binary in a temporary HOME and exercises the headscale create + settings flow.
# It is destructive to the local environment only insofar as it starts a hostapp
# process that will be killed when the test completes.

if [ "${RUN_INTEGRATION:-}" != "1" ]; then
  echo "This script is opt-in. Set RUN_INTEGRATION=1 to run the test."
  exit 1
fi

echo "Running HostApp headscale e2e test via HostApp API (this will take ~30s)"
# Prefer API-driven verifier which exercises the server endpoints for headscale + cluster creation.
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
exec "$ROOT/scripts/verify-hostapp-api-e2e.sh"
