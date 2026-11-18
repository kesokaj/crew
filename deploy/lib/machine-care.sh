# shellcheck shell=bash
# deploy/lib/machine-care.sh — the machine's security updates and the restart they need (design
# 16.10), as functions sourced by host-setup.sh. Paths are arguments, so deploy/lib/machine-care-test.sh
# points them at a scratch dir; each writer prints `changed` or `unchanged`, or the reason and a
# non-zero status.
#
# The machine's own unattended-upgrades installs the distro's security updates, and nothing else:
# not its point releases, and never Docker's packages (Kata is no package). It never restarts the
# machine itself. A kernel update writes /var/run/reboot-required, through the unattended-upgrades
# package's kernel hook on Debian and update-notifier-common's on Ubuntu; crew-ctl restart-if-quiet,
# from crew-restart.timer, reads it and asks the server, which says when the machine may restart.

# machine_care_upgrades_content ID: the settings of unattended-upgrades for the distro ID (debian or
# ubuntu). apt reads /etc/apt/apt.conf.d in order of the names and adds to a list, so the distro's
# own origins in 50unattended-upgrades are cleared first: what is left is the security origin.
machine_care_upgrades_content() {
    local pattern
    case "$1" in
        # The security archive of Debian: origin Debian, label Debian-Security, codename trixie-security.
        debian) pattern='origin=Debian,codename=${distro_codename}-security,label=Debian-Security' ;;
        # Ubuntu's security pocket: origin Ubuntu, label Ubuntu, archive (suite) resolute-security.
        ubuntu) pattern='origin=Ubuntu,archive=${distro_codename}-security,label=Ubuntu' ;;
        *) echo "machine-care: $1 is neither debian nor ubuntu" >&2; return 1 ;;
    esac
    cat <<CONF
// crew (16.10): the distro's security updates only, and no restart by unattended-upgrades itself:
// crew-ctl restarts the machine when Crew's server says no run is going. Written by host-setup.sh;
// edits here are overwritten.
#clear Unattended-Upgrade::Allowed-Origins;
#clear Unattended-Upgrade::Origins-Pattern;
Unattended-Upgrade::Origins-Pattern {
        "${pattern}";
};
Unattended-Upgrade::Package-Blacklist {
        "docker-ce";
        "docker-ce-cli";
        "docker-ce-rootless-extras";
        "containerd.io";
        "docker-buildx-plugin";
        "docker-compose-plugin";
};
Unattended-Upgrade::Automatic-Reboot "false";
CONF
}

# machine_care_periodic_content: apt's daily timer looks for updates and runs unattended-upgrades.
# A later file than the distro's 20auto-upgrades, so these values win.
machine_care_periodic_content() {
    cat <<'CONF'
// crew (16.10): apt's daily timer reads the package lists and runs unattended-upgrades. Written by
// host-setup.sh; edits here are overwritten.
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
CONF
}

# machine_care_service_content CREW_CTL: the unit crew-restart.timer starts.
machine_care_service_content() {
    cat <<UNIT
# crew: asks Crew's server whether the machine may restart for security updates, and restarts it on
# a yes (16.10). Written by host-setup.sh; edits here are overwritten.
[Unit]
Description=Crew: restart the machine for security updates when Crew's server says it is quiet
After=docker.service
Requisite=docker.service

[Service]
Type=oneshot
ExecStart=$1 restart-if-quiet
UNIT
}

# machine_care_timer_content: every 15 minutes, and 10 minutes after the machine started, so a restart
# that happened clears what the server kept of it.
machine_care_timer_content() {
    cat <<'UNIT'
# crew: every 15 minutes crew-ctl asks Crew's server whether the machine may restart for security
# updates (16.10). Written by host-setup.sh; edits here are overwritten.
[Unit]
Description=Crew: ask every 15 minutes whether the machine may restart for security updates

[Timer]
OnBootSec=10min
OnUnitActiveSec=15min

[Install]
WantedBy=timers.target
UNIT
}

# machine_care_write PATH CONTENT: puts CONTENT at PATH as a regular file, 0644, unless it is there
# already; a link at PATH is replaced, never followed.
machine_care_write() {
    local path="$1" want="$2" dir tmp
    dir="$(dirname "${path}")"
    if [[ -f "${path}" && ! -L "${path}" && "$(cat "${path}")" == "${want}" ]]; then
        echo unchanged
        return 0
    fi
    mkdir -p "${dir}" || { echo "failed: mkdir ${dir}"; return 1; }
    tmp="$(mktemp "${path}.XXXXXX")" || { echo "failed: mktemp beside ${path}"; return 1; }
    printf '%s\n' "${want}" > "${tmp}" && chmod 0644 "${tmp}" && mv -f "${tmp}" "${path}" \
        || { rm -f "${tmp}"; echo "failed: writing ${path}"; return 1; }
    echo changed
}

# machine_care_producer ID ROOT: the file that writes ROOT/var/run/reboot-required after a kernel
# update, as the distro ID's packages ship it under ROOT ("" on the machine), or the reason and 1.
machine_care_producer() {
    local p
    case "$1" in
        debian) p="$2/etc/kernel/postinst.d/unattended-upgrades" ;;
        ubuntu) p="$2/etc/kernel/postinst.d/update-notifier" ;;
        *) echo "$1 is neither debian nor ubuntu"; return 1 ;;
    esac
    if [[ -f "${p}" && -x "${p}" ]] && grep -q reboot-required "${p}"; then
        echo "${p}"
        return 0
    fi
    echo "no ${p} that writes reboot-required: a kernel update would not ask for its restart"
    return 1
}
