#!/usr/bin/env bash
# deploy/lib/machine-care-test.sh — exercises deploy/lib/machine-care.sh against a scratch dir it
# creates itself: no root, no /etc/apt, no systemd. internal/config/machine_care_script_test.go runs it
# under `go test ./...`. Whether apt takes the settings as meant was probed on Debian 13 and Ubuntu
# 26.04 (docs/testing, M17 phase 5); this pins the text that was probed.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=machine-care.sh
source "${HERE}/machine-care.sh"
PASS=0; FAIL=0
ok()  { echo "PASS $*"; PASS=$((PASS + 1)); }
bad() { echo "FAIL $*"; FAIL=$((FAIL + 1)); }
expect() { if [[ "$2" == "$3" ]]; then ok "$1"; else bad "$1: want '$2', got '$3'"; fi; }
has() { if grep -qxF -- "$3" <<<"$2"; then ok "$1"; else bad "$1: no line '$3'"; fi; }

SCRATCH="$(mktemp -d)"
trap 'rm -rf "${SCRATCH}"' EXIT

# The settings of unattended-upgrades: the distro's own origins cleared, the security origin alone,
# Docker's packages never, and no restart by unattended-upgrades itself.
for id in debian ubuntu; do
    c="$(machine_care_upgrades_content "${id}")"
    has "${id}: allowed origins cleared" "${c}" '#clear Unattended-Upgrade::Allowed-Origins;'
    has "${id}: origin patterns cleared" "${c}" '#clear Unattended-Upgrade::Origins-Pattern;'
    has "${id}: no restart by itself" "${c}" 'Unattended-Upgrade::Automatic-Reboot "false";'
    for p in docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin; do
        has "${id}: ${p} never" "${c}" "        \"${p}\";"
    done
    n="$(grep -c '^        "origin=' <<<"${c}")"
    expect "${id}: one origin" 1 "${n}"
done
has "debian: the security archive" "$(machine_care_upgrades_content debian)" \
    '        "origin=Debian,codename=${distro_codename}-security,label=Debian-Security";'
has "ubuntu: the security pocket" "$(machine_care_upgrades_content ubuntu)" \
    '        "origin=Ubuntu,archive=${distro_codename}-security,label=Ubuntu";'
if machine_care_upgrades_content fedora >/dev/null 2>&1; then bad "fedora: accepted"; else ok "another distro: refused"; fi
has "periodic: the lists" "$(machine_care_periodic_content)" 'APT::Periodic::Update-Package-Lists "1";'
has "periodic: unattended-upgrades" "$(machine_care_periodic_content)" 'APT::Periodic::Unattended-Upgrade "1";'

# The units: crew-ctl's verb, every 15 minutes and 10 minutes after the start.
has "service: the verb" "$(machine_care_service_content /usr/local/sbin/crew-ctl)" 'ExecStart=/usr/local/sbin/crew-ctl restart-if-quiet'
has "service: a oneshot" "$(machine_care_service_content /usr/local/sbin/crew-ctl)" 'Type=oneshot'
# A Docker the operator stopped stays stopped: the ask fails, which is no restart.
has "service: Docker only if it runs" "$(machine_care_service_content /usr/local/sbin/crew-ctl)" 'Requisite=docker.service'
if grep -q '^Wants=' <<<"$(machine_care_service_content x)"; then bad "service: starts Docker"; else ok "service: starts no Docker"; fi
has "timer: every 15 minutes" "$(machine_care_timer_content)" 'OnUnitActiveSec=15min'
has "timer: after the start" "$(machine_care_timer_content)" 'OnBootSec=10min'
has "timer: enabled with the timers" "$(machine_care_timer_content)" 'WantedBy=timers.target'

# The writer: changed, then unchanged; a link is replaced, never followed.
p="${SCRATCH}/apt/52crew-unattended-upgrades"
expect "write: first" changed "$(machine_care_write "${p}" "a")"
expect "write: again" unchanged "$(machine_care_write "${p}" "a")"
expect "write: other content" changed "$(machine_care_write "${p}" "b")"
expect "write: content" b "$(cat "${p}")"
expect "write: mode" 644 "$(stat -c %a "${p}")"
printf 'target\n' > "${SCRATCH}/target"; rm -f "${p}"; ln -s "${SCRATCH}/target" "${p}"
expect "write: a link" changed "$(machine_care_write "${p}" "b")"
if [[ -f "${p}" && ! -L "${p}" ]]; then ok "write: a regular file"; else bad "write: still a link"; fi
expect "write: the link's target untouched" target "$(cat "${SCRATCH}/target")"

# The producer of reboot-required: there and writing it, or the reason.
root="${SCRATCH}/root"
if out="$(machine_care_producer debian "${root}")"; then bad "no producer: accepted"; else ok "no producer: ${out}"; fi
mkdir -p "${root}/etc/kernel/postinst.d"
printf '#!/bin/sh\ntouch /var/run/reboot-required\n' > "${root}/etc/kernel/postinst.d/unattended-upgrades"
if machine_care_producer debian "${root}" >/dev/null; then bad "a producer that cannot run: accepted"; else ok "a producer that cannot run: refused"; fi
chmod +x "${root}/etc/kernel/postinst.d/unattended-upgrades"
expect "debian: the producer" "${root}/etc/kernel/postinst.d/unattended-upgrades" "$(machine_care_producer debian "${root}")"
printf '#!/bin/sh\nexit 0\n' > "${root}/etc/kernel/postinst.d/update-notifier"; chmod +x "${root}/etc/kernel/postinst.d/update-notifier"
if machine_care_producer ubuntu "${root}" >/dev/null; then bad "a hook that writes no reboot-required: accepted"; else ok "a hook that writes nothing: refused"; fi
printf '#!/bin/sh\n/usr/share/update-notifier/notify-reboot-required\n' > "${root}/etc/kernel/postinst.d/update-notifier"
expect "ubuntu: the producer" "${root}/etc/kernel/postinst.d/update-notifier" "$(machine_care_producer ubuntu "${root}")"

leftover="$(find "${SCRATCH}" -name '*.??????' | wc -l)"
expect "no temporary file left" 0 "${leftover}"
echo "machine-care-test: ${PASS} passed, ${FAIL} failed"
[[ "${FAIL}" -eq 0 ]]
