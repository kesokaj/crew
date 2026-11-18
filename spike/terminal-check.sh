#!/usr/bin/env bash
# Spike item 18: does a terminal in a running run's microVM disturb the CLI,
# and does a tmux session keep what was typed and printed?
# Run inside a spike sandbox as the user agent, with a login for CLI:
#   terminal-check.sh agy|claude
# Writes its findings to ~/item18.txt. Every step has a time limit.
set -u
cli="${1:-agy}"
out=~/item18.txt
: > "${out}"
mkdir -p ~/work18 && cd ~/work18 || exit 1
task='Run this shell command in the foreground and wait for it: for i in $(seq 1 40); do echo tick $i; sleep 1; done. Then answer with the single word finished.'
if [ "${cli}" = agy ]; then
    python3 -I -c 'import json,sys;print(json.dumps({"event":"user","message":{"content":sys.argv[1]}}))' "${task}" |
        timeout 200 agy -p "" --input-format stream-json --output-format stream-json \
            --print-timeout 180s --model gemini-3.8-flash-low > ~/run18.jsonl 2>&1 &
else
    echo "${task}" | timeout 200 claude -p --output-format stream-json --verbose --model haiku \
        --permission-mode bypassPermissions --setting-sources user --strict-mcp-config > ~/run18.jsonl 2>&1 &
fi
run=$!
sleep 12
# The "terminal": a tmux session in the same microVM, as a person would open it.
timeout 10 tmux new-session -d -s term18 -x 200 -y 50
timeout 10 tmux send-keys -t term18 'echo terminal-says-hi; ps -eo pid,etime,comm | grep -E "agy|claude" | head -3; uptime' Enter
sleep 3
echo "--- the screen while the run works:" >> "${out}"
timeout 10 tmux capture-pane -p -t term18 | grep -v '^$' >> "${out}"
wait "${run}"
echo "--- the run: exit $? $(grep -o -E '"(status|result)":"[^"]*"' ~/run18.jsonl | tail -2 | tr '\n' ' ')" >> "${out}"
timeout 10 tmux send-keys -t term18 'echo second-command' Enter
sleep 1
echo "--- scrollback lines kept after the run: $(timeout 10 tmux capture-pane -p -S - -t term18 | grep -c .)" >> "${out}"
timeout 10 tmux kill-session -t term18
echo "--- done" >> "${out}"
