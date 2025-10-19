#!/usr/bin/env python3
import re
import subprocess
from pathlib import Path
import sys

REPO = Path(__file__).resolve().parents[1]
PKG_DIR = REPO / 'api' / 'v1alpha1'
CG = Path.home() / 'go' / 'bin' / 'controller-gen'
LOG = Path('/tmp/controller-gen-struct-bisect.log')

def find_decls_in_file(p: Path):
    lines = p.read_text().splitlines()
    decls = []
    i = 0
    n = len(lines)
    tok_re = re.compile(r'^(type|const|var|func)\b')
    while i < n:
        line = lines[i]
        if tok_re.match(line):
            start = i+1
            # find end: next top-level token or EOF
            j = i+1
            while j < n and not tok_re.match(lines[j]):
                j += 1
            end = j  # exclusive
            decls.append((start, end, '\n'.join(lines[start-1:end])))
            i = j
        elif line.strip().startswith('package '):
            i += 1
        else:
            i += 1
    return decls

def run_controller_gen():
    LOG.write_text('')
    cmd = f"{CG} object:headerFile=hack/boilerplate.go.txt paths=./api/v1alpha1"
    try:
        p = subprocess.run(cmd, shell=True, cwd=str(REPO), capture_output=True, text=True, timeout=120)
    except Exception as e:
        LOG.write_text(str(e))
        return True, str(e)
    LOG.write_text(p.stdout + '\n' + p.stderr)
    out = p.stdout + p.stderr
    if 'panic: runtime error' in out:
        return True, out
    return False, out

def comment_out_range(p: Path, start: int, end: int):
    txt = p.read_text().splitlines()
    out = txt[:start-1] + ["/* __STRUCT_REMOVE_START__"] + txt[start-1:end] + ["__STRUCT_REMOVE_END__ */"] + txt[end:]
    p.write_text('\n'.join(out) + '\n')

def restore_backup(p: Path, backup: Path):
    p.write_bytes(backup.read_bytes())

def main():
    if not CG.exists():
        print('controller-gen not found at', CG)
        sys.exit(2)
    files = list(sorted(PKG_DIR.glob('*.go')))
    all_decls = []
    for f in files:
        decls = find_decls_in_file(f)
        for d in decls:
            all_decls.append((f, d[0], d[1], d[2]))

    print(f'Found {len(all_decls)} top-level declarations across {len(files)} files')
    # backup
    backups = {}
    for f in files:
        b = f.with_suffix('.struct.bak')
        if not b.exists():
            b.write_bytes(f.read_bytes())
        backups[f] = b

    # confirm panic on original
    panic, out = run_controller_gen()
    if not panic:
        print('controller-gen did NOT panic on original package; see log at', LOG)
        print(out[:1000])
        return

    for idx, (f, start, end, snippet) in enumerate(all_decls, start=1):
        print(f'Testing decl #{idx}/{len(all_decls)}: {f} lines {start}-{end} -> preview: {snippet.splitlines()[0][:120]!r}')
        comment_out_range(f, start, end)
        panic, out = run_controller_gen()
        # restore
        restore_backup(f, backups[f])
        if not panic:
            print('Removing this declaration removed the panic:')
            print(f'File: {f}, lines: {start}-{end}')
            # write a copy showing the removed decl
            outpath = Path('/tmp') / f'{f.name}.removed_{start}_{end}.go'
            comment_out_range(f, start, end)
            outpath.write_text(f.read_text())
            # restore
            restore_backup(f, backups[f])
            print('Wrote annotated copy to', outpath)
            print('Controller-gen log at', LOG)
            return
    print('No single top-level declaration removal removed the panic. Consider more advanced reduction (combinations).')

if __name__ == '__main__':
    main()
