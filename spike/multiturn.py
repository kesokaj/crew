#!/usr/bin/env python3
"""Spike item 39: several turns in one open process, over stream-json.

Run inside a spike sandbox as the user agent:  multiturn.py agy|claude
Sends a first message, waits for its result, sends a second, waits again,
then closes standard input. Prints the time of each turn and what the second
turn's result says about usage, turns, and cost.
"""

import json
import subprocess
import sys
import time

cli = sys.argv[1]
if cli == "agy":
    cmd = ["agy", "-p", "", "--input-format", "stream-json", "--output-format", "stream-json",
           "--print-timeout", "120s", "--model", "gemini-3.8-flash-low"]
    msg = lambda t: {"event": "user", "message": {"content": t}}
    is_result = lambda d: d.get("event") == "result"
    body = lambda d: d["result"]
else:
    cmd = ["claude", "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
           "--model", "haiku", "--tools", "", "--setting-sources", "user", "--strict-mcp-config"]
    msg = lambda t: {"type": "user", "message": {"role": "user", "content": t}}
    is_result = lambda d: d.get("type") == "result"
    body = lambda d: d

p = subprocess.Popen(cmd, stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
turns = ["Remember the word lantern. Answer only with ok.",
         "Which word did I ask you to remember? Answer with the word only."]
for i, t in enumerate(turns, 1):
    t0 = time.monotonic()
    p.stdin.write(json.dumps(msg(t)) + "\n")
    p.stdin.flush()
    for line in p.stdout:
        try:
            d = json.loads(line)
        except ValueError:
            continue
        if is_result(d):
            r = body(d)
            ans = r.get("response") if cli == "agy" else r.get("result")
            print(f"turn {i}: {time.monotonic() - t0:.1f} s, answer {ans!r}, "
                  f"num_turns {r.get('num_turns')}, usage {r.get('usage', {}).get('output_tokens')} out, "
                  f"cost {r.get('total_cost_usd')}")
            break
p.stdin.close()
print(f"exit {p.wait(timeout=60)}")
