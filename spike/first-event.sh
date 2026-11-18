#!/usr/bin/env bash
# Spike item 5: how long from starting a run's sandbox to the CLI's first event,
# with the harness already in the image. Starts a fresh small sandbox, puts the
# login in, starts one headless turn, and stamps the first line of output.
#
#   first-event.sh agy|claude N
set -uo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
cli="$1" n="$2"
now() { date +%s%N; }
for i in $(seq 1 "$n"); do
    name="fe-${cli}-${i}"
    t0=$(now)
    "${here}/sandbox.sh" start "${name}" small >/dev/null
    t1=$(now)
    h="${here}/raw/homes/${name}"
    if [ "${cli}" = agy ]; then
        install -m 600 "${here}/raw/logins/agy-oauth-token" "${h}/.gemini/antigravity-cli/antigravity-oauth-token"
        cmd='mkdir -p ~/w && cd ~/w && agy -p "Answer with the single word ok." --output-format stream-json --model gemini-3.8-flash-low </dev/null'
    else
        cmd='mkdir -p ~/w && cd ~/w && echo "Answer with the single word ok." | claude -p --output-format stream-json --verbose --model haiku --tools "" --setting-sources user --strict-mcp-config'
    fi
    first="" last=""
    while IFS= read -r line; do
        [ -z "${first}" ] && first=$(now)
        last=$(now)
    done < <(timeout 120 "${here}/sandbox.sh" exec "${name}" bash -c "${cmd}" 2>/dev/null)
    echo "${cli} ${i}: sandbox ready $(( (t1 - t0) / 1000000 )) ms, first event $(( (first - t0) / 1000000 )) ms, turn ended $(( (last - t0) / 1000000 )) ms after the start"
    "${here}/sandbox.sh" rm "${name}" >/dev/null
done
