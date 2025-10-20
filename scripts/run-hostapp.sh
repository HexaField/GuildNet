#!/usr/bin/env bash
set -euo pipefail

# Simple launcher for GuildNet HostApp (no supervision, no DB lock handling)
ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
BIN="${ROOT_DIR}/bin/hostapp"

# Do not auto-prefer per-repo kubeconfig in production mode.
# For compatibility, set GN_USE_GUILDNET_KUBECONFIG=1 to preserve previous behavior.
if [ "${GN_USE_GUILDNET_KUBECONFIG:-0}" = "1" ] && [ -f "$HOME/.guildnet/kubeconfig" ]; then
  export KUBECONFIG="${KUBECONFIG:-$HOME/.guildnet/kubeconfig}"
fi

# Determine listen address (must match server logic): env LISTEN_LOCAL or default 127.0.0.1:8090
LISTEN_LOCAL="${LISTEN_LOCAL:-}"
if [ -z "$LISTEN_LOCAL" ]; then
  LISTEN_LOCAL="127.0.0.1:8090"
fi

# Extract port from LISTEN_LOCAL
HOST_PART=${LISTEN_LOCAL%:*}
PORT_PART=${LISTEN_LOCAL##*:}
if ! [[ "$PORT_PART" =~ ^[0-9]+$ ]]; then
  echo "Invalid LISTEN_LOCAL (expected host:port): $LISTEN_LOCAL" >&2
  exit 2
fi

# If an existing hostapp is listening on this port (v4 or v6), terminate it first
existing_pids=()
# Prefer ss on Linux; fall back to lsof/ps which are more portable (macOS has no /proc)
if command -v ss >/dev/null 2>&1; then
  # Linux path: parse ss output for PIDs
  while read -r line; do
    pid=$(echo "$line" | sed -n 's/.*pid=\([0-9]\+\).*/\1/p' | head -n1)
    [ -n "$pid" ] && existing_pids+=("$pid")
  done < <(ss -ltnp 2>/dev/null | awk '/LISTEN/ {print}' | grep -E ":${PORT_PART}\\b" || true)
else
  # Portable fallback: use lsof to find processes listening on the port
  if command -v lsof >/dev/null 2>&1; then
    while read -r pid; do
      [ -n "$pid" ] && existing_pids+=("$pid")
    done < <(lsof -nP -iTCP:${PORT_PART} -sTCP:LISTEN -t 2>/dev/null || true)
  else
    # Last resort: use netstat+ps parsing (very portable but brittle)
    if command -v netstat >/dev/null 2>&1; then
      while read -r line; do
        pid=$(echo "$line" | awk '{print $NF}' | sed -E 's#/.*##' | tr -d '()')
        [ -n "$pid" ] && existing_pids+=("$pid")
      done < <(netstat -anv 2>/dev/null | grep LISTEN | grep ".${PORT_PART} " || true)
    fi
  fi
fi

# De-dup PIDs (avoid mapfile/readarray for macOS bash compatibility)
len=${#existing_pids[@]}
if [ "${len:-0}" -gt 0 ]; then
  tmp=$(printf "%s\n" "${existing_pids[@]:-}" | awk 'NF' | sort -u || true)
  existing_pids=()
  if [ -n "$tmp" ]; then
    while IFS= read -r l; do
      existing_pids+=("$l")
    done <<< "$tmp"
  fi
fi

kill_count=0
  for pid in "${existing_pids[@]:-}"; do
    # Detect platform and inspect process command in a portable way
    is_hostapp=0
    if [ -d "/proc/$pid" ]; then
      exe=$(readlink "/proc/$pid/exe" 2>/dev/null || true)
      if echo "$exe" | grep -q "/hostapp$"; then
        is_hostapp=1
      else
        if tr '\0' ' ' < "/proc/$pid/cmdline" 2>/dev/null | grep -q "hostapp"; then
          is_hostapp=1
        fi
      fi
    else
      # macOS / BSD: use ps to get the command line
      cmdline=$(ps -p "$pid" -o command= 2>/dev/null || true)
      if echo "$cmdline" | grep -q "hostapp"; then
        is_hostapp=1
      fi
    fi
    if [ "$is_hostapp" -eq 1 ]; then
      echo "Stopping existing hostapp (pid=$pid) on port $PORT_PART..."
      kill "$pid" 2>/dev/null || true
      kill_count=$((kill_count+1))
    fi
  done

if [ "$kill_count" -gt 0 ]; then
  # Wait briefly for processes to exit; force kill if needed
  end=$((SECONDS + 5))
  while [ $SECONDS -lt $end ]; do
    alive=0
    for pid in "${existing_pids[@]}"; do
      if kill -0 "$pid" 2>/dev/null; then alive=1; fi
    done
    [ $alive -eq 0 ] && break
    sleep 0.2
  done
  for pid in "${existing_pids[@]}"; do
    if kill -0 "$pid" 2>/dev/null; then
      echo "Force killing hostapp (pid=$pid)"
      kill -KILL "$pid" 2>/dev/null || true
    fi
  done
fi

# As a safeguard, if a non-hostapp process is still listening on the port, abort
# As a safeguard, if a non-hostapp process is still listening on the port, abort
listener_check_cmd=""
listeners_cmd=""
if command -v ss >/dev/null 2>&1; then
  listener_check_cmd="ss -ltnp 2>/dev/null | awk '/LISTEN/ {print}' | grep -E ":${PORT_PART}\\b""
  listeners_cmd="ss -ltnp 2>/dev/null | awk '/LISTEN/ {print}' | grep -E ":${PORT_PART}\\b" | sed -n 's/.*pid=\\([0-9]\\+\\).*/\\1/p' | sort -u"
elif command -v lsof >/dev/null 2>&1; then
  listener_check_cmd="lsof -nP -iTCP:${PORT_PART} -sTCP:LISTEN 2>/dev/null"
  listeners_cmd="lsof -nP -iTCP:${PORT_PART} -sTCP:LISTEN -t 2>/dev/null | sort -u"
else
  listener_check_cmd="netstat -anv 2>/dev/null | grep LISTEN | grep ".${PORT_PART} ""
  listeners_cmd="netstat -anv 2>/dev/null | grep LISTEN | grep ".${PORT_PART} " | awk '{print \$NF}' | sed -E 's#/.*##' | tr -d '()' | sort -u"
fi

if [ -n "$listener_check_cmd" ] && eval "$listener_check_cmd" >/dev/null 2>&1; then
  listeners=$(eval "$listeners_cmd" || true)
  ours=0
  for pid in $listeners; do
    exe=""
    if [ -d "/proc/$pid" ]; then
      exe=$(readlink "/proc/$pid/exe" 2>/dev/null || true)
    else
      exe=$(ps -p "$pid" -o command= 2>/dev/null || true)
    fi
    if echo "$exe" | grep -q "/hostapp$\|hostapp"; then ours=1; fi
  done
  if [ "$ours" -eq 0 ]; then
    echo "Port ${PORT_PART} is in use by another process; attempting best-effort kill to free it." >&2
    for pid in $listeners; do
      echo "Attempting to kill pid=$pid" >&2
      kill "$pid" 2>/dev/null || kill -9 "$pid" 2>/dev/null || true
    done
    sleep 1
    # Re-check listeners; if still used, print and continue (we will still try to start)
    if eval "$listener_check_cmd" >/dev/null 2>&1; then
      echo "Warning: port ${PORT_PART} still in use after kill attempts:" >&2
      eval "$listener_check_cmd" || true
    fi
  fi
fi

exec "$BIN" serve
