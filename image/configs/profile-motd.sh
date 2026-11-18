# shellcheck shell=sh
# Crew: say when the broker did not answer this shell (crew-credentials.sh).
# Sourced from /etc/profile.d by every login shell (no shebang on purpose).
if [ "$(id -u)" != "0" ] && [ -z "${CREW_MOTD_SHOWN:-}" ] && [ -t 1 ]; then
    export CREW_MOTD_SHOWN=1
    # Written by zz-crew-credentials.sh (which runs first) when the broker
    # did not answer: the sandbox's credentials are then missing from THIS
    # shell — say so instead of letting git fail later
    # (a silent outage once lasted three days).
    # The marker is guest-written: printable bytes only, bounded, so a planted
    # file cannot push terminal control sequences at the operator.
    if [ -f "$HOME/.crew/broker-unreachable" ]; then
        echo "· credentials: the broker did not answer ($(tr -cd '[:print:]' < "$HOME/.crew/broker-unreachable" 2>/dev/null | cut -c1-200)) — the sandbox's credentials are not in this shell; open a new terminal to retry."
    fi
fi
