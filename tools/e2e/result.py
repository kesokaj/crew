#!/usr/bin/env python3
"""Says whether a CLI's stream-json output ends its turn with a result that is not an error.

  result.py agy FILE     exit 0 when the last result is a success, 1 otherwise, and prints it
The event shape is agy's result with its "status" (design 12.2). A turn's result is the last one, not
the first (12.4).
"""
import json
import sys

cli, path = sys.argv[1], sys.argv[2]
last = None
for line in open(path, encoding="utf-8", errors="replace"):
    line = line.strip()
    if not line.startswith("{"):
        continue
    try:
        ev = json.loads(line)
    except ValueError:
        continue
    kind = ev.get("type") or ev.get("event")
    if kind == "result":
        # agy keeps the result's fields under "result".
        last = ev["result"] if isinstance(ev.get("result"), dict) else ev
if last is None:
    print("no result event")
    sys.exit(1)
status = str(last.get("status", "")).upper()
ok = status in ("SUCCESS", "OK", "COMPLETED", "DONE")
print(cli, "result:", status, "turns", last.get("num_turns"), "seconds", last.get("duration_seconds"))
sys.exit(0 if ok else 1)
