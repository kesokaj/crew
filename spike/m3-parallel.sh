#!/usr/bin/env bash
# M3's done-when: as many runs at once as the plan carries, ten if it can, stay signed in (design
# 23, M3; section 24, items 1, 35, 67, 72). On dave, with Crew installed and both CLIs signed in.
# CLAUDE runs of claude on MODEL and AGY runs of agy start at once, through Crew's own driver; each
# makes two turns of short work in the fixture. A watcher stops every run if claude's five-hour
# window passes STOP_AT. It ends with the reasons, the time to each run's first event (item 5 with
# Crew's own image), the use, and a login check of each CLI. The microVMs are removed at the end.
#
#   spike/m3-parallel.sh [CLAUDE=10] [AGY=1] [MODEL=haiku] [STOP_AT=0.85]
set -uo pipefail
CLAUDE=${1:-10}
AGY=${2:-1}
MODEL=${3:-haiku}
STOP_AT=${4:-0.85}
OUT=spike/raw/m3-parallel-$(date +%F-%H%M)
mkdir -p "${OUT}"
crew() { sudo docker exec crew-server crew "$@"; }
sql() { sudo docker exec crew-db psql -U crew -d crew -Atc "$1"; }
stamp() { date +%T; }

prompt() {
    echo "You are run $1 of a test of Crew. In this repository: create the file runs/$1.txt holding the line 'run $1', \
run 'go test ./...' and note whether it passed, then commit both changes with the message 'run $1' and push the \
branch with git push. Answer with one line: the result of go test."
}

ids=()
start=$(date +%s)
echo "$(stamp) starting ${CLAUDE} runs of claude on ${MODEL} and ${AGY} of agy" | tee "${OUT}/log.txt"
pids=()
for i in $(seq 1 "${CLAUDE}"); do
    crew admin run start --cli claude --model "${MODEL}" --minimal --timeout 15m --max-turns 60 \
        --title "parallel claude ${i}" --prompt "$(prompt "c${i}")" >/dev/null &
    pids+=($!)
done
for i in $(seq 1 "${AGY}"); do
    crew admin run start --cli agy --model gemini-3.8-flash-low --minimal --timeout 15m --max-turns 60 \
        --title "parallel agy ${i}" --prompt "$(prompt "a${i}")" >/dev/null &
    pids+=($!)
done
wait "${pids[@]}"
# The ids, read back from the database. The titles name this test's start, so an earlier test's
# runs are not counted.
mapfile -t ids < <(sql "select r.id from runs r join subtasks s on s.id = r.subtask where s.title like 'parallel %' \
    and r.started_at > to_timestamp(${start}) order by r.started_at")
printf '%s\n' "${ids[@]}" > "${OUT}/ids.txt"
echo "$(stamp) ${#ids[@]} runs started" | tee -a "${OUT}/log.txt"

# The watcher: claude's highest five-hour use from the rate_limit_event of every run since the start.
watch() {
    while true; do
        u=$(sql "select coalesce(max((payload->'info'->'unifiedWindows'->'five_hour'->>'utilization')::float), 0) from events \
            where type = 'quota' and payload->>'kind' = 'rate_limit' and at > to_timestamp(${start})")
        echo "$(stamp) five-hour use ${u}" >> "${OUT}/use.txt"
        if awk -v u="${u}" -v s="${STOP_AT}" 'BEGIN { exit !(u >= s) }'; then
            echo "$(stamp) five-hour use ${u} reached ${STOP_AT}: stopping every run" | tee -a "${OUT}/log.txt"
            for id in "${ids[@]}"; do crew admin run stop "${id}" >/dev/null 2>&1; done
            return
        fi
        sleep 15
    done
}
watch &
WATCHER=$!

waitall() {
    for id in "${ids[@]}"; do crew admin run wait "${id}" idle ended --timeout 15m >/dev/null 2>&1; done
}
waitall
echo "$(stamp) first turns ended" | tee -a "${OUT}/log.txt"
pids=()
for id in "${ids[@]}"; do
    sub=$(sql "select subtask from runs where id = '${id}'")
    state=$(sql "select state from runs where id = '${id}'")
    if [ "${state}" = idle ]; then
        crew admin run continue "${sub}" --text "Now run 'go vet ./...' and answer with its result in one line. Change nothing." >/dev/null &
        pids+=($!)
    fi
done
# Only these: the watcher runs in the background until it is killed.
wait "${pids[@]}"
sleep 5
waitall
kill "${WATCHER}" 2>/dev/null
echo "$(stamp) second turns ended, after $(( $(date +%s) - start )) s" | tee -a "${OUT}/log.txt"

list=$(printf "'%s'," "${ids[@]}"); list=${list%,}
{
    echo "== the runs"
    sql "select r.cli, r.state, coalesce(r.stop_reason, '-'), r.turn, r.events, r.tool_steps, r.tokens_in, r.tokens_out, coalesce(r.cost_usd, 0)
        from runs r where r.id in (${list}) order by r.cli, r.started_at"
    echo "== by state and reason"
    sql "select cli, state, coalesce(stop_reason, '-'), count(*) from runs where id in (${list}) group by 1, 2, 3 order by 1, 2"
    echo "== seconds from the start of a run to its first event (item 5): min, median, max"
    sql "select r.cli, round(min(f.s)::numeric, 1), round((percentile_cont(0.5) within group (order by f.s))::numeric, 1), round(max(f.s)::numeric, 1)
        from runs r join lateral (select extract(epoch from min(e.at) - r.started_at) s from events e where e.run = r.id) f on true
        where r.id in (${list}) group by r.cli"
    echo "== events and the bytes of their lines (item 43)"
    sql "select r.cli, count(*), sum(length(e.raw)), round(avg(length(e.raw))) from events e join runs r on r.id = e.run
        where r.id in (${list}) group by r.cli"
    echo "== claude's windows at the end"
    sql "select payload->'info'->'unifiedWindows' from events where type = 'quota' and payload->>'kind' = 'rate_limit'
        and at > to_timestamp(${start}) order by at desc limit 1"
    echo "== cost, estimated at list prices"
    sql "select cli, round(sum(cost_usd), 4) from runs where id in (${list}) group by cli"
} | tee -a "${OUT}/log.txt"

echo "$(stamp) the login of each CLI after the test" | tee -a "${OUT}/log.txt"
crew admin login check claude --real | tee -a "${OUT}/log.txt"
crew admin login check agy | tee -a "${OUT}/log.txt"
for id in "${ids[@]}"; do crew admin run finish "${id}" >/dev/null 2>&1; done
echo "$(stamp) the runs are finished, and their microVMs removed" | tee -a "${OUT}/log.txt"
