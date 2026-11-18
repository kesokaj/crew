#!/usr/bin/env python3
"""Checks that the repository is clean of secrets (design 16.12).

  python3 tools/check-clean.py          checks every file git tracks or would add; in a tree with
                                        no .git, as git archive writes one, every file
  python3 tools/check-clean.py --staged checks what is staged, the check before every commit

Four checks:
  1. No file with the name of a secret is tracked: an install file, a key, a login.
  2. No built program is tracked (an ELF or Mach-O file): a release is built in images from the
     source, and a program no reviewer can read never ships (AGENTS.md, Reproducible).
  3. spike/scrub.py finds no secret value and no token shape in any file. The values are read
     from spike/raw/scrub-values.txt, which git ignores. Where that file is missing, as on a
     public clone, the token shapes are still checked.
  4. Public hygiene (runs even without scrub-values.txt): no personal email domains and no
     workstation /home/<user> paths or file:///home/ URIs.
It needs only Python 3 and its standard library.
"""
import os
import pathlib
import re
import subprocess
import sys
import tempfile

ROOT = pathlib.Path(__file__).resolve().parent.parent
SCRUB = ROOT / 'spike' / 'scrub.py'
VALUES = ROOT / 'spike' / 'raw' / 'scrub-values.txt'
# Names that hold a secret. .env.example is the one install file that belongs in the repository.
SECRET_NAMES = re.compile(
    r'(^|/)(\.env(\..+)?|crew\.env|secret\.key|.+\.pem|.+\.key|.+\.tfvars|.+\.tfstate'
    r'|.+\.p12|.+\.pfx|.+\.jks|\.pgpass|kubeconfig'
    r'|\.credentials\.json|credentials\.json|token\.json|antigravity-oauth\.json|antigravity-oauth-token|id_(rsa|ed25519|ecdsa))$')
ALLOWED = {'.env.example'}
# The first bytes of a built program: ELF, and Mach-O in its two byte orders and as a fat file.
PROGRAMS = (b'\x7fELF', b'\xcf\xfa\xed\xfe', b'\xfe\xed\xfa\xcf', b'\xca\xfe\xba\xbe')

# Public hygiene checks that always run without needing gitignored scrub-values.txt:
# print file:line:kind only, never matched secret or personal values.
PERSONAL_EMAIL_DOMAIN_RE = re.compile(
    r'@(?:gmail\.com|googlemail\.com|outlook\.com|hotmail\.com|yahoo\.com|icloud\.com|proton\.me|protonmail\.com|styrman\.xyz)\b',
    re.I,
)
FILE_HOME_RE = re.compile(r'file:///home/([A-Za-z0-9_-]+)')
ABS_HOME_RE = re.compile(r'(?<![A-Za-z0-9_./}>-])/home/([A-Za-z0-9_-]+)')
ALLOWED_HOME_USERS = {'agent', 'crew', 'runner', 'nonroot', 'node', 'ubuntu', 'debian', 'user', 'alice', 'bob', 'airflow', 'tree'}
CATALOG_PREFIX = 'harness/skills/google-catalog/catalog/'


def is_program_bytes(data):
    return bool(data and data[:4] in PROGRAMS)


def is_program(path):
    try:
        with open(path, 'rb') as f:
            return is_program_bytes(f.read(4))
    except OSError:
        return False


def read_entry_bytes(rel, root=ROOT, staged=False):
    if staged:
        try:
            return subprocess.run(
                ['git', '-C', str(root), 'show', f':{rel}'],
                check=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            ).stdout
        except subprocess.CalledProcessError:
            return None
    try:
        return (root / rel).read_bytes()
    except OSError:
        return None


def check_public_hygiene(names, root=ROOT, staged=False):
    bad = []
    for rel in names:
        if rel.startswith(CATALOG_PREFIX):
            continue
        data = read_entry_bytes(rel, root=root, staged=staged)
        if data is None or b'\x00' in data:
            continue
        text = data.decode('utf-8', errors='replace')
        for idx, line in enumerate(text.splitlines(), 1):
            if PERSONAL_EMAIL_DOMAIN_RE.search(line):
                bad.append(f'{rel}:{idx}: personal-email-domain')
            for m in FILE_HOME_RE.finditer(line):
                if m.group(1) not in ALLOWED_HOME_USERS:
                    bad.append(f'{rel}:{idx}: workstation-home-uri')
            for m in ABS_HOME_RE.finditer(line):
                if m.group(1) not in ALLOWED_HOME_USERS:
                    bad.append(f'{rel}:{idx}: workstation-home-path')
    return bad


def git(root, *args):
    return subprocess.run(['git', '-C', str(root), *args], check=True, capture_output=True,
                          text=True).stdout.split('\0')


def files(staged, root=ROOT):
    # A check sandbox gets the commit as git archive writes it, with no .git (design 16.2, Crew's
    # project's checks): every file in it is one the commit tracks, so every one is read. A .git
    # that git cannot read fails the check, and is never taken for such a tree.
    if not (root / '.git').exists():
        if staged:
            sys.exit('check-clean: --staged needs a git checkout; this tree has no .git')
        return sorted(str(p.relative_to(root)) for p in root.rglob('*') if p.is_file())
    if staged:
        names = git(root, 'diff', '--cached', '--name-only', '--diff-filter=ACMR', '-z')
        return sorted({n for n in names if n})
    names = git(root, 'ls-files', '-z', '--cached', '--others', '--exclude-standard')
    return sorted({n for n in names if n and (root / n).is_file()})


def main(argv):
    staged = argv == ['--staged']
    names = files(staged)
    bad = [n for n in names if SECRET_NAMES.search(n) and n not in ALLOWED]
    for n in bad:
        print(f'check-clean: {n}: the name of a secret file; it never belongs in the repository')
    programs = [n for n in names if is_program_bytes(read_entry_bytes(n, ROOT, staged=staged))]
    for n in programs:
        print(f'check-clean: {n}: a built program; it is built in an image, never committed')
    hygiene = check_public_hygiene(names, ROOT, staged=staged)
    for h in hygiene:
        print(f'check-clean: {h}: non-example email domain or workstation home path')
    bad += programs + hygiene
    env = dict(os.environ)
    note = ''
    with tempfile.NamedTemporaryFile('w', suffix='.txt') as empty:
        if VALUES.is_file():
            env['SCRUB_VALUES'] = str(VALUES)
        else:
            env['SCRUB_VALUES'] = empty.name
            note = ' (no values file: token shapes only)'
        cmd = [sys.executable, '-I', str(SCRUB), 'check']
        cmd += ['--staged'] if staged else [str(ROOT / n) for n in names]
        if not names:
            print('check-clean: ok, nothing to check')
            return 1 if bad else 0
        r = subprocess.run(cmd, cwd=ROOT, env=env)
    if bad or r.returncode:
        print('check-clean: FAIL' + note)
        return 1
    print(f'check-clean: ok, {len(names)} file(s){note}')
    return 0


if __name__ == '__main__':
    sys.exit(main(sys.argv[1:]))
