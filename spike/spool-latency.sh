#!/usr/bin/env bash
# Spike items 12 and 53: is a spool in the shared home read reliably from the
# machine while the CLI writes it, and how late do its lines arrive?
#
# In the sandbox, every line `agy` prints is written to ~/spool/events.jsonl
# with the guest's time in front of it. On the machine, `tail -F` reads the
# same file through the shared folder and puts the machine's time in front.
# The offset between the two clocks is measured first and taken off.
#
#   spool-latency.sh NAME      NAME is a running spike sandbox with an agy login
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
name="$1"
home="${here}/raw/homes/${name}"
out="${here}/raw/item-12-spool"
mkdir -p "${out}" "${home}/spool"
: > "${home}/spool/events.jsonl"

# Clock offset: the guest's time minus the machine's, from five samples.
offset_ns=$(for _ in 1 2 3 4 5; do
    a=$(date +%s%N); g=$("${here}/sandbox.sh" exec "${name}" date +%s%N); b=$(date +%s%N)
    echo $(( g - (a + b) / 2 ))
done | sort -n | sed -n 3p)
echo "clock offset guest-host: ${offset_ns} ns (includes half of docker exec's own delay)"

tail -n +1 -F "${home}/spool/events.jsonl" 2>/dev/null | while IFS= read -r line; do
    printf '%s\t%s\n' "$(date +%s%N)" "${line}"
done > "${out}/host-seen.tsv" &
tailer=$!

prompt='Run this shell command and wait for it to finish: for i in $(seq 1 8); do echo tick $i; sleep 1; done. Then answer with the single word done.'
msg=$(python3 -I -c 'import json,sys;print(json.dumps({"event":"user","message":{"content":sys.argv[1]}}))' "${prompt}")
"${here}/sandbox.sh" exec "${name}" bash -c '
    cd ~/work
    printf "%s\n" "$1" | agy -p "" --input-format stream-json --output-format stream-json \
        --mode accept-edits --print-timeout 300s --model gemini-3.8-flash-low |
    while IFS= read -r l; do printf "%s\t%s\n" "$(date +%s%N)" "$l" >> ~/spool/events.jsonl; done' _ "${msg}"
sleep 3
kill "${tailer}" 2>/dev/null || true
cp "${home}/spool/events.jsonl" "${out}/guest-written.tsv"

python3 -I - "${out}" "${offset_ns}" <<'EOF'
import sys
out, off = sys.argv[1], int(sys.argv[2])
g = [l.rstrip("\n").split("\t", 1) for l in open(f"{out}/guest-written.tsv")]
h = [l.rstrip("\n").split("\t", 2) for l in open(f"{out}/host-seen.tsv")]
seen = {}
for hs, gs, line in (x for x in h if len(x) == 3):
    seen.setdefault((gs, line), int(hs))
lat = []
for gs, line in g:
    if (gs, line) in seen:
        lat.append((seen[(gs, line)] - (int(gs) - off)) / 1e6)
print(f"lines written {len(g)}, seen on the machine {len(lat)}, lost {len(g) - len(lat)}")
if lat:
    lat.sort()
    print(f"delay ms: min {lat[0]:.0f}, median {lat[len(lat)//2]:.0f}, max {lat[-1]:.0f}")
EOF
