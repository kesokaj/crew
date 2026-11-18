#!/usr/bin/env bash
# release.sh: builds a release of Crew from a tree given as a folder (design 16.2 step 4, 16.10).
#
#   release.sh build --tree DIR --version X.Y.Z --out DIR --agy DIR
#       The images, each saved as a file under OUT/images/, OUT/pins.json with what the runtime
#       image got of agy, and OUT/browser.tar, the gate's browser tests with their packages. Docker is the one of the machine it runs on: in a release Crew builds,
#       the check sandbox's own daemon, in Sources mode, with no socket of the machine. It is
#       build-runtime and then build-rest. --agy is a folder holding agy's tarball, which the server,
#       or crew-ctl on the machine, downloaded and checked against the tree's versions.lock: the runtime
#       image gets it as its build context agy, and this script downloads none (12.8, since 0.57).
#   release.sh build-runtime ... and build-rest ...
#       The two steps of a build. A release Crew builds runs them as two commands, and the server
#       copies the runtime image out between them: the first runs only image/Dockerfile, so an escape
#       from the candidate's other Dockerfiles cannot change the image the gate's live steps give
#       the operator's login to.
#   release.sh write --tree DIR --tree-tar FILE --version X.Y.Z --out DIR --commit SHA --facts FILE
#       --machine-setup N
#       release.json and the deploy archive, written by the release's own server program
#       from the tree: crew-ctl build does this on a checkout. A release Crew builds is written by
#       the running updater instead, from what the running server read of the commit, since the
#       candidate's program is code agents wrote.
#
# Crew runs the copy of this script that is in the running release's deploy archive, never the
# candidate's: the candidate's Dockerfiles run, and only inside the sandbox. A change to the steps
# here takes two releases: the one that carries it, and the first one it builds.
set -euo pipefail
shopt -s inherit_errexit

log() { echo "==> $*"; }
die() { echo "release.sh: ERROR $*" >&2; exit 1; }

TREE="" VERSION="" OUT="" TREE_TAR="" COMMIT="" FACTS="" SETUP="" AGY=""
verb="${1:-}"; shift || true
while [ $# -gt 0 ]; do
    case "$1" in
        --tree) TREE="$2"; shift 2 ;;
        --version) VERSION="$2"; shift 2 ;;
        --out) OUT="$2"; shift 2 ;;
        --tree-tar) TREE_TAR="$2"; shift 2 ;;
        --commit) COMMIT="$2"; shift 2 ;;
        --facts) FACTS="$2"; shift 2 ;;
        --machine-setup) SETUP="$2"; shift 2 ;;
        --agy) AGY="$2"; shift 2 ;;
        *) die "unknown argument $1" ;;
    esac
done
[[ "${VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-dev\.[0-9a-f]{7,40})?$ ]] || die "--version X.Y.Z"
[ -f "${TREE}/versions.lock" ] || die "--tree is the folder of a commit of Crew"
[ -n "${OUT}" ] || die "--out is the folder to write"
lock_value() { sed -n "s/^$1=//p" "${TREE}/versions.lock" | tail -1; }

# The proxy of a Sources sandbox reaches the builds too: Docker gives it to a build, and to the
# container that installs the browser tests' packages, only by name. So does the server's module
# proxy, GOPROXY, to the builds, whose stages that run Go declare it (13.6, since 0.61).
proxy_args=() proxy_env=()
for v in HTTPS_PROXY https_proxy NO_PROXY no_proxy; do
    [ -n "${!v:-}" ] && proxy_args+=(--build-arg "${v}") && proxy_env+=(-e "${v}")
done
[ -n "${GOPROXY:-}" ] && proxy_args+=(--build-arg GOPROXY)

build_one() { # build_one TAG DOCKERFILE CONTEXT [TARGET [ARGS...]]: TARGET "" is none
    local tag="$1" file="$2" ctx="$3" target=()
    [ -z "${4:-}" ] || target=(--target "$4")
    shift 3; shift || true
    log "building ${tag}"
    docker build --progress=plain -f "${file}" "${target[@]}" "${proxy_args[@]}" "$@" \
        --build-arg "CREW_VERSION=${VERSION}" -t "${tag}" "${ctx}" || die "the build of ${tag} failed"
}

# postgres_tag: PostgreSQL's image, loaded from /crew/cache/postgres.tar when pre-baked in the runtime
# image or pulled by the digest versions.lock names, and tagged with its tag alone: a pull by digest
# gives no tag on a fresh machine, and the release saves it by its tag.
postgres_tag() {
    local full ref tag
    full="$(lock_value POSTGRES_IMAGE)"; tag="${full%@*}"; ref="${tag%%:*}@${full#*@}"
    if [ -f /crew/cache/postgres.tar ]; then
        docker load -q -i /crew/cache/postgres.tar >&2 || true
        if docker image inspect "${tag}" >/dev/null 2>&1; then
            echo "${tag}"
            return 0
        fi
    fi
    docker pull --quiet "${full}" >&2 || return 1
    docker tag "${ref}" "${tag}" || return 1
    echo "${tag}"
}

# build_runtime: the runtime image first, saved, and what it got of agy (16.12), before any other
# Dockerfile of the tree runs. When BASE_RUNTIME and BASE_VERSION are given (SameRuntime holds), it
# layers the new version stamp on top of the running release's runtime image.
cmd_build_runtime() {
    [ -n "${AGY}" ] && [ -f "${AGY}/cli_linux_x64.tar.gz" ] \
        || die "build-runtime needs --agy DIR with cli_linux_x64.tar.gz: agy's tarball, which the build is given and does not download"
    install -d -m 0755 "${OUT}" "${OUT}/images"
    if [ -n "${BASE_RUNTIME:-}" ] && [ -f "${BASE_RUNTIME}" ] && [ -n "${BASE_VERSION:-}" ]; then
        log "loading base runtime image ${BASE_VERSION} for fast SameRuntime layer"
        docker load -q -i "${BASE_RUNTIME}" >&2 || die "loading ${BASE_RUNTIME} failed"
        rm -f -- "${BASE_RUNTIME}"
        local df="${OUT}/.Dockerfile.fast-runtime"
        cat > "${df}" <<EOF
FROM crew-runtime:${BASE_VERSION}
USER root
RUN printf '%s\n' "${VERSION}" > /crew/VERSION && jq --arg v "${VERSION}" '.crew = \$v' /crew/versions.json > /crew/versions.json.new && mv /crew/versions.json.new /crew/versions.json && chmod 0644 /crew/VERSION /crew/versions.json
LABEL crew.version="${VERSION}"
USER agent
EOF
        docker build --progress=plain -f "${df}" -t "crew-runtime:${VERSION}" "${OUT}" || die "fast build of crew-runtime:${VERSION} failed"
        rm -f -- "${df}"
    else
        build_one "crew-runtime:${VERSION}" "${TREE}/image/Dockerfile" "${TREE}" "" --build-context "agy=${AGY}"
    fi
    log "saving crew-runtime:${VERSION}"
    docker save --output "${OUT}/images/crew-runtime.tar" "crew-runtime:${VERSION}" || die "saving crew-runtime:${VERSION} failed"
    docker run --rm --entrypoint cat "crew-runtime:${VERSION}" /crew/versions.json > "${OUT}/pins.json" 2>/dev/null \
        || echo '{}' > "${OUT}/pins.json"
    log "built the runtime image of ${VERSION} into ${OUT}"
}

cmd_build_rest() {
    install -d -m 0755 "${OUT}" "${OUT}/images"
    local pg_file="${OUT}/.pg_tag" pids=() pid
    (
        build_one "crew-server:${VERSION}" "${TREE}/deploy/Dockerfile.server" "${TREE}"
        build_one "crew-updater:${VERSION}" "${TREE}/deploy/Dockerfile.server" "${TREE}" updater
    ) & pids+=($!)
    build_one "crew-caddy:${VERSION}" "${TREE}/ingress/Dockerfile" "${TREE}/ingress" & pids+=($!)
    ( postgres_tag > "${pg_file}" ) & pids+=($!)
    pack_browser & pids+=($!)
    for pid in "${pids[@]}"; do
        wait "${pid}" || die "a parallel build step in build-rest failed"
    done
    local pg n tag save_pids=()
    pg="$(cat "${pg_file}")" && rm -f -- "${pg_file}"
    [ -n "${pg}" ] || die "PostgreSQL's image could not be loaded or pulled"
    for n in crew-server crew-caddy crew-updater postgres; do
        if [ "${n}" = postgres ]; then tag="${pg}"; else tag="${n}:${VERSION}"; fi
        log "saving ${tag}"
        docker save --output "${OUT}/images/${n}.tar" "${tag}" & save_pids+=($!)
    done
    for pid in "${save_pids[@]}"; do
        wait "${pid}" || die "saving an image in build-rest failed"
    done
    log "built ${VERSION} into ${OUT}"
}

# pack_browser: the gate's browser tests with their packages, as OUT/browser.tar. The packages are
# installed here, from the lock file with no install script: in a release Crew builds, inside the
# build's sandbox and through its proxy, so the updater, which runs the tests against the release in
# check mode, fetches nothing (16.2 step 5).
pack_browser() {
    local src="${TREE}/tools/e2e/browser" work="${OUT}/browser" img
    img="$(lock_value WEB_NODE_IMAGE)"
    [ -n "${img}" ] || die "versions.lock names no WEB_NODE_IMAGE"
    rm -rf -- "${work}" && install -d -m 0755 "${work}" "${work}/gate" || die "the browser tests' folder"
    cp -- "${src}/package.json" "${src}/package-lock.json" "${src}/playwright.gate.config.ts" "${work}/" \
        && cp -r -- "${src}/gate/." "${work}/gate/" || die "the tree has no browser tests of the gate"
    log "installing the browser tests' packages"
    docker run --rm --user "$(id -u):$(id -g)" "${proxy_env[@]}" -v "${work}:/work" -w /work -e HOME=/tmp \
        -e npm_config_cache=/tmp/.npm "${img}" npm ci --ignore-scripts --no-audit --no-fund >/dev/null \
        || die "the browser tests' packages were not installed"
    tar -C "${work}" --sort=name --owner=0 --group=0 --numeric-owner -cf "${OUT}/browser.tar" . || die "packing the browser tests"
    rm -rf -- "${work}"
}

cmd_write() {
    [ -f "${TREE_TAR}" ] || die "--tree-tar is the tree as git archive wrote it"
    [[ "${COMMIT}" =~ ^[0-9a-f]{40,64}$ ]] || die "--commit is the commit's hash"
    [ -f "${FACTS}" ] || die "--facts is the file of git ls-tree and the agents' lines"
    [[ "${SETUP}" =~ ^[0-9]+$ ]] || die "--machine-setup N"
    local agy pg
    agy="$(python3 -I -c 'import json,sys; print(json.load(open(sys.argv[1])).get("agy",""))' "${OUT}/pins.json" 2>/dev/null || true)"
    pg="$(lock_value POSTGRES_IMAGE)"; pg="${pg%@*}"
    local imgs=() n tag id
    for n in crew-server crew-caddy crew-runtime crew-updater postgres; do
        if [ "${n}" = postgres ]; then tag="${pg}"; else tag="${n}:${VERSION}"; fi
        id="$(docker image inspect --format '{{.Id}}' "${tag}")"
        imgs+=(--image "${n}=${tag}@${id}")
    done
    docker run --rm -i --user 0 --network none -v "${OUT}:/out/${VERSION}" -v "${TREE_TAR}:/tree.tar:ro" \
        --entrypoint crew "crew-server:${VERSION}" release write --dir "/out/${VERSION}" --commit "${COMMIT}" \
        --tree /tree.tar --machine-setup "${SETUP}" --pin "agy=${agy:-unknown}" --pin "postgres=${pg##*:}" "${imgs[@]}" \
        < "${FACTS}" || die "release.json of ${VERSION} was not written"
    rm -f "${OUT}/pins.json"
}

case "${verb}" in
    build) cmd_build_runtime; cmd_build_rest ;;
    build-runtime) cmd_build_runtime ;;
    build-rest) cmd_build_rest ;;
    write) cmd_write ;;
    *) die "release.sh build|build-runtime|build-rest|write ..." ;;
esac
