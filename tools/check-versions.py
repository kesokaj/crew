#!/usr/bin/env python3
"""Checks versions.lock, and the few places that must name a version themselves (design 16.12).

  python3 tools/check-versions.py      fails when versions.lock is malformed, names a moving tag,
                                       or a place on the fixed list names another version

Every version Crew is built from is in versions.lock. A tool insists on a few other places:
they are on the list PLACES below, and each must agree with versions.lock.
It needs only Python 3 and its standard library.
"""
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
LINE = re.compile(r'^([A-Z][A-Z0-9_]*)=(.*)$')
# An image is named by its tag and its hash, so that neither can move alone.
IMAGE = re.compile(r'^[a-z0-9./_-]+:[A-Za-z0-9._-]+@sha256:[0-9a-f]{64}$')
SHA256 = re.compile(r'^[0-9a-f]{64}$')
SHA512 = re.compile(r'^[0-9a-f]{128}$')
# agy is pinned by the vendor's tarball, whose address names its version (design 12.8, since 0.56).
AGY_URL = re.compile(r'^https://storage\.googleapis\.com/antigravity-public/antigravity-cli/'
                     r'([0-9]+\.[0-9]+\.[0-9]+)-[0-9]+/linux-x64/cli_linux_x64\.tar\.gz$')
MOVING = {'latest', 'stable', 'edge', 'main', 'master', 'nightly'}

# The fixed list: (file, pattern whose first group is the version, key in versions.lock).
PLACES = [
    ('go.mod', r'^go (\S+)$', 'GO_VERSION'),
    ('ingress/go.mod', r'^go (\S+)$', 'GO_VERSION'),
    ('web/.node-version', r'^(\S+)$', 'WEB_NODE_VERSION'),
]
# Every ARG with a default in these files whose name is a key of versions.lock must have its value;
# and each file must name the keys listed for it, so that a pin cannot be dropped unseen.
DOCKERFILES = {
    'deploy/Dockerfile.server': ['GO_IMAGE', 'SERVER_BASE_IMAGE', 'WEB_NODE_IMAGE', 'POSTGRES_IMAGE'],
    'ingress/Dockerfile': ['GO_IMAGE', 'CADDY_BUILDER_IMAGE', 'CADDY_IMAGE'],
    'image/Dockerfile': ['GO_IMAGE', 'RUNTIME_BASE_IMAGE', 'POSTGRES_IMAGE', 'GOVULNCHECK_VERSION',
                         'RUNTIME_GO_VERSION', 'RUNTIME_GO_SHA256',
                         'RUNTIME_NODE_VERSION', 'RUNTIME_NODE_SHA256', 'TOFU_VERSION', 'TOFU_SHA256',
                         'YQ_VERSION', 'YQ_SHA256', 'HADOLINT_VERSION', 'HADOLINT_SHA256',
                         'CHROME_DEVTOOLS_MCP_VERSION', 'CHROME_DEVTOOLS_MCP_INTEGRITY', 'RUNTIME_DOCKER_CE_VERSION', 'RUNTIME_CONTAINERD_VERSION',
                         'RUNTIME_BUILDX_VERSION', 'RUNTIME_COMPOSE_VERSION', 'RUNTIME_GH_VERSION',
                         'RUNTIME_GCLOUD_VERSION', 'RUNTIME_KUBECTL_VERSION', 'AGY_VERSION', 'AGY_SHA512'],
    'tools/check-image/Dockerfile': ['GOVULNCHECK_VERSION'],
}
ARG = re.compile(r'^ARG ([A-Z][A-Z0-9_]*)=(\S+)$', re.M)


def read_lock(path):
    values, errors = {}, []
    for n, raw in enumerate(path.read_text().splitlines(), 1):
        line = raw.strip()
        if not line or line.startswith('#'):
            continue
        m = LINE.match(line)
        if not m:
            errors.append(f'versions.lock:{n}: not a KEY=VALUE line')
            continue
        key, value = m.groups()
        if key in values:
            errors.append(f'versions.lock:{n}: {key} is given twice')
        if not value:
            errors.append(f'versions.lock:{n}: {key} has no value')
        if key.endswith('_IMAGE') and value and not IMAGE.match(value):
            errors.append(f'versions.lock:{n}: {key} is not an image with a tag and a sha256 hash')
        if key.endswith('_SHA256') and value and not SHA256.match(value):
            errors.append(f'versions.lock:{n}: {key} is not a sha256 checksum')
        if key.endswith('_SHA512') and value and not SHA512.match(value):
            errors.append(f'versions.lock:{n}: {key} is not a sha512 checksum')
        tag = value.split('@')[0].rsplit(':', 1)[-1] if key.endswith('_IMAGE') else value
        if tag in MOVING:
            errors.append(f'versions.lock:{n}: {key} names the moving tag {tag}')
        values[key] = value
    if 'AGY_URL' in values:
        m = AGY_URL.match(values['AGY_URL'])
        if not m:
            errors.append("versions.lock: AGY_URL is not the address of the vendor's tarball")
        elif m.group(1) != values.get('AGY_VERSION'):
            errors.append(f"versions.lock: AGY_URL names agy {m.group(1)}, AGY_VERSION is {values.get('AGY_VERSION')}")
    return values, errors


def check_places(root, values):
    errors = []
    for rel, pattern, key in PLACES:
        path = root / rel
        if not path.exists():
            # A place exists from the milestone that adds it. Until then there is nothing to differ.
            continue
        found = re.findall(pattern, path.read_text(), re.M)
        if not found:
            errors.append(f'{rel}: names no version for {key}')
            continue
        if key not in values:
            errors.append(f'{rel}: names {found[0]}, and versions.lock has no {key}')
            continue
        for v in found:
            if v != values[key]:
                errors.append(f'{rel}: names {v} for {key}, versions.lock has {values[key]}')
    for rel, required in DOCKERFILES.items():
        path = root / rel
        if not path.exists():
            continue
        found = ARG.findall(path.read_text())
        named = {k for k, _ in found}
        for k, v in found:
            if k in values and v != values[k]:
                errors.append(f'{rel}: ARG {k}={v}, versions.lock has {values[k]}')
        for k in required:
            if k not in named:
                errors.append(f'{rel}: names no ARG {k} with a default')
    return errors


def main(root=ROOT):
    values, errors = read_lock(root / 'versions.lock')
    errors += check_places(root, values)
    for e in errors:
        print('check-versions: ' + e)
    if errors:
        print(f'check-versions: FAIL, {len(errors)} problem(s)')
        return 1
    print(f'check-versions: ok, {len(values)} entries, {len(PLACES) + len(DOCKERFILES)} places on the list')
    return 0


if __name__ == '__main__':
    sys.exit(main(pathlib.Path(sys.argv[1]) if len(sys.argv) > 1 else ROOT))
