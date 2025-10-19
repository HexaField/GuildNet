#!/usr/bin/env python3
import itertools
import subprocess
from pathlib import Path
import tempfile
import shutil
import sys

REPO = Path(__file__).resolve().parents[1]
PKG_DIR = REPO / 'api' / 'v1alpha1'
FILES = sorted([p.name for p in PKG_DIR.glob('*.go')])
CG = Path.home() / 'go' / 'bin' / 'controller-gen'

def run_subset(files):
    # create temp module
    with tempfile.TemporaryDirectory() as td:
        td = Path(td)
        # create go.mod
        (td / 'go.mod').write_text('module tmp.repro\n\ngo 1.23\n')
        # copy hack boilerplate if present
        if (REPO / 'hack' / 'boilerplate.go.txt').exists():
            shutil.copy(REPO / 'hack' / 'boilerplate.go.txt', td / 'boilerplate.go.txt')
        # copy selected files
        for f in files:
            shutil.copy(PKG_DIR / f, td / f)
        # run controller-gen
        cmd = f'{CG} object:headerFile=boilerplate.go.txt paths=./'
        try:
            p = subprocess.run(cmd, shell=True, cwd=str(td), capture_output=True, text=True, timeout=120)
        except Exception as e:
            return True, str(e)
        out = p.stdout + p.stderr
        panic = 'panic: runtime error' in out
        return panic, out

def main():
    if not CG.exists():
        print('controller-gen not found at', CG)
        sys.exit(2)
    print('Files in package:', FILES)
    # try increasing subset sizes to find minimal reproducer
    for r in range(1, len(FILES)+1):
        print(f'testing subsets of size {r}...')
        for combo in itertools.combinations(FILES, r):
            print(' testing', combo)
            panic, out = run_subset(combo)
            if panic:
                print(' Found reproducer with files:', combo)
                # write debug output
                reprodir = Path('/tmp/guildnet_repro_api')
                if reprodir.exists():
                    shutil.rmtree(reprodir)
                reprodir.mkdir()
                (reprodir / 'go.mod').write_text('module tmp.repro\n\ngo 1.23\n')
                if (REPO / 'hack' / 'boilerplate.go.txt').exists():
                    shutil.copy(REPO / 'hack' / 'boilerplate.go.txt', reprodir / 'boilerplate.go.txt')
                for f in combo:
                    shutil.copy(PKG_DIR / f, reprodir / f)
                (reprodir / 'controller-gen.log').write_text(out)
                print(' Repro written to', reprodir)
                return
    print('No subset reproduces the panic')

if __name__ == '__main__':
    main()
