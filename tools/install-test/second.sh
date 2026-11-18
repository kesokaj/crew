#!/usr/bin/env bash
# The install test's phase second, its part on this machine (design 16.12, 22; M17 plan D3, D49): two
# releases of this commit built here with crew-ctl build --out, which this machine's updater does not
# see, their images pushed from this machine's Docker to a throwaway registry, and their cards, which
# the virtual machine gets as another machine gets them. The registry is two containers on one volume:
# one on 127.0.0.1, plain, which this machine's Docker pushes to, and one on the install test's own
# bridge, with TLS from a CA this script makes and a made-up login, which the virtual machine pulls
# from. A third card names a release whose image the registry swapped.
#   second.sh up | down      run by make install-test PHASE=second; IMG is the install test's image
set -euo pipefail
cd "$(dirname "$0")/../.."
DOCKER="${DOCKER:-sudo docker}"
OUT=.install-test/second
V1=0.99.1 V2=0.99.2 V3=0.99.3
PUSH=127.0.0.1:5998
NET=crew-install-test
VOL=crew-install-test-registry
USER_NAME=second
TOKEN=made-up-second-token
log() { echo "[second $(date -u +%T)] $*"; }
lock() { sed -n "s/^$1=//p" versions.lock; }

# card VERSION FROM: the card of the release built in FROM, with a gate.json of this test's own, bound
# to its release.json: no gate ran on these releases, and another machine installs none without one.
card() {
    local v="$1" from="$2" f
    rm -rf -- "${OUT}/cards/${v}"
    mkdir -p "${OUT}/cards/${v}"
    for f in release.json notes.md deploy.tar.gz browser.tar; do
        [ -f "${from}/${f}" ] && cp -- "${from}/${f}" "${OUT}/cards/${v}/${f}"
    done
    python3 -I - "${OUT}/cards/${v}" "${v}" <<'PY'
import datetime, hashlib, json, os, sys
d, v = sys.argv[1], sys.argv[2]
p = os.path.join(d, "release.json")
m = json.load(open(p))
if m["version"] != v:
    # The swapped release: the release of V2 under another version, its images by the same digests.
    old = m["version"]
    m["version"] = v
    for img in m["images"].values():
        if img["tag"].endswith(":" + old):
            img["tag"] = img["tag"][: -len(old)] + v
    open(p, "w").write(json.dumps(m, indent=2) + "\n")
body = open(p, "rb").read()
gate = {"version": v, "manifestSha256": hashlib.sha256(body).hexdigest(), "passed": True,
        "at": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "steps": [{"name": "the install test's own: no gate ran on this release", "ok": True}],
        "installTest": "skipped", "installTestNote": "a release of the install test's phase second"}
open(os.path.join(d, "gate.json"), "w").write(json.dumps(gate, indent=2) + "\n")
PY
}

up() {
    : "${IMG:?IMG names the image of the install test}"
    mkdir -p "${OUT}/built" "${OUT}/certs" "${OUT}/auth"
    # crew-ctl build --out writes only into a folder no one else may change: not the group either.
    chmod 0755 "${OUT}/built"
    for v in "${V1}" "${V2}"; do
        if [ -f "${OUT}/built/${v}/release.json" ]; then log "${v} is built already"; continue; fi
        log "building ${v} on this machine"
        sudo -n deploy/crew-ctl build --version "${v}" --out "${OUT}/built"
    done

    log "the registry's CA, its certificate for registry.install.test, and its made-up login"
    ${DOCKER} run --rm --user "$(id -u):$(id -g)" -v "${PWD}/${OUT}:/w" --entrypoint sh "${IMG}" -c '
        set -e; cd /w/certs
        openssl req -x509 -newkey rsa:3072 -nodes -days 2 -subj "/CN=crew install test CA" -keyout ca.key -out ca.crt 2>/dev/null
        openssl req -newkey rsa:3072 -nodes -subj "/CN=registry.install.test" -keyout registry.key -out registry.csr 2>/dev/null
        printf "subjectAltName=DNS:registry.install.test\n" > san.ext
        openssl x509 -req -in registry.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 2 -extfile san.ext -out registry.crt 2>/dev/null
        rm -f ca.key registry.csr san.ext ca.srl
        htpasswd -Bbn '"${USER_NAME} ${TOKEN}"' > /w/auth/htpasswd'
    # As the README has another machine write it: 0600, which crew-ctl requires.
    install -m 0600 /dev/null "${OUT}/token"
    printf '%s:%s\n' "${USER_NAME}" "${TOKEN}" > "${OUT}/token"
    cp "${OUT}/certs/ca.crt" "${OUT}/ca.crt"

    local reg auth
    reg="$(lock INSTALL_TEST_REGISTRY_IMAGE)"
    auth=(-e REGISTRY_AUTH=htpasswd -e REGISTRY_AUTH_HTPASSWD_REALM=crew-install-test -e REGISTRY_AUTH_HTPASSWD_PATH=/auth/htpasswd
          -v "${PWD}/${OUT}/auth:/auth:ro" -v "${VOL}:/var/lib/registry")
    log "the registry: ${PUSH} for this machine's push, registry.install.test on ${NET} for the virtual machine"
    ${DOCKER} run -d --name crew-install-test-registry-push -p "${PUSH}:5000" "${auth[@]}" "${reg}" >/dev/null
    ${DOCKER} run -d --name crew-install-test-registry --network "${NET}" --network-alias registry.install.test "${auth[@]}" \
        -v "${PWD}/${OUT}/certs:/certs:ro" -e REGISTRY_HTTP_ADDR=0.0.0.0:443 \
        -e REGISTRY_HTTP_TLS_CERTIFICATE=/certs/registry.crt -e REGISTRY_HTTP_TLS_KEY=/certs/registry.key "${reg}" >/dev/null
    ${DOCKER} inspect -f "{{(index .NetworkSettings.Networks \"${NET}\").IPAddress}}" crew-install-test-registry > "${OUT}/registry-ip"
    for _ in $(seq 1 30); do curl -s -o /dev/null "http://${PUSH}/v2/" && break; sleep 1; done

    log "pushing Crew's images of ${V1} and ${V2} from this machine's Docker; PostgreSQL's stays Docker Hub's"
    local cfg n v
    mkdir -p "${PWD}/.agents/scratch"
    cfg="$(mktemp -d -p "${PWD}/.agents/scratch" second-login.XXXXXX)"
    printf '{"auths": {"%s": {}}}\n' "${PUSH}" > "${cfg}/config.json"
    printf '%s\n' "${TOKEN}" | ${DOCKER} --config "${cfg}" login "${PUSH}" -u "${USER_NAME}" --password-stdin >/dev/null
    for v in "${V1}" "${V2}"; do
        for n in crew-server crew-caddy crew-runtime crew-updater; do
            ${DOCKER} tag "${n}:${v}" "${PUSH}/owner/${n}:${v}"
            ${DOCKER} --config "${cfg}" push -q "${PUSH}/owner/${n}:${v}" >/dev/null
            ${DOCKER} image rm "${PUSH}/owner/${n}:${v}" >/dev/null
        done
    done
    # The swapped release: under V3, the registry holds V1's ingress where release.json names V2's.
    for n in crew-server crew-caddy crew-runtime crew-updater; do
        local from="${n}:${V2}"
        [ "${n}" = crew-caddy ] && from="crew-caddy:${V1}"
        ${DOCKER} tag "${from}" "${PUSH}/owner/${n}:${V3}"
        ${DOCKER} --config "${cfg}" push -q "${PUSH}/owner/${n}:${V3}" >/dev/null
        ${DOCKER} image rm "${PUSH}/owner/${n}:${V3}" >/dev/null
    done
    rm -rf -- "${cfg}"

    log "the cards"
    card "${V1}" "${OUT}/built/${V1}"
    card "${V2}" "${OUT}/built/${V2}"
    card "${V3}" "${OUT}/built/${V2}"
    log "ready: $(cat "${OUT}/registry-ip") registry.install.test"
}

down() {
    log "removing the registry, its volume, and the images of ${V1} and ${V2} on this machine"
    ${DOCKER} rm -f crew-install-test-registry crew-install-test-registry-push >/dev/null 2>&1 || true
    ${DOCKER} volume rm "${VOL}" >/dev/null 2>&1 || true
    local n v
    for v in "${V1}" "${V2}"; do
        for n in crew-server crew-caddy crew-runtime crew-updater; do
            ${DOCKER} image rm "${n}:${v}" >/dev/null 2>&1 || true
        done
    done
    # The built folders are root's; the cards, the CA, and the login are the test's.
    sudo -n rm -rf -- "${OUT:?}/built"
    rm -rf -- "${OUT:?}/cards" "${OUT:?}/certs" "${OUT:?}/auth" "${OUT:?}/token" "${OUT:?}/ca.crt" "${OUT:?}/registry-ip"
}

case "${1:-}" in
    up) up ;;
    down) down ;;
    *) echo "second.sh up | down" >&2; exit 2 ;;
esac
