#!/usr/bin/env bash
# deploy/firewall/install.sh — install or converge the machine's half of
# sandbox network isolation. Idempotent and narrow: the firewall script and
# its two units, nothing else. crew-ctl install runs it (design 13.2, step 7).
#
# Usage (root):
#   sudo bash deploy/firewall/install.sh [--dry-run]
#   Advanced (root tests, never production paths): --bin-dir --unit-dir
#        override the fixed system paths.
set -euo pipefail
shopt -s inherit_errexit

BIN_DIR=/usr/local/sbin
UNIT_DIR=/etc/systemd/system
DRY=0

log() { echo "crew-firewall-install: $*"; }
die() { echo "crew-firewall-install: ERROR $*" >&2; exit 1; }
run() { if [ "${DRY}" = 1 ]; then echo "+ $*"; else "$@"; fi; }

while [ $# -gt 0 ]; do
    case "$1" in
        --bin-dir) BIN_DIR="$2"; shift 2 ;;
        --unit-dir) UNIT_DIR="$2"; shift 2 ;;
        --dry-run) DRY=1; shift ;;
        # Accepted and ignored so an operator's muscle memory, a stale runbook
        # or an old host_converge invocation does not fail the install. They
        # configured the LAN allowance machinery, which is gone.
        *) echo "unknown flag: $1" >&2; exit 2 ;;
    esac
done

[ "${DRY}" = 1 ] || [ "$(id -u)" = 0 ] || die "run as root (sudo), or pass --dry-run"
SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
for f in crew-firewall.sh crew-firewall.service crew-firewall.timer; do
    [ -f "${SRC}/${f}" ] || die "missing ${SRC}/${f}"
done
bash -n "${SRC}/crew-firewall.sh"

# ─── 1. Script + units ──────────────────────────────────────────────────────
log "installing ${BIN_DIR}/crew-firewall.sh"
run install -m 0755 -o root -g root "${SRC}/crew-firewall.sh" "${BIN_DIR}/crew-firewall.sh"
for f in crew-firewall.service crew-firewall.timer; do
    run install -m 0644 -o root -g root "${SRC}/${f}" "${UNIT_DIR}/${f}"
done

#
# RETIREMENT: this converge has no reachable consumer left. The NUC has been
# past it since v1.27.0, a host created by deploy/gcp/install.sh never had the
# artifacts, and the only caller that could still need it is a host frozen
# between v1.23 and v1.35 that upgrades later. It is kept for that host and
# for no other reason, a safety margin chosen over the deletion. DELETE IT,
# and this note, in the first release cut after the GCP target is used in
# anger.
# ─── 2. Converge away the old LAN allowance artifacts ───────────────────────
# Order matters: stop and disable before deleting the unit files, or systemd
# keeps a running unit whose fragment is gone.
run systemctl daemon-reload
# The oneshot exits 1 on a DEGRADED host (a missing xt_physdev, a tagged
# DROP it refused to delete — every WARN reaches its exit path). That must
# not abort this installer before the units are enabled (the updater's
# host_converge tolerates it the same way): log and go on.
run systemctl start crew-firewall.service \
    || log "WARN: crew-firewall.service reported a degraded state (journalctl -u crew-firewall) — continuing so the units are enabled"
# Enabled as a boot-time unit too: the base rules land once right after
# docker.service instead of at the timer's first tick (the service's
# [Install] section).
run systemctl enable crew-firewall.service
run systemctl enable --now crew-firewall.timer
if [ "${DRY}" = 0 ]; then
    log "CREW-FWD:"
    iptables -S CREW-FWD 2>/dev/null | sed 's/^/    /' || true
    log "CREW-IN:"
    iptables -S CREW-IN 2>/dev/null | sed 's/^/    /' || true
    log "CREW-MDL:"
    iptables -S CREW-MDL 2>/dev/null | sed 's/^/    /' || true
    log "done. Verify: systemctl status crew-firewall.timer"
else
    log "dry run complete - nothing written"
fi
