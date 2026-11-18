# shellcheck shell=sh
# Crew: a login shell in a sandbox pulls the sandbox's credentials from the broker (design 12.6).
# crew-run creds does the work, with the sandbox's token from ~/.crew/token: files go into the home,
# and values into ~/.crew/creds.env, which this shell then reads. The broker answers only on the
# sandbox's own network, with its own token. Sourced by every login shell, so it is quiet and never
# fatal; a failure leaves a marker that profile-motd.sh reports.
__sc_marker="${HOME}/.crew/broker-unreachable"
if [ -n "${CREW_CRED_BROKER:-}" ] && [ -r "${HOME}/.crew/token" ] && command -v crew-run >/dev/null 2>&1; then
    if crew-run creds --quiet 2>/dev/null; then
        rm -f "${__sc_marker}" 2>/dev/null || true
    else
        printf '%s %s\n' "$(date -u +%FT%TZ 2>/dev/null || echo unknown)" "${CREW_CRED_BROKER}" > "${__sc_marker}" 2>/dev/null || true
    fi
    # shellcheck disable=SC1091
    [ -r "${HOME}/.crew/creds.env" ] && . "${HOME}/.crew/creds.env"
fi
unset ANTHROPIC_API_KEY
unset __sc_marker
