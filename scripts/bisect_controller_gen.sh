#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# Ensure GOPATH and PATH include local bin where controller-gen may have been installed
GOPATH_DEFAULT="${GOPATH:-$HOME/go}"
export GOPATH="$GOPATH_DEFAULT"
export PATH="$PATH:$GOPATH/bin"
FILE_REL=api/v1alpha1/types.go
FILE="$REPO_ROOT/$FILE_REL"
BACKUP="$FILE.orig.bisect"
LOG=/tmp/controller-gen-bisect.log

if [ ! -f "$FILE" ]; then
  echo "file not found: $FILE" >&2
  exit 2
fi

if [ ! -f "$BACKUP" ]; then
  cp "$FILE" "$BACKUP"
fi

count_lines() {
  wc -l < "$BACKUP" | tr -d ' '
}

run_controller_gen() {
  echo "running controller-gen (this may take a bit)…"
  # prefer a locally-installed controller-gen
  if [ -x "$GOPATH/bin/controller-gen" ]; then
    echo "using controller-gen at $GOPATH/bin/controller-gen"
    GOTRACEBACK=all "$GOPATH/bin/controller-gen" object:headerFile=hack/boilerplate.go.txt paths=./$FILE_REL 2>&1 | tee "$LOG" || true
  elif command -v controller-gen >/dev/null 2>&1; then
    GOTRACEBACK=all controller-gen object:headerFile=hack/boilerplate.go.txt paths=./$FILE_REL 2>&1 | tee "$LOG" || true
  else
    # fall back to containerized run
    docker run --rm -v "$REPO_ROOT":/src -w /src golang:1.23 bash -lc \
      "export GOPATH=/go; export PATH=\$PATH:/go/bin; go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.12.0 >/dev/null 2>&1 || true; GOTRACEBACK=all controller-gen object:headerFile=/src/hack/boilerplate.go.txt paths=./$FILE_REL 2>&1 || true" | tee "$LOG"
  fi

  # If log contains a runtime panic, indicate failure (1). Otherwise success (0).
  if grep -q "panic: runtime error" "$LOG"; then
    return 1
  fi
  return 0
}

comment_range() {
  local start=$1
  local end=$2
  awk -v s=$start -v e=$end 'NR < s {print} NR == s {print "/* __BISCT_REMOVE_START__"} NR > s && NR < e { next } NR == e {print "__BISCT_REMOVE_END__ */"} NR > e {print}' "$BACKUP" > "$FILE"
}

restore() {
  cp "$BACKUP" "$FILE"
}

N=$(count_lines)
echo "types.go lines: $N"

low=1
high=$N
found_start=-1
found_end=-1

# First ensure the original file panics (otherwise nothing to bisect)
restore
if run_controller_gen; then
  echo "controller-gen did NOT panic on original file; nothing to bisect. Check logs at $LOG"
  exit 0
fi

echo "confirmed panic on full file; starting binary-range bisect"

while [ $low -le $high ]; do
  mid=$(( (low + high) / 2 ))
  echo "testing removing lines $low..$mid"
  comment_range $low $mid
  if run_controller_gen; then
    # removing this chunk fixed the panic; record and narrow left side
    echo "removing $low..$mid removes panic"
    found_start=$low
    found_end=$mid
    # try to find smaller chunk on the left side
    high=$(( mid - 1 ))
  else
    # still panics; need to move right
    echo "removing $low..$mid did NOT remove panic"
    low=$(( mid + 1 ))
  fi
  # restore backup for next iteration
  restore
done

if [ $found_start -ne -1 ]; then
  echo "Found a removable range that removes panic: $found_start..$found_end"
  echo "Writing an annotated copy to /tmp/types.bisect.removed.go"
  comment_range $found_start $found_end
  cp "$FILE" /tmp/types.bisect.removed.go
  echo "Bisect log: $LOG"
  exit 0
else
  echo "Could not find a single contiguous range via this simple bisect that eliminates the panic."
  echo "Logs at: $LOG"
  exit 3
fi
