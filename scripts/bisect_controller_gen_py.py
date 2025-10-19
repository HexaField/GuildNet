#!/usr/bin/env python3
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
FILE = REPO_ROOT / 'api' / 'v1alpha1' / 'types.go'
BACKUP = FILE.with_suffix('.go.bisect.orig')
CG = Path.home() / 'go' / 'bin' / 'controller-gen'
LOG = Path('/tmp/controller-gen-py-bisect.log')

if not FILE.exists():
    print('types.go not found:', FILE)
    sys.exit(2)

if not BACKUP.exists():
    BACKUP.write_bytes(FILE.read_bytes())

lines = BACKUP.read_text().splitlines()
N = len(lines)
print('lines:', N)

def write_with_removed(start, end):
    # start,end are 1-based inclusive
    out = []
    for i, l in enumerate(lines, start=1):
        if i < start or i > end:
            out.append(l)
        else:
            if i == start:
                out.append('/* __BISCT_REMOVE_START__')
            # skip other lines
            if i == end:
                out.append('__BISCT_REMOVE_END__ */')
    FILE.write_text('\n'.join(out) + '\n')

def restore():
    FILE.write_bytes(BACKUP.read_bytes())

def run_controller_gen():
    LOG.write_text('')
    cmd = [str(CG), 'object:headerFile=hack/boilerplate.go.txt', 'paths=./api/v1alpha1']
    # controller-gen expects arguments as a single string token per arg; run via shell to match earlier use
    full = f"{CG} object:headerFile=hack/boilerplate.go.txt paths=./api/v1alpha1"
    try:
        p = subprocess.run(full, shell=True, cwd=str(REPO_ROOT), capture_output=True, text=True, timeout=120)
    except Exception as e:
        LOG.write_text(str(e))
        return False, str(e)
    LOG.write_text(p.stdout + '\n' + p.stderr)
    out = p.stdout + p.stderr
    if 'panic: runtime error' in out:
        return True, out
    return False, out

# Confirm panic on original file
restore()
panic, out = run_controller_gen()
if not panic:
    print('controller-gen did NOT panic on original file; aborting bisect. Check log at', LOG)
    print(out[:1000])
    sys.exit(0)

print('confirmed panic on full file; starting binary-range bisect')

low = 1
high = N
found = None
while low <= high:
    mid = (low + high) // 2
    print(f'testing remove {low}..{mid}')
    write_with_removed(low, mid)
    did_panic, out = run_controller_gen()
    restore()
    if not did_panic:
        # removal fixed panic
        print(f'removing {low}..{mid} removes panic')
        found = (low, mid)
        # try to narrow left side
        high = mid - 1
    else:
        print(f'removing {low}..{mid} did NOT remove panic')
        low = mid + 1

if found:
    start, end = found
    print('Found removable range:', start, end)
    write_with_removed(start, end)
    outfile = Path('/tmp/types.bisect.removed.go')
    outfile.write_text(FILE.read_text())
    print('Wrote', outfile)
    print('Log at', LOG)
else:
    print('Could not find a single contiguous range that eliminates the panic.')
    print('See log at', LOG)
