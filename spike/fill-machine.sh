#!/usr/bin/env bash
# Spike item 6: how many idle run microVMs fit on dave at the sizes of design
# 14.1, with the Docker daemon inside off (small) and on (large).
#
# Starts sandboxes one at a time and reads MemAvailable after each has settled.
# It stops before the machine's free memory would fall under the floor, at the
# count given, or when a start fails, and then removes every sandbox it made.
#
#   fill-machine.sh small|large MAX [FLOOR_GB]      (FLOOR_GB defaults to 16)
set -uo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
kind="$1" max="$2" floor_kb=$(( ${3:-16} * 1024 * 1024 ))
avail() { awk '/^MemAvailable:/ {print $2}' /proc/meminfo; }
log="${here}/raw/item-6-fill-${kind}.tsv"
made=()
cleanup() { for n in "${made[@]}"; do "${here}/sandbox.sh" rm "$n" >/dev/null; done; }
trap cleanup EXIT

base=$(avail)
printf 'n\tavail_kb\tused_by_sandboxes_kb\tper_sandbox_kb\tstart_s\n' > "${log}"
printf '0\t%s\t0\t0\t0\n' "${base}" >> "${log}"
last_step=0
for i in $(seq 1 "${max}"); do
    a=$(avail)
    # Stop before the next one could cross the floor: assume it costs twice the last step.
    if [ $(( a - 2 * last_step )) -lt "${floor_kb}" ]; then
        echo "stop before ${i}: available ${a} kB, floor ${floor_kb} kB"; break
    fi
    n="fill-${kind}-${i}"
    t0=${SECONDS}
    if [ "${kind}" = small ]; then
        "${here}/sandbox.sh" start "$n" small minimal >/dev/null || { echo "start ${i} failed"; break; }
    else
        "${here}/sandbox.sh" start "$n" large >/dev/null || { echo "start ${i} failed"; break; }
    fi
    made+=("$n")
    sleep 10   # let the guest settle: dockerd starts, pages are shared
    now=$(avail)
    last_step=$(( a - now ))
    printf '%s\t%s\t%s\t%s\t%s\n' "$i" "${now}" $(( base - now )) $(( (base - now) / i )) $(( SECONDS - t0 - 10 )) >> "${log}"
done
column -t "${log}" | tail -4
echo "sandboxes started: ${#made[@]}; KSM pages shared: $(cat /sys/kernel/mm/ksm/pages_sharing)"
