#!/usr/bin/env bash
# Runs as root inside the install test's fresh virtual machine, from a clone of the checkout
# (design 16.11, 22 "Install, update, uninstall"; spike items 34, 64, and the Kata 4 half of 13).
#   PHASE=cycle  doctor, install from one file of answers, status, the selftest, edit, backup, settings
#                out and in, migrate, restore, a newer checkout over the running one, a release built,
#                gated, and installed by the updater, a roll back, a build by hand, uninstall at each
#                level with an install again after level 1 and 2 on the kept data, nothing left after
#                level 3 with the runtime, and an install again on the fresh machine.
#   PHASE=kata4  the move from Kata 3.31 to versions.lock's 4.x (13.2, since 0.68), as a machine set up by
#                MACHINE_SETUP 4 makes it: 3.31 from the host setup of KATA_OLD_COMMIT, a microVM left
#                running on it, a new Kata that fails its proof and changes nothing, then the move: each
#                sandbox size with its CPUs and memory, the 3.31 microVM going on and removed, and
#                /opt/kata taken out by the next run of the host setup once nothing runs from it.
#   PHASE=halfway  an install stopped half way on the fresh machine, after the machine's setup and
#                before the images: doctor says it is a repair, and install run again goes on (21).
#   PHASE=e2e    install, the browser tests through the real ingress (tools/e2e/browser), and
#                recovery: each service killed, Docker restarted; then it asks vm.sh for a reboot
#                (exit 100), and PHASE=e2e-after-reboot checks that all of it comes back.
#   PHASE=second  another machine (16.12), as tools/install-test/second.sh made it: two releases built on
#                this machine, their images in a throwaway registry with TLS and a made-up login; the
#                first install from the card of one, with the crew-ctl unpacked from it, its images
#                pulled to the containerd store; the card of the later one pulled, waiting for Install,
#                and installed; a card whose image the registry swapped, refused.
#   PHASE=restart  the machine's own security updates (the security origin alone, the producer of
#                reboot-required there), and the restart they need (16.10): with machine_care.restart_hours
#                away from the clock crew-restart.service restarts nothing; with the hours around it,
#                it restarts the machine (exit 101: vm.sh waits for the machine to go and come back), and
#                PHASE=restart-after-reboot checks that Crew came back, with the news, and that
#                uninstall takes the timer and the settings out.
# Each step prints PASS or FAIL; the script ends with the count, and exits 1 on any FAIL.
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 2
REPO="$(pwd)"
PASS=0 FAIL=0
pass() { echo "PASS $*"; PASS=$((PASS + 1)); }
fail() { echo "FAIL $*"; FAIL=$((FAIL + 1)); }
step() { local name="$1"; shift; echo "── ${name}: $*"; if "$@"; then pass "${name}"; else fail "${name}"; return 1; fi; }
# must: a step the rest cannot do without; on a failure the cycle ends there.
must() { step "$@" && return 0; { echo; echo "install-test ${DISTRO:-?} ${PHASE:-cycle}: stopped at $1: PASS=${PASS} FAIL=${FAIL}"; exit 1; }; }
ctl() { "${REPO}/deploy/crew-ctl" "$@"; }
section() { echo; echo "════ $* ($(date -u +%T))"; }

cat > /root/test.env <<'EOF'
CREW_HOSTNAME=crew.install.test
CREW_ADMIN_EMAIL=admin@example.com
CREW_TLS=internal
EOF
chmod 600 /root/test.env

proof_rows() { # the install proofs in the database's history
    docker exec crew-db sh -c 'PGPASSWORD="$(cat /run/secrets/db_crew_read)" psql -h 127.0.0.1 -U crew_read -d crew -tAc "SELECT count(*) FROM changes WHERE tbl = '"'"'install'"'"'"' 2>/dev/null | tr -d ' '
}
running_version() { docker inspect -f '{{index .Config.Labels "crew.version"}}' crew-server 2>/dev/null; }
nothing_left() {
    local left=()
    for p in /etc/crew /var/lib/crew /usr/local/lib/crew /usr/local/sbin/crew-ctl /usr/local/sbin/crew-firewall.sh \
             /etc/systemd/system/crew-firewall.service /etc/systemd/system/crew-firewall.timer \
             /etc/systemd/system/crew-restart.service /etc/systemd/system/crew-restart.timer \
             /etc/apt/apt.conf.d/52crew-unattended-upgrades /etc/apt/apt.conf.d/21crew-auto-upgrades \
             /etc/systemd/system/docker.service.d/50-crew-oom.conf /etc/tmpfiles.d/crew-ksm.conf /opt/kata \
             /usr/local/bin/containerd-shim-mvm-v2 /etc/docker/daemon.json /opt/kata-[0-9]*; do
        [ -e "${p}" ] && left+=("${p}")
    done
    id crew >/dev/null 2>&1 && left+=("user crew")
    hash -r   # the shell remembers where docker was
    command -v docker >/dev/null && left+=("docker")
    iptables -S 2>/dev/null | grep 'CREW-' >/dev/null && left+=("firewall chains")
    [ ${#left[@]} = 0 ] || { echo "left behind: ${left[*]}"; return 1; }
}


# The browser tests, in the pinned Playwright image, against this machine's Crew.
browser_tests() {
    local img; img="$(sed -n 's/^PLAYWRIGHT_IMAGE=//p' versions.lock)"
    docker run --rm --network host --add-host "crew.install.test:127.0.0.1" --add-host "auth.crew.install.test:127.0.0.1" \
        -e CREW_HOSTNAME=crew.install.test -e CREW_ADMIN_EMAIL=admin@example.com -e CREW_FRESH_MACHINE=1 \
        -e CREW_JWT_KEY="$(cat /etc/crew/secrets/jwt_key)" -e HOME=/tmp -e npm_config_cache=/tmp/.npm \
        -v "${REPO}/tools/e2e/browser:/e2e:ro" -w /tmp "${img}" \
        sh -c 'cp -r /e2e /tmp/e2e && cd /tmp/e2e && npm ci --no-audit --no-fund >/dev/null && npx playwright test'
}
health() { curl -sk --max-time 5 --resolve crew.install.test:443:127.0.0.1 https://crew.install.test/healthz; }
# healthy WORD VALUE: the health check's WORD is VALUE (a function, so wait_for can call it)
healthy() { health | grep "\"$1\":\"$2\"" >/dev/null; }
# wait_for SECONDS COMMAND...: true as soon as the command is
wait_for() { local n="$1"; shift; for _ in $(seq 1 "${n}"); do "$@" >/dev/null 2>&1 && return 0; sleep 1; done; return 1; }
crash() { kill -9 "$(docker inspect -f '{{.State.Pid}}' "$1")"; }
# sql QUERY: one value from Crew's database, with the role that only reads.
sql() { docker exec crew-db sh -c "PGPASSWORD=\$(cat /run/secrets/db_crew_read) psql -h 127.0.0.1 -U crew_read -d crew -tAc \"$1\""; }
# restart_hours FROM-TO: machine_care.restart_hours set through the settings' export and import (14.3).
restart_hours() {
    ctl settings export > /root/crew.yaml || return 1
    python3 -I - "$1" /root/crew.yaml <<'PY' || return 1
import re, sys
hours, path = sys.argv[1], sys.argv[2]
text = open(path).read()
text, n = re.subn(r'(?m)^(  restart_hours: ).*$', r'\g<1>"' + hours + '"', text)
if n != 1:
    sys.exit("no restart_hours in the export")
open(path, "w").write(text)
PY
    ctl settings import /root/crew.yaml
}
wanted_set() { [ -n "$(sql 'SELECT reboot_wanted_at FROM crew_state')" ]; }
not_held() { [ -z "$(sql 'SELECT restart_wait FROM crew_state')" ]; }
restart_news() { sql "SELECT text FROM machine_news WHERE kind = 'restart'" | grep 'The machine restarts for security updates, in the hours' >/dev/null; }
cleared() { [ "$(sql 'SELECT count(*) FROM crew_state WHERE reboot_wanted_at IS NULL AND restart_wait IS NULL')" = 1 ]; }
# hours_around OFFSET: a range of two hours that begins OFFSET hours from now, on the server's clock (UTC
# here: the test's answers name no CREW_TIMEZONE).
hours_around() { local h; h=$(( (10#$(date -u +%H) + 24 + $1) % 24 )); printf '%02d:00-%02d:00' "${h}" $(( (h + 2) % 24 )); }

case "${PHASE:-cycle}" in
cycle)
    section "doctor on a fresh machine, with the answers"
    step "doctor" ctl doctor --env-file /root/test.env
    ctl doctor --env-file /root/test.env --json > /root/doctor.json || true

    section "install --build from one file of answers (item 64: time and disk on an empty Docker)"
    t0=$(date +%s)
    must "install" ctl install --build --env-file /root/test.env
    t1=$(date +%s)
    echo "item 64: install --build took $((t1 - t0)) s in all"
    docker system df 2>/dev/null
    du -sh /var/lib/docker 2>/dev/null
    docker images --format '{{.Repository}}:{{.Tag}} {{.Size}}' | grep -E '^crew-|postgres' || true
    cat /etc/crew/install.json

    section "status"
    step "status ok" ctl status
    ctl status --json
    step "the updater answers" sh -c "${REPO}/deploy/crew-ctl status | grep 'updater .*runs ' >/dev/null"
    step "the first release folder" sh -c 'ls /var/lib/crew/releases/*/release.json'

    section "the selftest: one ticket from its OK to Done with the fake harness"
    step "selftest" ctl selftest

    section "edit one value"
    step "edit" ctl edit CREW_TIMEZONE=Europe/Stockholm
    step "the value is in the install file" grep -qx 'CREW_TIMEZONE=Europe/Stockholm' /etc/crew/crew.env
    step "the server got it" sh -c 'docker inspect -f "{{range .Config.Env}}{{println .}}{{end}}" crew-server | grep -x TZ=Europe/Stockholm >/dev/null'
    step "a wrong value is refused" sh -c '! '"${REPO}"'/deploy/crew-ctl edit CREW_ADMIN_EMAIL=nobody'
    step "status ok after edit" ctl status

    section "backup, settings, migrate"
    step "backup" ctl backup
    step "a backup file is there" sh -c 'ls /var/lib/crew/backups/crew-*.tar.gz'
    ctl settings export > /root/crew.yaml
    step "settings export" grep -q 'clean_at: 85' /root/crew.yaml
    sed -i 's/clean_at: 85/clean_at: 80/' /root/crew.yaml
    step "settings import" ctl settings import /root/crew.yaml
    step "the import took" sh -c "${REPO}/deploy/crew-ctl settings export | grep 'clean_at: 80' >/dev/null"
    step "migrate" ctl migrate
    step "restore" ctl restore --confirm restore "$(ls /var/lib/crew/backups/crew-*.tar.gz | tail -1)"
    step "status ok after restore" ctl status

    section "a newer checkout over the running one"
    before_v="$(running_version)"; before_rows="$(proof_rows)"
    # The newer checkout changes the schema: one more numbered step (16.11, "crew-ctl migrate").
    n="$(ls internal/db/migrations | wc -l)"; next="$(printf '%04d' $((n + 1)))"
    # A step that adds no table: the gate's make check later on holds every table to a group of 15.3.
    printf -- "-- older versions read: yes\nCOMMENT ON TABLE jobs IS 'the install test''s step';\n" > "internal/db/migrations/${next}_install_test.sql"
    # Its line under the data model (15.3): the gate's make check holds every step since 64 to one.
    python3 -I - "$((n + 1))" docs/plan/tdd/15-backend.md <<'PY'
import sys
n, p = sys.argv[1], sys.argv[2]
lines = open(p).read().split("\n")
i = next(i for i, l in enumerate(lines) if l.startswith("- Since 0.") and "Schema step " in l)
lines.insert(i, "- Since 0.99, the install test's newer checkout: Schema step %s (older versions read: yes) comments the table `jobs`." % n)
open(p, "w").write("\n".join(lines))
PY
    git add internal/db/migrations docs/plan/tdd/15-backend.md
    git -c user.name=test -c user.email=test@example.com commit -q -m "a newer checkout, with a schema change"
    must "install from the newer checkout" ctl install --build --env-file /root/test.env
    step "the schema change is applied" sh -c "docker exec crew-db sh -c 'PGPASSWORD=\$(cat /run/secrets/db_crew_read) psql -h 127.0.0.1 -U crew_read -d crew -tAc \"SELECT max(number) FROM schema_steps\"' | grep -x $((n + 1)) >/dev/null"
    after_v="$(running_version)"
    echo "version ${before_v} -> ${after_v}; proofs ${before_rows} -> $(proof_rows)"
    step "the version changed" test "${before_v}" != "${after_v}"
    step "the data is kept" test "$(proof_rows)" -gt "${before_rows:-0}"

    section "a release: built once, gated, installed by the updater, and rolled back (16.2, 16.4, 16.5)"
    dev_v="$(running_version)"
    # The gate's own install test is this one: it is off for the release built here.
    ctl settings export | sed 's/install_test: local/install_test: off/' > /root/crew.yaml
    step "the install test is off for the gate inside it" ctl settings import /root/crew.yaml
    # A prompt of the baseline, which is text for agents (16.3).
    prompt="$(git ls-files 'baseline/stages/*.md' | head -1)"
    echo "a prompt changed for 0.1.0" >> "${prompt}"
    git -c user.name=test -c user.email=test@example.com commit -qam "a prompt for 0.1.0"
    must "build 0.1.0" ctl build --version 0.1.0
    # The gate runs make check, as on the machine releases are built on: a fresh machine has no make.
    step "make for the gate" sh -c 'DEBIAN_FRONTEND=noninteractive apt-get install -y -q make >/dev/null'
    # make check runs the firewall test in a user namespace of the checkout's owner (test-firewall),
    # which Ubuntu refuses an unprivileged user: a machine releases are built on allows it.
    step "user namespaces for the gate's make check" sh -c '! sysctl -n kernel.apparmor_restrict_unprivileged_userns >/dev/null 2>&1 || sysctl -qw kernel.apparmor_restrict_unprivileged_userns=0'
    step "the release folder names its images by hash" python3 -I -c 'import json; m=json.load(open("/var/lib/crew/releases/0.1.0/release.json")); assert len(m["images"]) == 5 and all(len(i["sha256"]) == 64 for i in m["images"].values())'
    step "a release that did not pass the gate is refused" sh -c '! '"${REPO}"'/deploy/crew-ctl update --version 0.1.0'
    step "the running server reads the install test as off" sh -c 'docker exec crew-server crew admin settings export | grep -Ex "  install_test: \"?off\"?"'
    must "the gate of 0.1.0" ctl gate 0.1.0
    # The live part is the running server's: a round every 30 s takes the pending gate up, and with no
    # CLI signed in on this machine (the selftest put its login back) it is skipped (16.2 step 5).
    live_ended() { python3 -I -c 'import json,sys; sys.exit(json.load(open("/var/lib/crew/releases/0.1.0/gate.json")).get("live") == "pending")'; }
    step "the server ended the gate's live part" wait_for 300 live_ended
    cat /var/lib/crew/releases/0.1.0/gate.json; echo
    step "gate.json says it passed" python3 -I -c 'import json; g=json.load(open("/var/lib/crew/releases/0.1.0/gate.json")); assert g["passed"] and g["installTest"] == "skipped" and g["live"] == "skipped" and any("no agent CLI is signed in" in s for s in g["skipped"])'
    must "update to 0.1.0" ctl update
    step "0.1.0 runs" test "$(running_version)" = 0.1.0
    step "status ok on 0.1.0" ctl status
    must "roll back to the version before" ctl rollback
    step "the version before runs again" test "$(running_version)" = "${dev_v}"
    step "status ok after the roll back" ctl status
    # An image file changed after the gate is not what was tested: the updater refuses it (16.4).
    caddy=/var/lib/crew/releases/0.1.0/images/crew-caddy.tar; size="$(stat -c %s "${caddy}")"
    printf x >> "${caddy}"
    step "a changed image file is refused" sh -c '! '"${REPO}"'/deploy/crew-ctl update --version 0.1.0'
    truncate -s "${size}" "${caddy}"
    section "a build by hand, given by root (16.10)"
    echo "a prompt changed for 0.2.0" >> "${prompt}"
    git -c user.name=test -c user.email=test@example.com commit -qam "a prompt for 0.2.0"
    must "build 0.2.0" ctl build --version 0.2.0
    step "the app's way is refused without the gate" sh -c '! '"${REPO}"'/deploy/crew-ctl update --version 0.2.0'
    must "install 0.2.0 by hand" ctl install --from /var/lib/crew/releases/0.2.0 --confirm install
    step "0.2.0 runs" test "$(running_version)" = 0.2.0
    step "status ok on 0.2.0" ctl status

    section "uninstall level 1, then install again"
    step "uninstall 1" ctl uninstall --level 1 --confirm stop
    step "the services are stopped" sh -c '! docker ps --format "{{.Names}}" | grep crew-server >/dev/null'
    rows="$(docker start crew-db >/dev/null; sleep 5; proof_rows)"
    must "install after level 1" ctl install --build --env-file /root/test.env
    step "status ok" ctl status

    section "uninstall level 2, then install again on the kept data"
    rows="$(proof_rows)"
    step "uninstall 2" ctl uninstall --level 2 --confirm uninstall
    step "the data folder is kept" test -d /var/lib/crew/db
    step "/etc/crew is kept" test -f /etc/crew/secret.key
    step "no service is left" sh -c '! docker ps -a --format "{{.Names}}" | grep "^crew-" >/dev/null'
    # Crew's own images; make check's crew-check image, which the gate built here, is the build
    # machine's and not the installation's.
    docker images --format '{{.Repository}}:{{.Tag}}' | grep '^crew-' || true
    step "no image is left" sh -c '! docker images --format "{{.Repository}}" | grep -E "^crew-(server|caddy|runtime|updater)$" >/dev/null'
    step "no firewall chain is left" sh -c '! iptables -S | grep CREW- >/dev/null'
    step "doctor after level 2" /usr/local/sbin/crew-ctl doctor --env-file /root/test.env
    must "install after level 2 on the kept data" ctl install --build --env-file /root/test.env
    step "the kept database is opened again" test "$(proof_rows)" -gt "${rows:-0}"

    section "uninstall level 3 with the runtime: nothing is left"
    step "uninstall 3" ctl uninstall --level 3 --with-runtime --confirm "$(hostname -s)"
    step "nothing is left" nothing_left
    if [ -f /etc/docker/daemon.json ]; then echo "daemon.json:"; cat /etc/docker/daemon.json; fi

    section "install again on the fresh machine"
    step "doctor finds a fresh machine" ctl doctor --env-file /root/test.env
    must "install again" ctl install --build --env-file /root/test.env
    step "status ok" ctl status
    ;;

e2e)
    section "install"
    must "install" ctl install --build --env-file /root/test.env
    section "the browser tests through the real ingress"
    step "browser tests" browser_tests
    section "a sandbox of the runtime image, working on the fixture project"
    step "sandbox e2e" bash tools/e2e/sandbox.sh
    section "recovery: a service that crashes comes back"
    step "the database is stopped" docker stop crew-db
    step "the health check says the database is down" wait_for 20 healthy database down
    step "the database is started again" docker start crew-db
    step "the server answers with the database again" wait_for 90 healthy database ok
    step "the database crashes" crash crew-db
    step "the database is back, and the server with it" wait_for 90 healthy database ok
    step "the server crashes" crash crew-server
    step "the server is back" wait_for 90 healthy status ok
    step "the ingress crashes" crash crew-caddy
    step "the ingress is back" wait_for 90 healthy status ok
    step "Docker restarts" systemctl restart docker
    step "all of Crew is back after Docker's restart" wait_for 120 healthy database ok
    step "status ok" wait_for 60 ctl status
    echo "install-test ${DISTRO:-?} e2e: PASS=${PASS} FAIL=${FAIL}, and now a reboot"
    exit 100
    ;;
e2e-after-reboot)
    section "after the machine's restart"
    step "all of Crew is back" wait_for 180 healthy database ok
    step "status ok" wait_for 60 ctl status
    step "the firewall is in place" sh -c 'iptables -S DOCKER-USER | grep CREW-FWD >/dev/null && iptables -S INPUT | grep CREW-IN >/dev/null'
    step "the sandbox proof" ctl proof
    step "browser tests" browser_tests
    ;;
restart)
    section "install"
    must "install" ctl install --build --env-file /root/test.env
    section "the machine's own security updates (16.10)"
    # shellcheck disable=SC1091
    . /etc/os-release
    step "the producer of reboot-required" bash -c "source deploy/lib/machine-care.sh && machine_care_producer ${ID} ''"
    step "unattended-upgrades takes the security origin alone" sh -c \
        'unattended-upgrade --dry-run --debug 2>&1 | grep -Ex "Allowed origins are: origin=(Debian,codename=[a-z]+-security,label=Debian-Security|Ubuntu,archive=[a-z]+-security,label=Ubuntu)"'
    step "no restart by unattended-upgrades itself" sh -c 'apt-config dump | grep -x "Unattended-Upgrade::Automatic-Reboot \"false\";" >/dev/null'
    step "apt's daily run is on" sh -c 'apt-config dump | grep -x "APT::Periodic::Unattended-Upgrade \"1\";" >/dev/null'
    step "crew-restart.timer runs" systemctl is-active --quiet crew-restart.timer
    step "status names the timer" sh -c '"${0}" status | grep -E "crew-restart.timer +active"' "${REPO}/deploy/crew-ctl"
    section "a restart outside the hours: none"
    step "the hours away from the clock" restart_hours "$(hours_around 6)"
    touch /var/run/reboot-required
    echo linux-image-crew-test > /var/run/reboot-required.pkgs
    step "crew-restart.service asks, and restarts nothing" systemctl start crew-restart.service
    step "the server said why" sh -c 'journalctl -u crew-restart.service --no-pager | grep "outside the hours" >/dev/null'
    step "the first yes is recorded" wanted_set
    step "nothing holds Crew" not_held
    section "a restart in the hours"
    step "the hours around the clock" restart_hours "$(hours_around -1)"
    cat /proc/sys/kernel/random/boot_id > /root/boot-before
    step "crew-restart.service is started" systemctl start --no-block crew-restart.service
    echo "install-test ${DISTRO:-?} restart: PASS=${PASS} FAIL=${FAIL}, and the machine restarts by itself"
    exit 101
    ;;
restart-after-reboot)
    section "after the restart for security updates"
    step "the machine restarted" sh -c 'test "$(cat /proc/sys/kernel/random/boot_id)" != "$(cat /root/boot-before)"'
    step "all of Crew is back" wait_for 180 healthy database ok
    step "status ok" wait_for 60 ctl status
    step "the restart is news" restart_news
    step "reboot-required went with the restart" test ! -e /var/run/reboot-required
    step "the next ask clears what was kept" systemctl start crew-restart.service
    step "nothing is kept of the restart" cleared
    section "uninstall takes the timer and the settings out"
    step "uninstall level 2" ctl uninstall --level 2 --confirm uninstall
    step "no restart timer, no settings of Crew's" sh -c 'for f in /etc/systemd/system/crew-restart.service /etc/systemd/system/crew-restart.timer /etc/apt/apt.conf.d/52crew-unattended-upgrades /etc/apt/apt.conf.d/21crew-auto-upgrades; do test ! -e "$f" || { echo "left: $f"; exit 1; }; done'
    step "the timer is gone from systemd" sh -c '! systemctl is-active --quiet crew-restart.timer'
    ;;
halfway)
    section "an install stopped half way, then run again (21)"
    # setsid: the install and everything it started are one group, stopped at once, as a lost
    # session or a power cut would.
    setsid "${REPO}/deploy/crew-ctl" install --build --env-file /root/test.env > /root/half.log 2>&1 &
    half=$!
    machine_done() { python3 -I -c 'import json,sys; sys.exit("machine_setup" not in json.load(open("/etc/crew/install.json")))'; }
    half_ready() { machine_done || ! kill -0 "${half}" 2>/dev/null; }
    wait_for 1800 half_ready
    must "the install got past the machine's setup" machine_done
    kill -9 -- "-${half}"; wait "${half}" 2>/dev/null
    tail -5 /root/half.log
    not_installed() { python3 -I -c 'import json,sys; sys.exit("version" in json.load(open("/etc/crew/install.json")))' && ! docker ps --format '{{.Names}}' | grep crew-server >/dev/null; }
    step "it stopped before it recorded a version, and no server runs" not_installed
    "${REPO}/deploy/crew-ctl" doctor --env-file /root/test.env > /root/doctor-half.txt 2>&1 || true
    cat /root/doctor-half.txt
    step "doctor says it is a repair" grep -q "this is a repair or an update" /root/doctor-half.txt
    step "doctor says it stopped before it finished" grep -q "it stopped before it finished" /root/doctor-half.txt
    must "install run again goes on" ctl install --build --env-file /root/test.env
    step "the record keeps what the first run installed" python3 -I -c 'import json,sys; m=json.load(open("/etc/crew/install.json"))["machine"]; sys.exit(not m.get("docker","").startswith("installed"))'
    step "status ok" ctl status
    step "uninstall 3" ctl uninstall --level 3 --with-runtime --confirm "$(hostname -s)"
    step "nothing is left" nothing_left
    ;;
kata4)
    # shellcheck source=../../deploy/lib/kata-config.sh
    source deploy/lib/kata-config.sh
    lock() { sed -n "s/^$1=//p" versions.lock; }
    img="$(lock RUNTIME_BASE_IMAGE)" new="$(lock KATA_VERSION)" newdir="$(kata_dir "" "$(lock KATA_VERSION)")"
    # The machine setup before 0.68, from the commit that last had it.
    old_commit="${KATA_OLD_COMMIT:-fe5dc49}"
    rm -rf /root/old && mkdir -p /root/old
    git archive "${old_commit}" deploy versions.lock | tar -x -C /root/old
    old_lock() { sed -n "s/^$1=//p" /root/old/versions.lock; }
    # host_setup DIR LOCKFN RECORD [ENV...]: one run of a host setup, with the versions of its lock.
    host_setup() {
        local dir="$1" lk="$2" rec="$3"; shift 3
        env CREW_DATA_DIR=/var/lib/crew DOCKER_VERSION="$(${lk} DOCKER_VERSION)" KATA_VERSION="$(${lk} KATA_VERSION)" \
            KATA_SHA256="$(${lk} KATA_SHA256)" PROOF_IMAGE="${img}" "$@" bash "${dir}/deploy/host-setup.sh" --record "${rec}"
    }
    # The record a host setup writes goes into install.json as crew-ctl merges it, and the next run gets
    # its KATA_OURS from there, as crew-ctl install gives it (13.2, 16.11).
    install -d -m 0700 /etc/crew
    merged() { bash -c 'source deploy/crew-ctl; merge_host_record "$1"' _ "$1"; }
    ours() { bash -c 'source deploy/crew-ctl; kata_ours'; }
    mvm_type() { python3 -I -c 'import json; print(json.load(open("/etc/docker/daemon.json"))["runtimes"]["mvm"].get("runtimeType",""))'; }
    sized() { # ARGS...: what a microVM started with these limits has: its CPUs and its MemTotal in MiB
        timeout 180 docker run --rm --runtime mvm "$@" "${img}" sh -c 'nproc; echo $(( $(sed -n "s/^MemTotal: *\([0-9]*\) kB$/\1/p" /proc/meminfo) / 1024 ))' 2>&1 | tr '\n' ' '
    }
    # about CPUS MIB OUT: the microVM has CPUS and, of MIB, all but the guest kernel's own (at most 10%).
    about() { local c m; read -r c m <<< "$3"; [ "${c}" = "$1" ] && [ -n "${m}" ] && [ "${m}" -le "$2" ] && [ "${m}" -ge $(( $2 * 9 / 10 )) ]; }

    section "Kata $(old_lock KATA_VERSION), as machine setup $(old_lock MACHINE_SETUP) installs it (${old_commit})"
    must "old host setup" host_setup /root/old old_lock /root/old.rec
    must "its record in install.json" merged /root/old.rec
    step "3.31 is Crew's to remove later" test "$(ours)" = /opt/kata
    step "3.31's runtime is the wrapper" test "$(mvm_type)" = io.containerd.mvm.v2
    t0=$(date +%s%N); out="$(timeout 180 docker run --rm --runtime mvm "${img}" uname -r 2>&1)"; t1=$(date +%s%N)
    echo "3.31: kernel ${out}, started and removed in $(( (t1 - t0) / 1000000 )) ms"
    step "3.31 starts a microVM" test "${out}" != "$(uname -r)"
    echo "3.31, small: $(sized --cpus 1)"
    must "a microVM left on 3.31" docker run -d --name kata-old --runtime mvm "${img}" sleep 3600

    section "Kata ${new}, broken, fails its proof and changes nothing"
    asset="kata-static-${new}-amd64.tar.zst"
    curl -fsSL -o /root/k.tar.zst "https://github.com/kata-containers/kata-containers/releases/download/${new}/${asset}"
    must "the archive's checksum" sh -c "echo '$(lock KATA_SHA256)  /root/k.tar.zst' | sha256sum -c -"
    must "unpacked beside it" kata_unpack /root/k.tar.zst "${newdir}" "${new}"
    rm -f /root/k.tar.zst
    mv "${newdir}/bin/cloud-hypervisor" "${newdir}/bin/cloud-hypervisor.away"
    host_setup . lock /root/broken.rec KATA_OURS="$(ours)" > /root/broken.log 2>&1; rc=$?
    tail -3 /root/broken.log
    step "a failed proof stops the host setup" test "${rc}" != 0
    step "it says the proof failed" grep -q "did not pass its proof" /root/broken.log
    step "mvm still starts 3.31" test "$(mvm_type)" = io.containerd.mvm.v2
    step "mvm-next is gone" sh -c '! docker info --format "{{json .Runtimes}}" | grep mvm-next >/dev/null'
    out="$(timeout 180 docker run --rm --runtime mvm "${img}" uname -r 2>&1)"
    step "a new microVM starts on 3.31 still" test "${out}" != "$(uname -r)"
    mv "${newdir}/bin/cloud-hypervisor.away" "${newdir}/bin/cloud-hypervisor"

    section "the move to Kata ${new}"
    must "new host setup" host_setup . lock /root/new.rec KATA_OURS="$(ours)"
    cat /root/new.rec
    must "its record in install.json" merged /root/new.rec
    step "install.json lists both folders" test "$(ours | sort | tr '\n' ' ')" = "/opt/kata /opt/kata-${new} "
    step "mvm names ${new}'s shim" test "$(mvm_type)" = "${newdir}/${KATA_SHIM}"
    step "with its configuration as ConfigPath" python3 -I -c 'import json,sys; sys.exit(json.load(open("/etc/docker/daemon.json"))["runtimes"]["mvm"]["options"]["ConfigPath"] != sys.argv[1])' "${newdir}/${KATA_CLH_CONF}"
    step "Kata's own file untouched" grep -q '^overhead_memory = 32$' "${newdir}/${KATA_CLH_CONF}"
    step "the 3.31 microVM runs on" test "$(docker inspect -f '{{.State.Running}}' kata-old)" = true
    step "and answers" docker exec kata-old true
    step "/opt/kata kept while it runs" test -x /opt/kata/bin/containerd-shim-kata-v2
    step "the record names it for later" grep -qx 'kata_dir /opt/kata' /root/new.rec
    step "no proof microVM is left" sh -c '! docker ps -a --format "{{.Names}}" | grep -x crew-kata-proof >/dev/null'
    t0=$(date +%s%N); out="$(timeout 180 docker run --rm --runtime mvm "${img}" uname -r 2>&1)"; t1=$(date +%s%N)
    echo "${new}: kernel ${out}, started and removed in $(( (t1 - t0) / 1000000 )) ms"
    step "${new} starts a microVM with its own kernel" test "${out}" != "$(uname -r)"
    mkdir -p /root/share && echo hello > /root/share/x
    step "${new} shares a folder" test "$(timeout 180 docker run --rm --runtime mvm -v /root/share:/share "${img}" cat /share/x 2>&1)" = hello

    section "the sizes (13.10, 14.1): the same as 3.31's"
    out="$(sized)"; echo "base, no limits: ${out}"; step "base: 1 CPU, 2 GB" about 1 2048 "${out}"
    out="$(sized --cpus 1)"; echo "small: ${out}"; step "small: 2 CPUs, 2 GB" about 2 2048 "${out}"
    out="$(sized --cpus 3 --memory 4g)"; echo "large: ${out}"; step "large: 4 CPUs, 6 GB" about 4 6144 "${out}"
    out="$(sized --cpus 3 --memory 6g)"; echo "build: ${out}"; step "build: 4 CPUs, 8 GB" about 4 8192 "${out}"
    # The largest size Kata takes on this machine: every CPU, and the memory less Kata's 2 GB, in blocks
    # of 128 MiB.
    host_cpus="$(docker info --format '{{.NCPU}}')"; host_mib=$(( $(docker info --format '{{.MemTotal}}') >> 20 ))
    whole=$(( (host_mib - 2048) / 128 * 128 ))
    out="$(sized --cpus "${host_cpus}" --memory "${whole}m")"; echo "the whole machine (${host_cpus} CPUs, ${whole} MiB asked): ${out}"
    step "the whole machine starts" sh -c "echo '${out}' | grep -E '^[0-9]+ [0-9]+ \$' >/dev/null"

    section "the 3.31 microVM removed, then /opt/kata by the next run"
    step "the 3.31 microVM is removed" docker rm -f kata-old
    not_busy() { ! kata_busy /opt/kata /proc >/dev/null; }
    step "nothing runs from /opt/kata" not_busy
    must "host setup again" host_setup . lock /root/again.rec KATA_OURS="$(ours)"
    must "its record in install.json" merged /root/again.rec
    step "install.json lists ${new}'s folder alone" test "$(ours)" = "/opt/kata-${new}"
    step "it changed no runtime" sh -c '! grep -q "^daemon_json" /root/again.rec'
    step "/opt/kata is gone" test ! -e /opt/kata
    step "3.31's wrapper is gone" test ! -e /usr/local/bin/containerd-shim-mvm-v2
    step "${new} is kept" test -x "${newdir}/${KATA_SHIM}"
    out="$(timeout 180 docker run --rm --runtime mvm "${img}" uname -r 2>&1)"
    step "a microVM starts after it" test "${out}" != "$(uname -r)"
    ;;
second)
    # Another machine (16.12): it holds no checkout; what it was brought is in ../second (vm.sh), and
    # its crew-ctl is the one unpacked from the card's deploy archive.
    S="$(cd "${REPO}/.." && pwd)/second"
    REG=registry.install.test/owner
    CTL=/usr/local/sbin/crew-ctl
    is_version() { [ "$(running_version)" = "$1" ]; }
    waits() { [ "$(sql "SELECT state || '|' || coalesce(install_after::text, '') FROM releases WHERE version = '$1'")" = "waiting|" ]; }
    section "another machine: the cards it was brought, and the registry by a CA the test made"
    step "the cards, the CA, and the login are here" test -f "${S}/cards/0.99.1/release.json" -a -f "${S}/cards/0.99.2/release.json" -a -f "${S}/token" -a -f "${S}/ca.crt"
    install -m 0644 "${S}/ca.crt" /usr/local/share/ca-certificates/crew-install-test.crt && update-ca-certificates >/dev/null 2>&1
    echo "$(cat "${S}/registry-ip") registry.install.test" >> /etc/hosts
    step "the registry answers over TLS, and asks for a login" test "$(curl -s -o /dev/null -w '%{http_code}' https://registry.install.test/v2/)" = 401

    section "the first install, from the card of 0.99.1, its images pulled"
    mkdir -p /root/card && tar -xzf "${S}/cards/0.99.1/deploy.tar.gz" -C /root/card
    must "install --from the card" /root/card/deploy/crew-ctl install --from "${S}/cards/0.99.1" --registry "${REG}" --registry-token-file "${S}/token" --env-file /root/test.env
    step "0.99.1 runs" is_version 0.99.1
    step "it is healthy" wait_for 120 healthy status ok
    step "its images went to the containerd store" sh -c "docker info -f '{{json .DriverStatus}}' | grep io.containerd.snapshotter >/dev/null"
    step "its folder holds pulled.json and no image" sh -c 'test -f /var/lib/crew/releases/0.99.1/pulled.json && test ! -e /var/lib/crew/releases/0.99.1/images'
    pg="$(sed -n 's/^POSTGRES_IMAGE=//p' /usr/local/lib/crew/versions.lock)"
    step "PostgreSQL runs by the index the lock pins" test "$(docker inspect -f '{{.Config.Image}}' crew-db)" = "${pg}"
    step "no login is left" sh -c '! ls -d /run/crew-login.* 2>/dev/null && ! grep -rqs made-up-second-token /etc/crew /var/lib/crew/releases /run /root/.docker /root/.config'

    section "a later release: the card of 0.99.2, pulled, waits for Install"
    must "release pull of 0.99.2" "${CTL}" release pull "${S}/cards/0.99.2" --registry "${REG}" --registry-token-file "${S}/token"
    step "it waits for Install, with no time to install by itself" wait_for 300 waits 0.99.2
    must "Install of 0.99.2" "${CTL}" update --version 0.99.2
    step "0.99.2 runs" wait_for 600 is_version 0.99.2
    step "it is healthy" wait_for 120 healthy status ok
    step "no login is left after the pull" sh -c '! ls -d /run/crew-login.* 2>/dev/null && ! grep -rqs made-up-second-token /etc/crew /var/lib/crew/releases /run /root/.docker /root/.config'

    section "a card whose image the registry swapped"
    out="$("${CTL}" release pull "${S}/cards/0.99.3" --registry "${REG}" --registry-token-file "${S}/token" 2>&1)"; rc=$?
    echo "${out}" | tail -3
    step "it is refused, by its config" sh -c "[ ${rc} != 0 ] && printf '%s' \"\$1\" | grep 'is the image' >/dev/null" _ "${out}"
    step "nothing of it is kept" sh -c '! test -e /var/lib/crew/releases/0.99.3 && ! docker image inspect crew-caddy:0.99.3 >/dev/null 2>&1'
    step "0.99.2 still runs" is_version 0.99.2
    ;;
esac

echo
echo "install-test ${DISTRO:-?} ${PHASE:-cycle}: PASS=${PASS} FAIL=${FAIL}"
[ "${FAIL}" = 0 ]
