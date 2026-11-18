#!/usr/bin/env bash
# Crew's host setup (design 13.2): turns a fresh Debian or Ubuntu machine with KVM into one that
# can run Crew: Docker CE and Kata Containers with Cloud Hypervisor at their fixed versions, the
# settings Docker needs, the firewall, the system user crew, the data folders, and the machine's own
# security updates with the timer that restarts it for them (16.10). It ends by starting one microVM. crew-ctl install runs it as root; it can be run again, and every step looks
# before it changes anything.
#
#   sudo deploy/host-setup.sh --record FILE
#
# The versions come from the environment, which crew-ctl fills from versions.lock: DOCKER_VERSION,
# KATA_VERSION, KATA_SHA256, and PROOF_IMAGE (the image the proof microVM starts from). CREW_DATA_DIR
# is the data folder. KATA_OURS lists, one per line, the folders of Kata that install.json says Crew
# put there (crew-ctl reads it): only those are removed, once nothing runs from them.
#
# --record FILE: one line per part, `key value`, saying whether Crew put it there or found it, for
# crew-ctl to write into /etc/crew/install.json, so that uninstall takes out only what install put
# in (16.11).
#
# Left out of styrman-crew's script: writing its server's .env, building its runtime image with a
# GitHub token, installing its updater, and adding the operator's account to the docker group. No
# account of a person joins that group: the server runs as the system user crew.
set -euo pipefail
shopt -s inherit_errexit

: "${DOCKER_VERSION:?set DOCKER_VERSION (crew-ctl reads it from versions.lock)}"
: "${KATA_VERSION:?set KATA_VERSION}"
: "${KATA_SHA256:?set KATA_SHA256}"
: "${PROOF_IMAGE:?set PROOF_IMAGE}"
DATA_DIR="${CREW_DATA_DIR:-/var/lib/crew}"
RECORD=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --record) RECORD="$2"; shift 2 ;;
        *) echo "host-setup: unknown flag: $1" >&2; exit 2 ;;
    esac
done
[[ $(id -u) -eq 0 ]] || { echo "host-setup: run as root" >&2; exit 1; }
[[ -n "${RECORD}" ]] || { echo "host-setup: pass --record FILE" >&2; exit 2; }
: > "${RECORD}"

SCRIPT_DIR="$(cd "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")" && pwd)"
# A step that fails under set -e says which, instead of ending the script without a word.
trap 'echo "host-setup: FATAL at line ${LINENO}: ${BASH_COMMAND}" >&2' ERR
log() { echo "==> $*"; }
record() { echo "$1 $2" >> "${RECORD}"; }
die() { echo "host-setup: FATAL $*" >&2; exit 1; }

# ─── 1. Check ────────────────────────────────────────────────────────────────
# shellcheck disable=SC1091
. /etc/os-release
case "${ID:-}" in
    debian|ubuntu) ;;
    *) die "this is ${PRETTY_NAME:-an unknown system}; Crew installs on Debian or Ubuntu only" ;;
esac
[[ "$(uname -m)" == x86_64 ]] || die "Crew runs on AMD64 only"
if [[ ! -e /dev/kvm ]]; then
    if grep -qE 'vmx|svm' /proc/cpuinfo; then
        die "/dev/kvm is missing although the CPU has virtualization: check the firmware and the kvm modules"
    fi
    die "no hardware virtualization: Crew needs bare metal or a virtual machine with nested virtualization"
fi
log "system: ${PRETTY_NAME}, KVM present"

apt_install() { # apt_install PACKAGE...: installs what is missing, and records each package added
    local p missing=()
    for p in "$@"; do dpkg-query -W -f='${Status}' "$p" 2>/dev/null | grep 'install ok installed' >/dev/null || missing+=("$p"); done
    [[ ${#missing[@]} -eq 0 ]] && return 0
    log "installing ${missing[*]}"
    DEBIAN_FRONTEND=noninteractive apt-get update -qq
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends "${missing[@]}" >/dev/null
    for p in "${missing[@]}"; do record package "$p"; done
}
apt_install ca-certificates curl python3 zstd iptables

# ─── 2. Docker ───────────────────────────────────────────────────────────────
# From Docker's own package source at the fixed version (13.2), with its signing key checked against
# the fingerprint Docker publishes. Not its install script: that runs unpinned code as root.
DOCKER_KEY_FINGERPRINT=9DC858229FC7DD38854AE2D88D81803C0EBFCD88
if ! command -v docker >/dev/null; then
    log "installing Docker CE ${DOCKER_VERSION} from Docker's package source"
    apt_install gnupg
    key="$(mktemp)"
    curl -fsSL "https://download.docker.com/linux/${ID}/gpg" -o "${key}"
    # awk reads to the end: one that exits early can kill the writer with SIGPIPE, which pipefail
    # turns into a silent stop of this script.
    fpr="$(gpg --show-keys --with-colons "${key}" 2>/dev/null | awk -F: '$1 == "fpr" && !f {print $10; f = 1}')"
    [[ "${fpr}" == "${DOCKER_KEY_FINGERPRINT}" ]] || die "Docker's signing key has the fingerprint ${fpr:-none}, not ${DOCKER_KEY_FINGERPRINT}"
    install -d -m 0755 /etc/apt/keyrings
    install -m 0644 "${key}" /etc/apt/keyrings/docker.asc
    rm -f "${key}"
    echo "deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/${ID} ${VERSION_CODENAME} stable" \
        > /etc/apt/sources.list.d/docker.list
    record file /etc/apt/keyrings/docker.asc
    record file /etc/apt/sources.list.d/docker.list
    DEBIAN_FRONTEND=noninteractive apt-get update -qq
    # The package's version string names the system: 5:29.7.2-1~debian.13~trixie, ~ubuntu.26.04~resolute.
    pkg="$(apt-cache madison docker-ce | awk -v v="${DOCKER_VERSION}" '$3 ~ "^[0-9]+:" v "-" && !f {print $3; f = 1}')"
    [[ -n "${pkg}" ]] || die "Docker's package source has no docker-ce ${DOCKER_VERSION} for ${ID} ${VERSION_CODENAME}"
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
        "docker-ce=${pkg}" "docker-ce-cli=${pkg}" containerd.io docker-buildx-plugin docker-compose-plugin >/dev/null
    systemctl enable --now docker >/dev/null 2>&1
    record docker "installed ${DOCKER_VERSION}"
else
    have="$(docker version --format '{{.Server.Version}}' 2>/dev/null || docker --version | sed -E 's/.* ([0-9.]+).*/\1/')"
    log "Docker present: ${have}"
    record docker "found ${have}"
fi

# ─── 3. Kata Containers with Cloud Hypervisor → /opt/kata-<version> ─────────
# Unpacked beside the Kata that runs, in a folder of its own (13.2, since 0.68): Docker's runtime mvm
# names it only in step 4b, after its proof. A Kata newer than versions.lock is left as it is; an older
# one, 3.31's /opt/kata among them, is moved from.
# shellcheck source=lib/kata-config.sh
source "${SCRIPT_DIR}/lib/kata-config.sh"
KATA_DIR="$(kata_dir "" "${KATA_VERSION}")"
KATA_HAVE="" KATA_HAVE_DIR=""
if r="$(kata_in_use /etc/docker/daemon.json "")"; then read -r KATA_HAVE KATA_HAVE_DIR <<< "${r}"; fi
if [[ -n "${KATA_HAVE}" ]] && kata_older "${KATA_VERSION}" "${KATA_HAVE}"; then
    log "Kata ${KATA_HAVE} runs, newer than ${KATA_VERSION}: left as it is, drop-ins and all"
    record kata "found ${KATA_HAVE}"
    KATA_DIR="${KATA_HAVE_DIR}" KATA_VERSION="${KATA_HAVE}" KATA_NEWER=1
else
    if ! kata_unpacked "${KATA_DIR}" "${KATA_VERSION}"; then
        log "downloading Kata ${KATA_VERSION}"
        KATA_ASSET="kata-static-${KATA_VERSION}-amd64.tar.zst"
        KATA_URL="https://github.com/kata-containers/kata-containers/releases/download/${KATA_VERSION}/${KATA_ASSET}"
        KATA_TMP="$(mktemp)"
        trap 'rm -f "${KATA_TMP}"' EXIT
        curl -fsSL "${KATA_URL}" -o "${KATA_TMP}"
        # Checked against the publisher's SHA-256 before anything is unpacked as root.
        ACTUAL="$(sha256sum "${KATA_TMP}" | awk '{print $1}')"
        [[ "${ACTUAL}" == "${KATA_SHA256}" ]] || die "the Kata archive's checksum is ${ACTUAL}, not ${KATA_SHA256}"
        log "Kata archive checksum verified"
        r="$(kata_unpack "${KATA_TMP}" "${KATA_DIR}" "${KATA_VERSION}")" || die "Kata ${KATA_VERSION}: ${r}"
        rm -f "${KATA_TMP}"
        trap - EXIT
    fi
    log "Kata ${KATA_VERSION} in ${KATA_DIR}"
    record kata "installed ${KATA_VERSION}"
    record kata_dir "${KATA_DIR}"
fi
# Crew's settings of Cloud Hypervisor beside Kata's own file, which is never edited: its paths moved to
# the folder, the sizes of 3.31, memory given back, and no annotation taken. Checked by content on every
# run, so a run again repairs a machine that drifted.
if [[ "${KATA_NEWER:-0}" != 1 ]]; then
    r="$(kata_dropins "${KATA_DIR}")" || die "Kata's drop-ins: ${r}"
    log "Kata's drop-ins in ${KATA_DIR}: ${r}"
fi

# ─── 4. Docker's settings: merged, not replaced ──────────────────────────────
# Docker is restarted only when the file changed. What Crew added or changed is recorded with the
# value it had, so uninstall --with-runtime can put it back.
log "merging /etc/docker/daemon.json"
DAEMON_OUT="$(python3 -I - <<'PYEOF'
import json, os, shutil, tempfile
path = "/etc/docker/daemon.json"
cfg = {}
existed = os.path.exists(path)
if existed:
    with open(path) as f:
        cfg = json.load(f)
want = {
    # The runtime mvm, which a sandbox is started with, is step 4b's.
    # Kata 3.31 refused time namespaces, which Docker 29.5 and later switch on; they stay off.
    "features": {**cfg.get("features", {}), "time-namespaces": False},
    # No IPv6 for a sandbox: the firewall's rules are for IPv4 (13.6).
    "ipv6": False,
    "ip6tables": False,
    # Sandboxes keep running when Docker restarts.
    "live-restore": True,
    # One network per sandbox: without a pool Docker stops at 29 (spike item 6). 172.16.0.0/12 in
    # /24s is 4096 networks, inside a range the firewall drops.
    "default-address-pools": [{"base": "172.16.0.0/12", "size": 24}],
    "log-driver": "json-file",
    "log-opts": {"max-size": "50m", "max-file": "3"},
}
record = {}
for k, v in want.items():
    if cfg.get(k) != v:
        record[k] = cfg.get(k, "<absent>")
        cfg[k] = v
if record:
    if not existed:
        # Crew made the file: uninstall --with-runtime removes it whole.
        record["__file__"] = "<absent>"
    if os.path.exists(path):
        shutil.copy2(path, path + ".bak-crew")
    os.makedirs(os.path.dirname(path), exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=os.path.dirname(path))
    with os.fdopen(fd, "w") as f:
        json.dump(cfg, f, indent=4, sort_keys=True)
    os.chmod(tmp, 0o644)
    os.replace(tmp, path)
print(json.dumps(record, sort_keys=True))
PYEOF
)"
if [[ "${DAEMON_OUT}" != "{}" ]]; then
    record daemon_json "${DAEMON_OUT}"
    log "daemon.json changed (the old file is daemon.json.bak-crew): restarting Docker"
    systemctl restart docker
else
    log "daemon.json already right: Docker is not restarted"
fi

# ─── 4b. The runtime mvm, switched only after the new Kata's proof ───────────
# Docker reads its runtimes again on a reload, with no restart, and containerd keeps each sandbox's own
# shim by the path it started with: sandboxes running on the old Kata go on, and only what starts after
# the switch gets the new one (13.2). The new Kata is first a runtime of its own, mvm-next, which starts
# one microVM of the small size; mvm names it only when that microVM has Kata's own kernel and the
# size Crew counts on. A proof that fails takes mvm-next out again, and mvm still names what ran.
KATA_WANT="$(kata_runtime_json "${KATA_DIR}")"
KATA_SHIM_WANT="${KATA_DIR}/${KATA_SHIM}"
# Docker's info lists the runtimes by name only: it says nothing of a runtime's type (BUG-002). Which
# Kata a runtime starts is read from the shim that runs a microVM of it.
docker_runtimes() { docker info --format '{{json .Runtimes}}' 2>/dev/null; }
has_trial() { docker_runtimes | grep '"mvm-next"' >/dev/null; }
no_trial() { ! docker_runtimes | grep '"mvm-next"' >/dev/null; }
# reload_until CHECK: reloads Docker's settings, and waits until CHECK says its runtimes are as written.
reload_until() {
    systemctl reload docker
    for _ in $(seq 1 30); do "$@" && return 0; sleep 1; done
    return 1
}
# proof_on RUNTIME: one microVM of the small size on RUNTIME, named crew-kata-proof, so one that hangs is
# found and removed. Prints its kernel, its CPUs and its MemTotal in kB, then the path of the shim that
# ran it, and removes it.
proof_on() {
    local id out shim
    docker rm -f crew-kata-proof >/dev/null 2>&1 || true
    if ! id="$(timeout 180 docker run -d --name crew-kata-proof --runtime "$1" --cpus 1 "${PROOF_IMAGE}" sleep 300 2>&1)"; then
        docker rm -f crew-kata-proof >/dev/null 2>&1 || true
        printf '%s\n\n\n%s\n' "$(echo "${id}" | tail -1)" "no shim"
        return 0
    fi
    out="$(timeout 60 docker exec crew-kata-proof sh -c 'uname -r; nproc; sed -n "s/^MemTotal: *\([0-9]*\) kB$/\1/p" /proc/meminfo' 2>&1)" || true
    shim="$(kata_shim_of "${id}" /proc)" || shim="no shim"
    docker rm -f crew-kata-proof >/dev/null 2>&1 || true
    printf '%s\n%s\n' "${out}" "${shim}"
}
# mvm_is_new: whether a microVM of mvm runs on the new Kata's shim now.
mvm_is_new() { [[ "$(proof_on mvm | tail -1)" == "${KATA_SHIM_WANT}" ]]; }
switched() { no_trial && mvm_is_new; }
daemon_runtime() { # NAME JSON|null: sets one runtime in daemon.json, and records what the runtimes were
    local r
    r="$(kata_daemon_runtime /etc/docker/daemon.json "$1" "$2")" || die "daemon.json's runtime $1: ${r}"
    [[ "${r}" == changed* ]] && record daemon_json "${r#changed }"
    return 0
}
docker pull -q "${PROOF_IMAGE}" >/dev/null
mvm_named() { python3 -I -c 'import json,sys
try:
    sys.exit(0 if json.load(open("/etc/docker/daemon.json"))["runtimes"]["mvm"] == json.loads(sys.argv[1]) else 1)
except Exception:
    sys.exit(1)' "${KATA_WANT}"; }
if [[ "${KATA_NEWER:-0}" == 1 ]]; then
    log "Docker's runtime mvm is left as it is, with the newer Kata ${KATA_VERSION}"
elif mvm_named; then
    # Named already, by a run that may have stopped before Docker read it: a reload if it did not.
    mvm_is_new || reload_until switched || die "Docker does not start Kata ${KATA_VERSION} for mvm after a reload: journalctl -u docker"
    log "Docker's runtime mvm already starts Kata ${KATA_VERSION}"
else
    log "trying Kata ${KATA_VERSION} as the runtime mvm-next"
    # trial_out REASON: takes the trial and its microVM out again, and stops; mvm was not touched.
    trial_out() {
        docker rm -f crew-kata-proof >/dev/null 2>&1 || true
        daemon_runtime mvm-next null
        reload_until no_trial || log "WARNING: Docker still lists mvm-next until its next reload"
        die "$1"
    }
    daemon_runtime mvm-next "${KATA_WANT}"
    reload_until has_trial || trial_out "Docker did not take the runtime mvm-next on a reload, so mvm still starts what ran before: journalctl -u docker"
    KATA_OUT="$(proof_on mvm-next)"
    KATA_RAN="$(echo "${KATA_OUT}" | tail -1)" KATA_OUT="$(echo "${KATA_OUT}" | head -3)"
    if ! r="$(kata_proof_check "$(uname -r)" "${KATA_OUT}")"; then
        trial_out "Kata ${KATA_VERSION} did not pass its proof, so mvm still starts what ran before: ${r}: ${KATA_OUT}"
    fi
    if [[ "${KATA_RAN}" != "${KATA_SHIM_WANT}" ]]; then
        trial_out "the proof of mvm-next ran on ${KATA_RAN}, not on ${KATA_SHIM_WANT}, so mvm still starts what ran before"
    fi
    log "Kata ${KATA_VERSION} passed its proof on ${KATA_RAN}: $(echo "${KATA_OUT}" | tr '\n' ' ')"
    daemon_runtime mvm "${KATA_WANT}"
    daemon_runtime mvm-next null
    reload_until switched || die "Docker does not start Kata ${KATA_VERSION} for mvm after the reload: daemon.json names it, and a run again of the host setup reloads it; journalctl -u docker"
    log "Docker's runtime mvm starts Kata ${KATA_VERSION}"
fi
# The folders of an older Kata go once nothing runs from them: a later run of the host setup takes what
# this one had to leave. Only folders install.json lists as Crew's (KATA_OURS), never one a runtime in
# daemon.json still names; 3.31's /opt/kata takes its wrapper with it.
while IFS= read -r old; do
    if r="$(kata_remove "${old}" /proc)"; then
        log "removed ${old}, which nothing runs from"
        record kata_dir_gone "${old%.removing}"
        if [[ "${old%.removing}" == /opt/kata ]]; then rm -f "${KATA_WRAPPER}"; log "removed ${KATA_WRAPPER}"; fi
    else
        log "${old}: ${r}; a later run of the host setup removes it"
        record kata_dir "${old%.removing}"
    fi
done < <(kata_old_dirs "" "${KATA_DIR}" /etc/docker/daemon.json "${KATA_OURS:-}")
if [[ -d /opt/kata && "${KATA_DIR}" != /opt/kata ]] && ! grep -qxF /opt/kata <<< "${KATA_OURS:-}"; then
    log "/opt/kata is not listed as Crew's in install.json: it is left as it is"
fi

# ─── 5. Protection: Docker last in line when memory runs out ─────────────────
# shellcheck source=lib/docker-oom.sh
source "${SCRIPT_DIR}/lib/docker-oom.sh"
DROPIN=/etc/systemd/system/docker.service.d/50-crew-oom.conf
r="$(docker_oom_dropin "${DROPIN}")" || die "dockerd OOM drop-in: ${r}"
log "dockerd's place when memory runs out: ${r}"
if [[ "${r}" == changed ]]; then
    record unit "${DROPIN}"
    systemctl daemon-reload
fi
for pid in $(pidof dockerd 2>/dev/null); do
    echo -999 > "/proc/${pid}/oom_score_adj" || log "WARNING: dockerd (pid ${pid}) gets its place at its next start"
done

# ─── 6. Tuning: KSM ──────────────────────────────────────────────────────────
# Kept on as in styrman-crew; with Cloud Hypervisor it shares nothing (spike item 6).
if [[ -w /sys/kernel/mm/ksm/run ]]; then
    echo 1 > /sys/kernel/mm/ksm/run
    if [[ ! -f /etc/tmpfiles.d/crew-ksm.conf ]]; then
        printf 'w /sys/kernel/mm/ksm/run - - - - 1\nw /sys/kernel/mm/ksm/sleep_millisecs - - - - 100\n' \
            > /etc/tmpfiles.d/crew-ksm.conf
        record kernel_setting /etc/tmpfiles.d/crew-ksm.conf
    fi
    log "KSM on"
fi

# ─── 7. The system user, the folders, and the firewall ───────────────────────
if ! id crew >/dev/null 2>&1; then
    useradd --system --user-group --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin crew
    record user "created crew"
    log "made the system user crew"
else
    record user "found crew"
fi
for d in "${DATA_DIR}" "${DATA_DIR}/db" "${DATA_DIR}/backups" "${DATA_DIR}/caddy" \
         "${DATA_DIR}/caddy/data" "${DATA_DIR}/caddy/config" "${DATA_DIR}/releases" "${DATA_DIR}/sandboxes" \
         "${DATA_DIR}/repos" "${DATA_DIR}/canonical" "${DATA_DIR}/builds" "${DATA_DIR}/hosted"; do
    [[ -L "${d}" ]] && die "${d} is a symlink: refusing to make the data folder through it"
done
install -d -m 0750 -o crew -g crew "${DATA_DIR}" "${DATA_DIR}/releases"
install -d -m 0700 -o crew -g crew "${DATA_DIR}/db" "${DATA_DIR}/backups" "${DATA_DIR}/caddy" \
    "${DATA_DIR}/caddy/data" "${DATA_DIR}/caddy/config" "${DATA_DIR}/sandboxes" "${DATA_DIR}/repos" "${DATA_DIR}/canonical" \
    "${DATA_DIR}/builds" "${DATA_DIR}/hosted"
# The release folders and the updater's are root's: a gate's verdict is believed only in a folder the
# server's user cannot write (16.8). The server reads releases/; crew admin writes the snapshots.
install -d -m 0755 -o root -g root "${DATA_DIR}/releases"
install -d -m 0711 -o root -g root "${DATA_DIR}/updater"
install -d -m 0700 -o crew -g crew "${DATA_DIR}/updater/snapshots"
log "data folder: ${DATA_DIR} (owner crew)"
bash "${SCRIPT_DIR}/firewall/install.sh"
record unit /etc/systemd/system/crew-firewall.service
record unit /etc/systemd/system/crew-firewall.timer
record file /usr/local/sbin/crew-firewall.sh

# ─── 8. Security updates, and the restart they need (16.10) ──────────────────
# The machine's own unattended-upgrades, for the distro's security origin only, never restarting by
# itself; a kernel update writes /var/run/reboot-required, and crew-restart.timer has crew-ctl ask the
# server whether the machine may restart. Each file is checked by content, so a run again repairs it.
# shellcheck source=lib/machine-care.sh
source "${SCRIPT_DIR}/lib/machine-care.sh"
apt_install unattended-upgrades
if [[ "${ID}" == ubuntu ]]; then apt_install update-notifier-common; fi
r="$(machine_care_producer "${ID}" "")" || die "${r}"
log "reboot-required comes from ${r}"
care_file() { # care_file PATH CONTENT KIND: writes it, and records what Crew put there
    local r
    r="$(machine_care_write "$1" "$2")" || die "$1: ${r}"
    log "$1: ${r}"
    record "$3" "$1"
}
care_file /etc/apt/apt.conf.d/52crew-unattended-upgrades "$(machine_care_upgrades_content "${ID}")" file
care_file /etc/apt/apt.conf.d/21crew-auto-upgrades "$(machine_care_periodic_content)" file
care_file /etc/systemd/system/crew-restart.service "$(machine_care_service_content /usr/local/sbin/crew-ctl)" unit
care_file /etc/systemd/system/crew-restart.timer "$(machine_care_timer_content)" unit
systemctl daemon-reload
# Enabled here, started by crew-ctl install once its own crew-ctl, which the service runs, is in place.
systemctl enable crew-restart.timer >/dev/null 2>&1 || die "crew-restart.timer could not be enabled"
log "crew-restart.timer: enabled"

# ─── 9. Proof: one microVM, with Kata's own kernel ───────────────────────────
log "starting one microVM"
GUEST="$(docker run --rm --runtime mvm "${PROOF_IMAGE}" uname -r)"
[[ "${GUEST}" != "$(uname -r)" ]] || die "the microVM has the machine's own kernel: Kata is not in use"
log "a microVM runs kernel ${GUEST}; the machine runs $(uname -r)"
record proof_kernel "${GUEST}"
