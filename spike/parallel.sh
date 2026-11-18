#!/usr/bin/env bash
# Spike items 1, 2, 14, 17, 26, 35, 50, 54 (agy) and 68, 72 (claude): many runs
# of one CLI at once, each in its own large sandbox, for a set time.
#
#   parallel.sh CLI N MINUTES MODEL EFFORT [UTIL_STOP]
#
# Each sandbox runs par-loop.sh until the deadline. Every 30 seconds the
# machine's free memory and each guest's memory, load, and CPU are sampled.
# For claude, the five-hour use is read from the newest rate_limit_event, and
# every loop is stopped when it reaches UTIL_STOP (default 0.85), so the test
# never runs the subscription into its limit. For agy, a loop stops itself at
# a limit. Results: spike/raw/par-CLI/.
set -uo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
cli="$1" n="$2" minutes="$3" model="$4" effort="$5" util_stop="${6:-0.85}"
out="${here}/raw/par-${cli}"
mkdir -p "${out}"
deadline=$(( $(date +%s) + minutes * 60 ))
names=(); for i in $(seq 1 "$n"); do names+=("par-${cli}-${i}"); done

for nm in "${names[@]}"; do
    "${here}/sandbox.sh" start "${nm}" large >/dev/null || { echo "start ${nm} failed"; exit 1; }
    h="${here}/raw/homes/${nm}"
    cp "${here}/par-loop.sh" "${h}/par-loop.sh"
    if [ "${cli}" = agy ]; then
        install -m 600 "${here}/raw/logins/agy-oauth-token" "${h}/.gemini/antigravity-cli/antigravity-oauth-token"
        python3 -I - "${h}/.gemini/antigravity-cli/settings.json" <<'EOF'
import json, sys
json.dump({"permissions": {"allow": ["command(*)", "read_file(*)", "write_file(*)", "mcp(*)"]},
           "trustedWorkspaces": ["/home/agent/par"]}, open(sys.argv[1], "w"), indent=2)
EOF
    fi
done
echo "$(date +%T) ${n} sandboxes ready; deadline $(date -d @"${deadline}" +%T)"

export CLAUDE_CODE_OAUTH_TOKEN="${CLAUDE_CODE_OAUTH_TOKEN:-}"
for nm in "${names[@]}"; do
    sudo --preserve-env=CLAUDE_CODE_OAUTH_TOKEN docker exec -d -u agent -w /home/agent -e HOME=/home/agent \
        ${CLAUDE_CODE_OAUTH_TOKEN:+-e CLAUDE_CODE_OAUTH_TOKEN} "crew-spike-${nm}" \
        bash /home/agent/par-loop.sh "${cli}" "${model}" "${effort}" "${deadline}"
done

samples="${out}/samples.tsv"
printf 'time\tsandbox\thost_avail_mb\tguest_used_mb\tload1\tcpu_busy_pct\tfive_hour_util\n' > "${samples}"
declare -A prev_busy prev_total
util=""
while :; do
    running=0
    host=$(awk '/^MemAvailable:/ {print int($2/1024)}' /proc/meminfo)
    if [ "${cli}" = claude ]; then
        util=$(cat "${here}"/raw/homes/par-claude-*/par/turn-*.jsonl 2>/dev/null | grep '"rate_limit_event"' | tail -1 |
            python3 -I -c 'import json,sys
try: print(json.loads(sys.stdin.read())["rate_limit_info"]["unifiedWindows"]["five_hour"]["utilization"])
except Exception: print("")')
        if [ -n "${util}" ] && python3 -I -c "import sys; sys.exit(0 if ${util} >= ${util_stop} else 1)"; then
            echo "$(date +%T) five-hour use ${util} reached ${util_stop}: stopping every loop"
            for nm in "${names[@]}"; do touch "${here}/raw/homes/${nm}/STOP"; done
        fi
    fi
    for nm in "${names[@]}"; do
        read -r used load busy total alive < <(sudo docker exec "crew-spike-${nm}" sh -c \
            'awk "/MemTotal/{t=\$2}/MemAvailable/{a=\$2}END{printf \"%d \", (t-a)/1024}" /proc/meminfo;
             cut -d" " -f1 /proc/loadavg | tr "\n" " ";
             awk "/^cpu /{b=\$2+\$3+\$4+\$7+\$8; t=b+\$5+\$6; printf \"%d %d \", b, t}" /proc/stat;
             pgrep -f "^bash /home/agent/par-loop.sh" >/dev/null && echo 1 || echo 0' 2>/dev/null)
        pct=""
        if [ -n "${prev_total[$nm]:-}" ] && [ "${total:-0}" -gt "${prev_total[$nm]}" ]; then
            pct=$(( 100 * (busy - prev_busy[$nm]) / (total - prev_total[$nm]) ))
        fi
        prev_busy[$nm]=${busy:-0}; prev_total[$nm]=${total:-0}
        [ "${alive:-0}" = 1 ] && running=$((running + 1))
        printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$(date +%s)" "${nm}" "${host}" "${used:-}" "${load:-}" "${pct}" "${util}" >> "${samples}"
    done
    [ "${running}" -gt 0 ] || break
    sleep 30
done
echo "$(date +%T) every loop has ended"
for nm in "${names[@]}"; do
    mkdir -p "${out}/${nm}"
    cp "${here}/raw/homes/${nm}/par/summary.tsv" "${out}/${nm}/" 2>/dev/null
    cp "${here}"/raw/homes/"${nm}"/par/turn-*.jsonl "${here}"/raw/homes/"${nm}"/par/turn-*.err "${out}/${nm}/" 2>/dev/null
    if [ "${cli}" = agy ]; then
        sha256sum < "${here}/raw/homes/${nm}/.gemini/antigravity-cli/antigravity-oauth-token" > "${out}/${nm}/token.sha256"
        cp "${here}/raw/homes/${nm}/.gemini/antigravity-cli/antigravity-oauth-token" "${out}/${nm}/token.json"
    fi
done
echo "results in ${out}"
