#!/usr/bin/env bash
# An end-to-end test of a run's sandbox, on a machine with Crew installed: a microVM of Crew's runtime
# image, started the way the orchestrator starts one (design 13.3 to 13.5), working on the fixture
# project (15.1). As root on the machine. It removes what it made, however it ends.
#
#   tools/e2e/sandbox.sh            the sandbox, the harness, and the fixture's setup, tests, and image
#   AGENTS=1 tools/e2e/sandbox.sh   also one short, cheap headless turn of agy on the fixture:
#                                   it costs a little of the subscription (AGENTS.md, "Real runs")
#
# The login is copied by path; nothing prints it: ~/.gemini/antigravity-cli/antigravity-oauth-token
# of the user who runs sudo.
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 2
REPO="$(pwd)"
OWNER_HOME="$(getent passwd "${SUDO_USER:-$(id -un)}" | cut -d: -f6)"
VERSION="$(python3 -I -c 'import json; print(json.load(open("/etc/crew/install.json"))["version"])')"
IMAGE="crew-runtime:${VERSION}"
NAME=crew-e2e-sandbox
NET=crew-e2e-sandbox
PASS=0 FAIL=0
pass() { echo "PASS $*"; PASS=$((PASS + 1)); }
fail() { echo "FAIL $*"; FAIL=$((FAIL + 1)); }
check() { local n="$1"; shift; if "$@"; then pass "$n"; else fail "$n"; fi; }
as_agent() { docker exec -u agent -w /home/agent/fixture -e HOME=/home/agent "${NAME}" bash -lc "$*"; }
cleanup() { [ -n "${KEEP:-}" ] && return; docker rm -f "${NAME}" crew-e2e-sandbox-uid >/dev/null 2>&1; docker network rm "${NET}" >/dev/null 2>&1; }
trap cleanup EXIT
cleanup

echo "── the sandbox: ${IMAGE}, on a sandbox bridge, with the four capabilities of 13.5"
docker network create -o com.docker.network.bridge.name=sdb-e2esandbox "${NET}" >/dev/null
docker run -d --name "${NAME}" --runtime mvm --network "${NET}" --cpus 3 --memory 4g \
    --cap-add SYS_ADMIN --cap-add NET_ADMIN --cap-add NET_RAW --cap-add SYS_PTRACE --security-opt apparmor=unconfined \
    --label crew.sandbox=e2e -e CREW_PROJECT=e2e "${IMAGE}" >/dev/null
for _ in $(seq 1 90); do docker exec "${NAME}" test -f /run/crew/harness-done 2>/dev/null && break; sleep 1; done
check "the sandbox is ready" docker exec "${NAME}" test -f /run/crew/ready
check "its own kernel" test "$(docker exec "${NAME}" uname -r)" != "$(uname -r)"
check "the user is agent, uid 1000, not root" test "$(docker exec -u agent "${NAME}" id -u)" = 1000
check "agent may use sudo" docker exec -u agent "${NAME}" sudo -n true
check "the harness is in the home for agy" docker exec "${NAME}" test -d /home/agent/.gemini/config/skills/golang
check "agy does not update itself" docker exec "${NAME}" sh -c 'test "$AGY_CLI_DISABLE_AUTO_UPDATE" = true'
check "agy acp supports --process-per-session" sh -c "docker exec ${NAME} agy acp --help 2>&1 | grep -- --process-per-session >/dev/null"
check "no claude in the image" sh -c "! docker exec ${NAME} sh -c 'command -v claude'"
check "agent cannot replace agy" sh -c "! docker exec -u agent ${NAME} sh -c 'touch /opt/agy/x' 2>/dev/null"
check "no ANTHROPIC_API_KEY" sh -c "! docker exec ${NAME} sh -c 'env | grep ^ANTHROPIC_API_KEY= >/dev/null'"
check "IPv6 is off in the guest" test "$(docker exec "${NAME}" cat /proc/sys/net/ipv6/conf/all/disable_ipv6)" = 1
check "crew-run is in the image" test "$(docker exec "${NAME}" crew-run version)" = "${VERSION}"

echo "── the fixture project in the sandbox"
docker cp "${REPO}/testdata/fixture-project" "${NAME}:/home/agent/fixture" && docker exec "${NAME}" chown -R agent:agent /home/agent/fixture
as_agent 'git init -q && git add -A && git -c user.name=crew -c user.email=crew@example.com commit -qm fixture'
check "make setup" as_agent make setup
check "make test" as_agent make test
check "its own Docker builds the fixture's image" as_agent 'docker build -q -t fixture:e2e . >/dev/null'
as_agent 'docker run -d --rm --name fx -p 18080:8080 fixture:e2e >/dev/null; sleep 2'
check "the fixture serves from the sandbox's own Docker, as a user that is not root" test "$(as_agent 'curl -s localhost:18080/hello/Crew')" = "Hello, Crew!"
check "nothing the sandbox built is in the machine's Docker" sh -c '! docker image inspect fixture:e2e >/dev/null 2>&1'

echo "── a sandbox as the server starts one: agent has the uid of crew, and the home on the machine is shared"
UID_NAME=crew-e2e-sandbox-uid
CREW_UID_="$(id -u crew 2>/dev/null || echo 999)" CREW_GID_="$(id -g crew 2>/dev/null || echo 999)"
UHOME="$(mktemp -d)"; chown "${CREW_UID_}:${CREW_GID_}" "${UHOME}"; chmod 700 "${UHOME}"
docker run -d --name "${UID_NAME}" --runtime mvm --network "${NET}" -e CREW_PROJECT=setup -e CREW_MINIMAL=1 \
    -e CREW_USER=agent -e CREW_UID="${CREW_UID_}" -e CREW_GID="${CREW_GID_}" -v "${UHOME}:/home/agent" "${IMAGE}" >/dev/null
for _ in $(seq 1 60); do docker exec "${UID_NAME}" test -f /run/crew/ready 2>/dev/null && break; sleep 1; done
check "the sandbox is ready with the uid of crew" docker exec "${UID_NAME}" test -f /run/crew/ready
check "agent has the uid of crew" test "$(docker exec -u agent "${UID_NAME}" id -u)" = "${CREW_UID_}"
docker exec -u agent "${UID_NAME}" sh -c 'umask 077; echo x > /home/agent/made-in-the-sandbox' >/dev/null 2>&1
check "what agent writes in the home belongs to crew on the machine" test "$(stat -c %u "${UHOME}/made-in-the-sandbox" 2>/dev/null)" = "${CREW_UID_}"
docker rm -f "${UID_NAME}" >/dev/null 2>&1; rm -rf "${UHOME}"

if [ "${AGENTS:-}" = 1 ]; then
    # The outputs are kept for a look, in a folder git ignores. The CLIs print no secret (spike item 62).
    LOGS="${REPO}/.install-test/e2e-agents"; mkdir -p "${LOGS}"
    # The task: a failing test for a function that does not exist yet. One turn, the cheapest model.
    task='The test in bye_test.go fails. Add the function Bye to main.go so that it passes, and nothing else. Run make test to check.'
    write_task() {
        as_agent 'git checkout -q -- . && git clean -qfd && cat > bye_test.go <<EOF
package main

import "testing"

func TestBye(t *testing.T) {
	if got := Bye("Ada"); got != "Goodbye, Ada!" {
		t.Errorf("Bye = %q", got)
	}
}
EOF'
    }

    echo "── agy: one turn on the fixture (Gemini Flash, low effort, the harness loaded)"
    write_task
    src="${OWNER_HOME}/.gemini/antigravity-cli/antigravity-oauth-token"
    if [ ! -s "${src}" ]; then fail "agy: no login to copy"; else
        docker exec "${NAME}" install -d -o agent -g agent -m 0700 /home/agent/.gemini/antigravity-cli
        docker cp "${src}" "${NAME}:/home/agent/.gemini/antigravity-cli/antigravity-oauth-token"
        # Full access inside the sandbox, as crew-run will write it (12.2), and paid credits off.
        docker exec -i "${NAME}" sh -c 'cat > /home/agent/.gemini/antigravity-cli/settings.json' <<'EOF'
{"useG1Credits": false,
 "permissions": {"allow": ["command(*)", "read_file(*)", "write_file(*)", "mcp(*)"]},
 "trustedWorkspaces": ["/home/agent/fixture"]}
EOF
        docker exec "${NAME}" chown -R agent:agent /home/agent/.gemini/antigravity-cli
        docker exec "${NAME}" chmod 0600 /home/agent/.gemini/antigravity-cli/antigravity-oauth-token
        as_agent "timeout 300 agy -p \"${task}\" --output-format stream-json --model gemini-3.8-flash-low --print-timeout 280s </dev/null > /tmp/agy.jsonl 2>&1" >/dev/null 2>&1
        docker exec "${NAME}" cat /tmp/agy.jsonl > "${LOGS}/agy.jsonl"
        check "agy ended its turn with a result" python3 -I "${REPO}/tools/e2e/result.py" agy "${LOGS}/agy.jsonl"
        check "agy's work passes make test" as_agent make test
        docker exec "${NAME}" rm -f /home/agent/.gemini/antigravity-cli/antigravity-oauth-token
    fi
fi

[ -n "${LOGS:-}" ] && [ -n "${SUDO_USER:-}" ] && chown -R "${SUDO_USER}:" "${LOGS}"
echo
echo "e2e sandbox: PASS=${PASS} FAIL=${FAIL}"
[ "${FAIL}" = 0 ]
