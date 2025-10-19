#!/usr/bin/env python3
"""Try removing pairs of top-level declarations to find a minimal reproducer.

This is a follow-up to structural_bisect.py. It enumerates all pairs
of top-level declarations (type/const/var/func) across the package and
comments out both declarations, runs controller-gen, and reports the
first pair whose removal prevents the panic.
"""
import re
import subprocess
from pathlib import Path
import sys
import itertools

REPO = Path(__file__).resolve().parents[1]
PKG_DIR = REPO / 'api' / 'v1alpha1'
CG = Path.home() / 'go' / 'bin' / 'controller-gen'
LOG = Path('/tmp/controller-gen-combinatorial-bisect.log')


def find_decls_in_file(p: Path):
    lines = p.read_text().splitlines()
    decls = []
    i = 0
    n = len(lines)
    tok_re = re.compile(r'^(type|const|var|func)\b')
    while i < n:
        line = lines[i]
        if tok_re.match(line):
            start = i + 1
            j = i + 1
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


def run_controller_gen(timeout=120):
    LOG.write_text('')
    cmd = f"{CG} object:headerFile=hack/boilerplate.go.txt paths=./api/v1alpha1"
    try:
        p = subprocess.run(cmd, shell=True, cwd=str(REPO), capture_output=True, text=True, timeout=timeout)
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
    out = txt[:start-1] + ["/* __COMBO_REMOVE_START__"] + txt[start-1:end] + ["__COMBO_REMOVE_END__ */"] + txt[end:]
    p.write_text('\n'.join(out) + '\n')


def restore_backup(p: Path, backup: Path):
    p.write_bytes(backup.read_bytes())


def main():
    if not CG.exists():
        print('controller-gen not found at', CG)
        sys.exit(2)

    files = list(sorted(PKG_DIR.glob('*.go')))
    all_decls = []  # list of (file, start, end, snippet, decl_id)
    for f in files:
        decls = find_decls_in_file(f)
        for d in decls:
            # create an id for readable reference
            decl_id = f"{f.name}:{d[0]}-{d[1]}"
            all_decls.append((f, d[0], d[1], d[2], decl_id))

    print(f'Found {len(all_decls)} top-level declarations across {len(files)} files')

    # backup
    backups = {}
    for f in files:
        b = f.with_suffix('.comb.bak')
        if not b.exists():
            b.write_bytes(f.read_bytes())
        backups[f] = b

    # confirm panic on original
    panic, out = run_controller_gen()
    if not panic:
        print('controller-gen did NOT panic on original package; see log at', LOG)
        print(out[:1000])
        return

    # enumerate pairs
    total_pairs = 0
    for (a_idx, a), (b_idx, b) in itertools.combinations(enumerate(all_decls), 2):
        total_pairs += 1
    print(f'Testing {total_pairs} declaration pairs (combinatorial pairs). This may take a while...')

    for (i, a), (j, b) in itertools.combinations(enumerate(all_decls), 2):
        fa, sa, ea, _, aid = a
        fb, sb, eb, _, bid = b
        print(f'Testing pair: [{aid}] + [{bid}] ({i+1}/{total_pairs})')
        # apply both comments
        try:
            comment_out_range(fa, sa, ea)
            # if same file and b is after a, indices might shift; restore from backup and re-comment intelligently
            if fa == fb:
                # restore file, then write comments for both by selecting the earlier then later
                restore_backup(fa, backups[fa])
                # determine order
                if sa <= sb:
                    comment_out_range(fa, sa, ea)
                    comment_out_range(fa, sb, eb)
                else:
                    comment_out_range(fa, sb, eb)
                    comment_out_range(fa, sa, ea)
            else:
                comment_out_range(fb, sb, eb)

            panic, out = run_controller_gen()
        finally:
            # restore both files to backups
            restore_backup(fa, backups[fa])
            restore_backup(fb, backups[fb])

        if not panic:
            print('Found a pair whose removal removed the panic:')
            print(' -', aid)
            print(' -', bid)
            # write annotated copies
            outpath = Path('/tmp') / f'combo_removed_{aid.replace("/","_")}_{bid.replace("/","_")}.txt'
            outpath.write_text(f'Removed declarations: {aid}, {bid}\n\ncontroller-gen output:\n\n{out}')
            print('Wrote annotated result to', outpath)
            print('Controller-gen log at', LOG)
            return

    print('No pair removal removed the panic. Consider trying triples or a different reduction strategy.')


if __name__ == '__main__':
    main()
