#!/usr/bin/env python3
"""Sums up a parallel test of spike/parallel.sh (items 1, 14, 26, 35, 68, 72).

  par-analyse.py spike/raw/par-CLI

Prints: calls and their outcomes, the time of a call, the trajectory's bytes
per hour of run time, the guests' memory and CPU from the samples, and for
claude the five-hour and seven-day use from rate_limit_event in time order.
Prints no content of the runs.
"""

import glob
import json
import os
import statistics as st
import sys

d = sys.argv[1]
rows = []
for f in sorted(glob.glob(f"{d}/*/summary.tsv")):
    for line in open(f):
        k, s, e, rc, status = (line.rstrip("\n").split("\t") + [""] * 5)[:5]
        rows.append((os.path.basename(os.path.dirname(f)), k, int(s), int(e), rc, status.strip()))
if not rows:
    sys.exit("no calls found")
sandboxes = sorted({r[0] for r in rows})
outcomes = {}
for r in rows:
    outcomes[r[5]] = outcomes.get(r[5], 0) + 1
durs = [r[3] - r[2] for r in rows]
t0, t1 = min(r[2] for r in rows), max(r[3] for r in rows)
print(f"sandboxes {len(sandboxes)}, calls {len(rows)}, wall time {(t1 - t0) / 60:.1f} min")
print("outcomes:", ", ".join(f"{k or '(none)'} {v}" for k, v in sorted(outcomes.items(), key=lambda x: -x[1])))
print(f"seconds per call: median {st.median(durs):.0f}, min {min(durs)}, max {max(durs)}")
run_s = sum(durs)
b = sum(os.path.getsize(f) for f in glob.glob(f"{d}/*/turn-*.jsonl"))
print(f"trajectory: {b / 1e6:.1f} MB over {run_s / 3600:.2f} hours of run time = {b / 1e6 / (run_s / 3600):.1f} MB per run-hour")

# Limits, in the order they were reported.
ev = []
for f in glob.glob(f"{d}/*/turn-*.jsonl"):
    t = os.path.getmtime(f)
    for line in open(f, errors="replace"):
        if '"rate_limit_event"' in line:
            try:
                i = json.loads(line)["rate_limit_info"]
                w = i.get("unifiedWindows", {})
                ev.append((w.get("five_hour", {}).get("resetsAt"), w.get("five_hour", {}).get("utilization"),
                           w.get("seven_day", {}).get("utilization"), i.get("overageStatus"), i.get("status"), t))
            except (ValueError, KeyError):
                pass
if ev:
    ev.sort(key=lambda x: x[5])
    five = [e[1] for e in ev if e[1] is not None]; seven = [e[2] for e in ev if e[2] is not None]
    print(f"rate_limit_event: {len(ev)}; five_hour lowest {min(five)} highest {max(five)}; "
          f"seven_day lowest {min(seven)} highest {max(seven)}; statuses {sorted({e[4] for e in ev})}; "
          f"overage {sorted({e[3] for e in ev})}; five-hour windows seen {len({e[0] for e in ev})}")

# Results that name a cost (claude) or tokens (both).
cost, tokens = 0.0, 0
for f in glob.glob(f"{d}/*/turn-*.jsonl"):
    for line in open(f, errors="replace"):
        if '"type":"result"' in line or '"event":"result"' in line:
            try:
                j = json.loads(line)
            except ValueError:
                continue
            r = j.get("result") if isinstance(j.get("result"), dict) else j
            cost += r.get("total_cost_usd") or 0
            u = r.get("usage") or {}
            tokens += (u.get("input_tokens") or 0) + (u.get("output_tokens") or 0)
print(f"estimated list-price cost {cost:.2f} USD, input+output tokens {tokens}")

# The guests, from the samples.
mem, cpu = {}, {}
host = []
for line in list(open(f"{d}/samples.tsv"))[1:]:
    p = line.rstrip("\n").split("\t")
    if len(p) < 6:
        continue
    host.append(int(p[2]))
    if p[3]:
        mem.setdefault(p[1], []).append(int(p[3]))
    if p[5]:
        cpu.setdefault(p[1], []).append(int(p[5]))
allm = [v for vs in mem.values() for v in vs]
allc = [v for vs in cpu.values() for v in vs]
if allm:
    print(f"guest memory used MB: median {st.median(allm):.0f}, max {max(allm)} (of 6 GB each)")
if allc:
    print(f"guest CPU busy % (of 4 vCPUs): median {st.median(allc):.0f}, p90 {sorted(allc)[int(len(allc) * .9)]}, max {max(allc)}")
if host:
    print(f"machine's free memory MB: min {min(host)}, max {max(host)}")
