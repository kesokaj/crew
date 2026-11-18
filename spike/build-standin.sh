#!/usr/bin/env bash
# Builds the stand-in for styrman-crew's runtime image, which the spike starts
# its containers from (spike items 5, 16, 74).
#
# styrman-crew was uninstalled from dave on 2026-10-12, before M0 began, and
# its image went with it. The operator approved building it again from the
# reference commit. The image is styrman-crew's image/ at 5c9e6fb, unchanged
# except for one step: styrman-crew downloads the sh-agent release with a
# GitHub token. Here sh-agent is built from styrman-harness at 43faca0
# (v2.7.0), with the image's own Go, so the build needs no token.
#
# The build context is made with `git archive`, so the two read-only
# repositories are never touched. It lives in spike/raw/, which git ignores.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
crew_ref=5c9e6fb
harness_ref=43faca0
harness_version=v2.7.0
tag="crew-spike-runtime:${crew_ref}"
ctx="${here}/raw/standin-build"

if [ ! -d "${here}/../../styrman-crew/.git" ] || [ ! -d "${here}/../../styrman-harness/.git" ]; then
    echo "build-standin.sh: historical M0 helper requiring private sibling checkouts ../styrman-crew and ../styrman-harness (not shipped in public Crew)." >&2
    exit 1
fi

rm -rf "${ctx}"
mkdir -p "${ctx}"
git -C "${here}/../../styrman-crew" archive "${crew_ref}" image | tar -x -C "${ctx}"
git -C "${here}/../../styrman-harness" archive --prefix=image/harness-src/ "${harness_ref}" | tar -x -C "${ctx}"

# Swap the token download for a build from source. The block starts at the
# ARG HARNESS_VERSION line and ends at the line that removes its temp folder.
python3 -I - "${ctx}/image/Dockerfile" "${harness_version}" <<'EOF'
import re, sys
path, version = sys.argv[1], sys.argv[2]
s = open(path).read()
block = re.compile(r'^ARG HARNESS_VERSION=.*?^    cd /; rm -r "\$d"\n', re.S | re.M)
new = (
    "COPY harness-src/ /tmp/harness-src/\n"
    "RUN set -eux; mkdir -p /styrman/bin; cd /tmp/harness-src; \\\n"
    f'    CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version={version}" -o /styrman/bin/sh-agent ./cmd/sh-agent; \\\n'
    f'    test "$(/styrman/bin/sh-agent version)" = "sh-agent {version}"; \\\n'
    "    cd /; rm -rf /tmp/harness-src /root/go /root/.cache/go-build\n"
)
s, n = block.subn(new, s)
if n != 1 or "--mount=type=secret" in s:
    sys.exit(f"build-standin: the harness block was found {n} times; the Dockerfile is not what this script expects")
open(path, "w").write(s)
EOF

sudo docker build --label crew-spike=1 --build-arg STYRMAN_VERSION="spike-${crew_ref}" \
    -t "${tag}" "${ctx}/image"
sudo docker image inspect "${tag}" --format 'built {{.Id}} {{.Size}} bytes'
