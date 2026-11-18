#!/usr/bin/env bash
# Starts, enters, and removes the spike's own sandboxes: containers of the
# stand-in runtime image (build-standin.sh) under the Kata runtime "mvm",
# started the way styrman-crew's orchestrator starts a microVM at 5c9e6fb
# (internal/orchestrator/orchestrator.go, ContainerCreate).
#
#   sandbox.sh start NAME [small|large] [minimal]   start crew-spike-NAME and wait for ready
#   sandbox.sh exec NAME CMD...                     run CMD as the user agent, in the home
#   sandbox.sh rm NAME [keep-home]                  remove the container, its network, its home
#   sandbox.sh ls                                   the spike's containers and networks
#
# The rules of the spike (spike/README.md) that this script keeps:
# - every container and network is named crew-spike-NAME and carries the label
#   crew-spike=1, and none of styrman-crew's labels;
# - agy's paid credits setting is written as off in the home before the
#   container starts, so before any run (useG1Credits, design 12.2);
# - neither CLI updates itself: AGY_CLI_DISABLE_AUTO_UPDATE and
#   DISABLE_AUTOUPDATER are set, unless the caller sets them to test it (items 7, 70);
# - the home is a folder under spike/raw/homes/, which git ignores, and rm
#   removes it, with any login in it, unless keep-home is given.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
image="${CREW_SPIKE_IMAGE:-crew-spike-runtime:5c9e6fb}"
homes="${here}/raw/homes"

die() { echo "sandbox: $*" >&2; exit 1; }
name_ok() { [[ "$1" =~ ^[a-z0-9][a-z0-9-]{0,40}$ ]] || die "bad name: $1"; }

start() {
    local name="$1" size="${2:-small}" minimal="${3:-}"
    name_ok "${name}"
    local c="crew-spike-${name}" home="${homes}/${name}"
    local -a res
    # Kata adds these to its own base of 1 vCPU and 2 GB (design 14.1).
    case "${size}" in
        small) res=(--cpus 1) ;;
        large) res=(--cpus 3 --memory 4g) ;;
        *) die "size is small or large" ;;
    esac
    mkdir -p "${home}/.gemini/antigravity-cli"
    chmod 0700 "${homes}" "${home}"
    local s="${home}/.gemini/antigravity-cli/settings.json"
    python3 -I - "${s}" <<'EOF'
import json, os, sys
p = sys.argv[1]
d = json.load(open(p)) if os.path.exists(p) else {}
d["useG1Credits"] = False
json.dump(d, open(p, "w"), indent=2)
EOF
    sudo docker network create --label crew-spike=1 "${c}" >/dev/null
    sudo docker run -d --name "${c}" --hostname "${name}" --label crew-spike=1 \
        --runtime mvm --network "${c}" "${res[@]}" \
        --cap-add SYS_ADMIN --cap-add NET_ADMIN --cap-add NET_RAW \
        --dns 8.8.8.8 --dns 1.1.1.1 --sysctl net.ipv4.ip_forward=1 \
        --security-opt apparmor=unconfined --restart no \
        -e STYRMAN_USER=agent -e STYRMAN_UID="$(id -u)" -e STYRMAN_GID="$(id -g)" \
        -e STYRMAN_PROJECT=spike ${minimal:+-e STYRMAN_MINIMAL=1} \
        -e AGY_CLI_DISABLE_AUTO_UPDATE="${AGY_CLI_DISABLE_AUTO_UPDATE-true}" \
        -e DISABLE_AUTOUPDATER="${DISABLE_AUTOUPDATER-1}" \
        -v "${home}:/home/agent" "${image}" >/dev/null
    local t0=${SECONDS}
    until sudo docker exec "${c}" test -f /run/styrman/ready 2>/dev/null; do
        [ $((SECONDS - t0)) -lt 180 ] || die "${c} not ready after 180 s"
        sudo docker inspect -f '{{.State.Running}}' "${c}" | grep -q true || die "${c} exited"
        sleep 1
    done
    echo "${c} ready after $((SECONDS - t0)) s"
}

run_exec() {
    local name="$1"; shift
    name_ok "${name}"
    # Variables are passed by name, so a token's value is never on a command line.
    local -a pass=()
    local v
    for v in CLAUDE_CODE_OAUTH_TOKEN ANTHROPIC_API_KEY HTTPS_PROXY HTTP_PROXY NO_PROXY; do
        [ -n "${!v+x}" ] && pass+=(-e "${v}")
    done
    sudo --preserve-env=CLAUDE_CODE_OAUTH_TOKEN,ANTHROPIC_API_KEY,HTTPS_PROXY,HTTP_PROXY,NO_PROXY \
        docker exec -i -u agent -w /home/agent -e HOME=/home/agent "${pass[@]}" "crew-spike-${name}" "$@"
}

remove() {
    local name="$1" keep="${2:-}"
    name_ok "${name}"
    sudo docker rm -f "crew-spike-${name}" >/dev/null 2>&1 || true
    sudo docker network rm "crew-spike-${name}" >/dev/null 2>&1 || true
    if [ -z "${keep}" ] && [ -d "${homes}/${name}" ]; then
        # The guest's root can leave files the operator's user cannot remove.
        sudo rm -rf --one-file-system "${homes:?}/${name}"
    fi
    echo "crew-spike-${name} removed${keep:+, home kept}"
}

case "${1:-}" in
    start) shift; start "$@" ;;
    exec) shift; run_exec "$@" ;;
    rm) shift; remove "$@" ;;
    ls) sudo docker ps -a --filter label=crew-spike=1 --format '{{.Names}}\t{{.Status}}'
        sudo docker network ls --filter label=crew-spike=1 --format '{{.Name}}' ;;
    *) sed -n '2,11p' "$0"; exit 2 ;;
esac
