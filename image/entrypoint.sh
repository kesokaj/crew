#!/usr/bin/env bash
# crew-runtime entrypoint — runs as root inside the Kata microVM.
# Responsibilities: guest tuning, user creation (matching host UID/GID),
# per-project DinD on a loop-mounted ext4 image, then park as a reaped
# PID-1 child.
set -uo pipefail

# The user in every sandbox is agent, home /home/agent, made in the image at uid
# 1000 (design 16.11). The server gives it the uid of the system user crew, who
# owns the home on the machine: Kata shares the home with the same owner on both
# sides, so the guest and the server must be the same uid (13.3).
USERNAME="${CREW_USER:-agent}"
UID_="${CREW_UID:-1000}"
GID_="${CREW_GID:-1000}"
PROJECT="${CREW_PROJECT:?CREW_PROJECT not set}"           # project UUID
PROJECT_DIR="${CREW_PROJECT_DIR:-$PROJECT}"                  # workspace dir name
DISK_GB="${CREW_DOCKER_DISK_GB:-20}"
HOME_DIR="/home/${USERNAME}"
export CREW_SANDBOX="${CREW_SANDBOX:-1}"

log() { echo "[crew] $*"; }

# /run is NOT a tmpfs in this container — a marker from the previous boot
# survives docker stop/start and would let waitReady pass while dockerd is
# still coming up. Clear it first thing. harness-done (the
# end of this boot's harness step) is a per-boot marker too.
mkdir -p /run/crew
rm -f /run/crew/ready /run/crew/harness-done

# ─── Guest tuning (Kata mounts /proc/sys and /sys/fs/cgroup read-only) ──────
mount -o remount,rw /proc/sys 2>/dev/null || true
cat /etc/resolv.conf.crew > /etc/resolv.conf 2>/dev/null || true
# No IPv6 in a sandbox (13.6): the machine drops all of it from a sandbox
# bridge, and the guest keeps not even its link-local address.
sysctl -qw net.ipv6.conf.all.disable_ipv6=1 net.ipv6.conf.default.disable_ipv6=1 2>/dev/null || true

# cgroup v2 nesting for DinD (without this dockerd dies on
# 'mkdir /sys/fs/cgroup/init: Read-only file system').
if [ -f /sys/fs/cgroup/cgroup.controllers ]; then
    mount -o remount,rw /sys/fs/cgroup 2>/dev/null || true
    mkdir -p /sys/fs/cgroup/init
    xargs -rn1 < /sys/fs/cgroup/cgroup.procs > /sys/fs/cgroup/init/cgroup.procs 2>/dev/null || :
    sed -e 's/ / +/g' -e 's/^/+/' < /sys/fs/cgroup/cgroup.controllers \
        > /sys/fs/cgroup/cgroup.subtree_control 2>/dev/null || true
fi

# Loop devices for the project docker image mount.
for i in $(seq 0 7); do
    [ -b "/dev/loop$i" ] || mknod -m660 "/dev/loop$i" b 7 "$i" 2>/dev/null || true
done

# ─── User: must match the host owner of the shared home ─────────────────────
# The image's agent is moved to the uid it is given, when no other user has it.
if id "${USERNAME}" &>/dev/null && [ "$(id -u "${USERNAME}")" != "${UID_}" ] && ! getent passwd "${UID_}" >/dev/null; then
    getent group "${GID_}" >/dev/null || groupmod -g "${GID_}" "${USERNAME}"
    usermod -u "${UID_}" "${USERNAME}"
fi
if ! id "${USERNAME}" &>/dev/null; then
    groupadd -g "${GID_}" "${USERNAME}" 2>/dev/null || true
    useradd -u "${UID_}" -g "${GID_}" -M -d "${HOME_DIR}" -s /bin/bash "${USERNAME}"
fi
# A reserved name (root, daemon, …) already exists at a different uid, so
# useradd was skipped and every docker exec would run as the WRONG uid on a
# home owned by ${UID_} — silent, total breakage. Fail loudly instead.
if [ "$(id -u "${USERNAME}")" != "${UID_}" ]; then
    log "FATAL: terminal username '${USERNAME}' maps to uid $(id -u "${USERNAME}"), not ${UID_} — pick a non-reserved name"
    exit 1
fi
echo "${USERNAME} ALL=(ALL) NOPASSWD: ALL" > "/etc/sudoers.d/${USERNAME}"
chmod 440 "/etc/sudoers.d/${USERNAME}"
usermod -aG docker "${USERNAME}" 2>/dev/null || true

# Neither CLI updates itself in a sandbox: a release fixes the version of
# each (design 12.8). The image sets AGY_CLI_DISABLE_AUTO_UPDATE and
# DISABLE_AUTOUPDATER, and /opt/agy stays root's, so agy cannot replace its
# own binary even if it tried.

# Self-heal home ownership: if the home was ever missing at mount time,
# Docker auto-creates the bind source owned by root, which makes every write
# by the guest fail (git config, ssh-keygen, gh). Claim the mount point for
# the guest uid. Non-recursive so existing user files keep their ownership.
chown "${UID_}:${GID_}" "${HOME_DIR}" 2>/dev/null || true

# Minimal mode (system VMs, e.g. the hidden onboarding container): no
# workspace, no DinD — just the user and a shell.
MINIMAL="${CREW_MINIMAL:-}"

# Standard home dirs — the home is now PER-PROJECT and starts empty,
# so the image seeds the skeleton. ~/projects/<dir> is the
# workspace bind mount (mkdir is a no-op on it); ~/.dockerd stays in the home.
# ~/.npm-global/{bin,lib} is the npm prefix skel/bash_exports names
# (NPM_CONFIG_PREFIX), where a user's npm install -g goes; without it
# skel/bashrc left ~/.npm-global/bin off PATH.
# Created on EVERY boot, so homes that predate this get it too.
if [ -z "${MINIMAL}" ]; then
    runuser -u "${USERNAME}" -- mkdir -p \
        "${HOME_DIR}/projects/${PROJECT_DIR}" "${HOME_DIR}/.dockerd" \
        "${HOME_DIR}/.config" "${HOME_DIR}/.cache" "${HOME_DIR}/.local/bin" \
        "${HOME_DIR}/.npm-global/bin" "${HOME_DIR}/.npm-global/lib" \
        2>/dev/null || true
fi
runuser -u "${USERNAME}" -- mkdir -p "${HOME_DIR}/.ssh" 2>/dev/null || true
chmod 700 "${HOME_DIR}/.ssh" 2>/dev/null || true

# Default shell dotfiles from the image, seeded write-once as the
# user. A per-project home starts empty, so this is what makes it a usable
# shell. bash_secrets is 0600. Never clobbers a file the user edited.
if [ -d /crew/skel ]; then
    for f in bashrc profile bash_aliases bash_exports bash_secrets npmrc; do
        dst="${HOME_DIR}/.${f}"
        if [ ! -e "${dst}" ]; then
            runuser -u "${USERNAME}" -- cp "/crew/skel/${f}" "${dst}" 2>/dev/null || true
            [ "${f}" = "bash_secrets" ] && chmod 600 "${dst}" 2>/dev/null || true
        fi
    done
fi

# ─── Login-shell profile.d snippets ─────────────────────────────────────────
# zzz- prefix: the MOTD probes the materialized credentials, so it must
# run AFTER zz-crew-credentials.sh has materialized them (otherwise
# it nags "run crew-setup" on a fully-onboarded project).
cp /crew/configs/profile-motd.sh /etc/profile.d/zzz-crew-motd.sh
rm -f /etc/profile.d/crew-motd.sh
# Credential materialization: every login shell pulls the current
# env/file credentials from the broker on this project's bridge.
cp /crew/configs/crew-credentials.sh /etc/profile.d/zz-crew-credentials.sh

# tmux defaults (hidden status bar, login shells) unless the user has their own.
if [ ! -f "${HOME_DIR}/.tmux.conf" ]; then
    runuser -u "${USERNAME}" -- cp /crew/configs/tmux.conf "${HOME_DIR}/.tmux.conf" 2>/dev/null \
        || log "WARN: could not seed ~/.tmux.conf (home not writable?)"
fi

# ─── The harness, from the image's home skeleton (design 12.1) ─────────────
# The image build wrote the harness into /crew/home-skel, in the place agy
# reads it: ~/.gemini/.
# Nothing is installed when a sandbox starts: the files are copied into the
# home, and a file the home already has is kept. It is a few hundred small
# files, so it runs in front of ready.
HARNESS_OK=1
# A run gets the harness also in a minimal sandbox (no Docker inside): CREW_HARNESS says so. A
# login check or Setup gets none, so a short call does not read the harness's context.
if { [ -z "${MINIMAL}" ] || [ -n "${CREW_HARNESS:-}" ]; } && [ -d /crew/home-skel ]; then
    timeout 60 runuser -u "${USERNAME}" -- cp -a --update=none /crew/home-skel/. "${HOME_DIR}/" \
        || { HARNESS_OK=0; log "WARN: the harness could not be copied into the home (continuing)"; }
fi
# The browser's MCP server, registered with agy's own command, as the user; a
# failure is logged and the boot goes on.
HR_OK=1; HR_FAIL=""
if [ -z "${MINIMAL}" ]; then
    HR_OUT="$(timeout 20 runuser -u "${USERNAME}" -- env HOME="${HOME_DIR}" \
        agy mcp add crew-browser /crew/bin/crew-browser mcp 2>&1 >/dev/null)" \
        || { HR_OK=0; HR_FAIL="agy"; log "  agy mcp add: $(printf '%s' "${HR_OUT}" | tail -1 | tr -cd '[:print:]' | cut -c1-200)"; }
fi

# harness_finish: after /run/crew/ready, reports this boot's harness step and
# writes /run/crew/harness-done last, on every path.
harness_finish() {
    if [ -z "${MINIMAL}" ]; then
        if [ "${HARNESS_OK}" = 1 ] && [ "${HR_OK}" = 1 ]; then
            log "agent harness in place (version $(cat /crew/VERSION 2>/dev/null || echo dev); browser MCP registered for agy)"
        else
            [ "${HARNESS_OK}" = 1 ] || log "WARN: the harness is not in the home"
            [ "${HR_OK}" = 1 ] || log "WARN: browser MCP registration failed for ${HR_FAIL} (continuing)"
        fi
    fi
    touch /run/crew/harness-done
}

# ─── Terminal transcripts (history) ─────────────────────────────────────────
# Every tmux session pipes its output to a log in the project home, so the
# record of what the agents and the operator did outlives the container.
# The path is project-specific, but ~/.tmux.conf is shared across projects —
# hence a per-container include file that the shared config sources.
LOG_DIR="${HOME_DIR}/.crew/logs/${PROJECT}"
# If the log dir can't be made, the pipe-pane hooks below would fail per-pane
# and transcripts would silently stop — warn loudly instead.
runuser -u "${USERNAME}" -- mkdir -p "${LOG_DIR}" || log "WARN: could not create transcript dir ${LOG_DIR} — history will be incomplete"
mkdir -p /etc/crew
cat > /etc/crew/tmux-project.conf <<TEOF
set-hook -g after-new-session 'pipe-pane -o "cat >> ${LOG_DIR}/#{session_name}.log"'
set-hook -g after-new-window 'pipe-pane -o "cat >> ${LOG_DIR}/#{session_name}.log"'
# Track terminal bells (agents ring BEL when waiting for input) so the UI
# can badge unattended terminals; flags clear when the session is viewed.
set -g monitor-bell on
set -g bell-action any
set -g visual-bell off
# OSC 52: let tmux (and TUIs under it) write the system clipboard — the
# browser terminal's clipboard addon picks it up.
set -g set-clipboard on
set -as terminal-features ',xterm-256color:clipboard'
TEOF
# Ensure the shared tmux.conf sources it — only if the file exists (a failed
# cp above must NOT lead to `echo >>` creating a root-owned stub the guest
# can't edit). Append AS THE USER so ownership never flips to root.
if [ -f "${HOME_DIR}/.tmux.conf" ]; then
    if ! grep -q 'crew/tmux-project.conf' "${HOME_DIR}/.tmux.conf" 2>/dev/null; then
        runuser -u "${USERNAME}" -- sh -c \
            "printf '%s\n' 'source-file -q /etc/crew/tmux-project.conf' >> '${HOME_DIR}/.tmux.conf'" 2>/dev/null || true
    fi
    # Migration: earlier defaults shipped 'mouse on', which steals mouse
    # selection from the browser terminal — only touch files still ours.
    if head -1 "${HOME_DIR}/.tmux.conf" 2>/dev/null | grep -q '^# crew default tmux config'; then
        runuser -u "${USERNAME}" -- sed -i 's/^set -g mouse on$/set -g mouse off/' "${HOME_DIR}/.tmux.conf" 2>/dev/null || true
    fi
    # Self-heal: reclaim a ~/.tmux.conf that a previous (buggy) boot created
    # root-owned, so the guest can always read/edit its own config.
    chown "${UID_}:${GID_}" "${HOME_DIR}/.tmux.conf" 2>/dev/null || true
fi

# ─── Per-project Docker daemon ──────────────────────────────────────────────
if [ -n "${MINIMAL}" ]; then
    touch /run/crew/ready
    log "system container ${PROJECT} ready (minimal mode, user=${USERNAME})"
    trap 'exit 0' TERM INT
    sleep infinity &
    wait $!
    exit 0
fi

IMG="${HOME_DIR}/.dockerd/${PROJECT}.img"
if [ ! -f "${IMG}" ]; then
    # Fail fast if the home disk can't hold the image, instead of letting
    # dockerd hit ENOSPC mid-write inside a filesystem that thinks it has room.
    AVAIL_KB=$(df -Pk "${HOME_DIR}/.dockerd" 2>/dev/null | awk 'NR==2{print $4}')
    NEED_KB=$((DISK_GB * 1024 * 1024))
    if [ -n "${AVAIL_KB}" ] && [ "${AVAIL_KB}" -lt "${NEED_KB}" ]; then
        log "WARN: only ${AVAIL_KB}KiB free for a ${DISK_GB}G docker image — DinD may fail on ENOSPC"
    fi
    # Build at a temp path and mv into place only once mkfs succeeded, so an
    # interrupted first boot can't leave a half-made image that silently
    # diverts the next boot to the vfs fallback.
    log "creating ${DISK_GB}G sparse docker image ${IMG}"
    if runuser -u "${USERNAME}" -- truncate -s "${DISK_GB}G" "${IMG}.tmp" && mkfs.ext4 -q "${IMG}.tmp"; then
        mv "${IMG}.tmp" "${IMG}"
    else
        log "WARN: docker image creation failed, removing partial file"
        rm -f "${IMG}.tmp"
    fi
fi
mkdir -p /var/lib/docker-project
# Check the ext4 image before mounting — a dirty journal from an ungraceful
# shutdown (now less likely with the SIGTERM trap) is repaired, not tolerated.
[ -f "${IMG}" ] && fsck.ext4 -p "${IMG}" >/dev/null 2>&1 || true
# Grow the image to the project's Docker disk size: the server
# recreates the container with a larger CREW_DOCKER_DISK_GB, and this boot
# applies it offline, before the mount. Never shrinks. Any failure leaves the
# image as it was (a file grown but not resized still mounts at its old
# size) and the next boot retries — which is why the gate reads the
# FILESYSTEM's size, not the file's: after a truncate that succeeded and a
# resize2fs that did not, the file is already the wanted size and a file
# check would never try again.
if [ -f "${IMG}" ]; then
    HAVE_BYTES=$(stat -c %s "${IMG}" 2>/dev/null || echo 0)
    FS_BYTES=$(dumpe2fs -h "${IMG}" 2>/dev/null | awk -F: '/^Block count:/{c=$2} /^Block size:/{s=$2} END{print (c+0)*(s+0)}')
    WANT_BYTES=$((DISK_GB * 1024 * 1024 * 1024))
    if [ "${FS_BYTES:-0}" -gt 0 ] && [ "${FS_BYTES}" -lt "${WANT_BYTES}" ]; then
        log "growing docker image ${IMG} to ${DISK_GB}G"
        AVAIL_KB=$(df -Pk "${HOME_DIR}/.dockerd" 2>/dev/null | awk 'NR==2{print $4}')
        NEED_KB=$(( (WANT_BYTES - HAVE_BYTES) / 1024 ))
        if [ -n "${AVAIL_KB}" ] && [ "${AVAIL_KB}" -lt "${NEED_KB}" ]; then
            log "WARN: only ${AVAIL_KB}KiB free to grow the docker image by ${NEED_KB}KiB — DinD may fail on ENOSPC"
        fi
        # e2fsck: 0 = clean, 1 = errors corrected; 2 and up = not safe to resize.
        e2fsck -f -p "${IMG}" >/dev/null 2>&1; FSCK_RC=$?
        if [ "${FSCK_RC}" -gt 1 ]; then
            log "WARN: docker image grow to ${DISK_GB}G failed at e2fsck (exit ${FSCK_RC}); keeping the current size"
        elif [ "${HAVE_BYTES}" -lt "${WANT_BYTES}" ] && ! runuser -u "${USERNAME}" -- truncate -s "${DISK_GB}G" "${IMG}"; then
            log "WARN: docker image grow to ${DISK_GB}G failed at truncate; keeping the current size"
        elif ! resize2fs "${IMG}" >/dev/null 2>&1; then
            log "WARN: docker image grow to ${DISK_GB}G failed at resize2fs; the filesystem keeps its old size"
        fi
    fi
fi
if [ -f "${IMG}" ] && mount -o loop "${IMG}" /var/lib/docker-project; then
    STORAGE_DRIVER="overlay2"
    DATA_ROOT="/var/lib/docker-project"
else
    # Fallback: vfs directly on the virtiofs home. Slow, and it uses
    # a DIFFERENT data-root — prior overlay2 images/containers won't be visible
    # (looks like data loss). Surface it loudly + via a marker the UI can read.
    log "WARN: loop mount of ${IMG} FAILED — falling back to slow 'vfs' storage."
    log "WARN: containers/images from the overlay2 image are not visible under vfs."
    STORAGE_DRIVER="vfs"
    DATA_ROOT="${HOME_DIR}/.dockerd/${PROJECT}"
    mkdir -p "${DATA_ROOT}"
    runuser -u "${USERNAME}" -- sh -c "printf 'vfs (loop mount failed)\n' > '${HOME_DIR}/.crew/logs/${PROJECT}/storage-warning.txt'" 2>/dev/null || true
fi

mkdir -p /etc/docker
cat > /etc/docker/daemon.json <<EOF
{
  "data-root": "${DATA_ROOT}",
  "storage-driver": "${STORAGE_DRIVER}",
  "dns": ["8.8.8.8", "1.1.1.1"],
  "ipv6": false,
  "ip6tables": false,
  "bip": "10.64.0.1/16",
  "default-address-pools": [{"base": "10.128.0.0/16", "size": 26}],
  "log-driver": "json-file",
  "log-opts": {"max-size": "10m", "max-file": "2"}
}
EOF
# /run is NOT a tmpfs in this image (it survives stop/start), so an unclean
# shutdown (SIGKILL) leaves stale docker/containerd sockets and pids that
# make the next dockerd wait on a dead containerd until timeout — observed
# as a "dockerd did not become ready" boot loop in prod. Clean slate.
rm -rf /var/run/docker.pid /var/run/docker /var/run/containerd 2>/dev/null || true
# A container's network namespace is bind-mounted under /run/docker/netns, which the container's own
# file system of a microVM refuses ("permission denied"): Docker could build but not run a container.
# A tmpfs takes the bind mounts, and also starts each boot empty.
for d in /run/docker /run/containerd; do
    mkdir -p "${d}"
    mountpoint -q "${d}" || mount -t tmpfs -o mode=0711 tmpfs "${d}" || log "WARN: no tmpfs on ${d}"
done
dockerd > /var/log/dockerd.log 2>&1 &
DOCKERD_PID=$!
DOCKER_UP=false
for i in $(seq 1 60); do
    if docker info &>/dev/null; then
        DOCKER_UP=true
        log "dockerd ready (${STORAGE_DRIVER} on ${DATA_ROOT})"
        break
    fi
    sleep 1
done
if [ "${DOCKER_UP}" != "true" ]; then
    # Exit non-zero so the orchestrator's waitReady fails with a real error
    # instead of handing out a project whose docker is broken.
    log "FATAL: dockerd did not become ready; last log lines:"
    tail -5 /var/log/dockerd.log || true
    exit 1
fi

# ─── Ready (only after everything above actually succeeded) ────────────────
touch /run/crew/ready
log "project ${PROJECT} ready (user=${USERNAME} uid=${UID_})"

# ─── Agent harness, second part ────────────────────────────────────────────
# The file install, behind ready and in the background: the boot does not
# wait on it. A shutdown does not wait on it either — the stamp is its last
# write, so an interrupted install is simply repeated on the next boot.
harness_finish &

# ─── apt persistence ───────────────────────────────────────────────────────
# The rootfs survives stop/start but dies on container REMOVAL (delete,
# username change, image upgrade). Packages the user apt-installed are
# captured as a manifest (delta vs the image's base snapshot) at shutdown
# and replayed here IN THE BACKGROUND after ready — the boot SLA must not
# wait on apt. The manifest lives in the project home, wiped with the project.
APT_DIR="${HOME_DIR}/.crew/apt"
APT_MANIFEST="${APT_DIR}/${PROJECT}.list"
APT_REPLAYING=/run/crew/apt-replaying
if [ -z "${MINIMAL}" ] && [ -s "${APT_MANIFEST}" ]; then
    # Marker: while the replay runs (or after it fails), a shutdown must NOT
    # capture a fresh delta — mid-replay dpkg state would SHRINK the manifest
    # and permanently lose packages.
    touch "${APT_REPLAYING}"
    (
        log "replaying apt manifest ($(wc -l < "${APT_MANIFEST}") packages) — see .crew/apt/${PROJECT}.log"
        export DEBIAN_FRONTEND=noninteractive
        if apt-get update -qq && xargs -a "${APT_MANIFEST}" apt-get install -y -qq --no-install-recommends --; then
            log "apt manifest replay complete"
            rm -f "${APT_REPLAYING}"
        else
            log "WARN: apt manifest replay failed — keeping existing manifest"
        fi
    ) > "${APT_DIR}/${PROJECT}.log" 2>&1 &
fi

capture_apt_manifest() {
    [ -n "${MINIMAL}" ] && return 0
    # Replay still running or failed: the existing manifest is more complete
    # than anything we could compute now — keep it.
    [ -e "${APT_REPLAYING}" ] && return 0
    runuser -u "${USERNAME}" -- mkdir -p "${APT_DIR}" 2>/dev/null || return 0
    # Delta vs the image's base package snapshot = what the user installed.
    # (apt REMOVALS of base packages are not recorded.)
    if dpkg-query -W -f='${Package}\n' 2>/dev/null | sort \
        | comm -13 /etc/crew-base-packages - > "${APT_MANIFEST}.tmp" 2>/dev/null; then
        mv "${APT_MANIFEST}.tmp" "${APT_MANIFEST}" 2>/dev/null || true
        chown "${UID_}:${GID_}" "${APT_MANIFEST}" 2>/dev/null || true
    else
        rm -f "${APT_MANIFEST}.tmp" 2>/dev/null || true
    fi
}

# Graceful shutdown: on `docker stop`, tini forwards SIGTERM here. Stop
# dockerd cleanly (so nested containers get their grace period and overlay2
# isn't torn down mid-write) and unmount the loop-mounted ext4 so its journal
# is clean on the next boot. Do NOT `exec` — the shell must stay alive to run
# the trap. tini (PID 1) still reaps exec'd session zombies.
shutdown() {
    log "shutting down project ${PROJECT}"
    capture_apt_manifest
    if [ -n "${DOCKERD_PID:-}" ]; then
        kill -TERM "${DOCKERD_PID}" 2>/dev/null || true
        wait "${DOCKERD_PID}" 2>/dev/null || true
    fi
    umount /var/lib/docker-project 2>/dev/null || true
    exit 0
}
trap shutdown TERM INT
sleep infinity &
wait $!
