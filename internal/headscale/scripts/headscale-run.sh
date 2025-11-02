#!/usr/bin/env bash
set -euo pipefail
cmd=${1-}
arg=${2-}
if [[ "$cmd" == "up" && "$arg" == "--json" ]]; then
  cat <<'JSON'
{"server_url":"http://127.0.0.1:8082","container":"headscale-test","image":"ghcr.io/juanfont/headscale:0.27.0","port":8082}
JSON
  exit 0
fi
if [[ "$cmd" == "preauth-key" ]]; then
  user=${2-"hostapp"}
  # support optional --json as third arg
  if [[ "${3-}" == "--json" || "${2-}" == "--json" ]]; then
    cat <<JSON
{"hex":"d8a3743ca2aefc23682832cdab2a819aba64a03fae845fb245fb2","tskey":"tskey-TESTKEY"}
JSON
    exit 0
  else
    echo "tskey-TESTKEY"
    exit 0
  fi
fi
# default
echo "{}"
exit 0
