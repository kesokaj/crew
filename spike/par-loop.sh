#!/usr/bin/env bash
# The loop each sandbox of a parallel test runs (spike/parallel.sh), inside the
# sandbox as the user agent. It works through coding tasks, one CLI call each,
# each in a fresh folder, until its deadline or until ~/STOP exists.
#
#   par-loop.sh CLI MODEL EFFORT DEADLINE_EPOCH
# Writes ~/par/turn-NNN.jsonl (the CLI's stream), ~/par/turn-NNN.err, and one
# line per call to ~/par/summary.tsv: n, start, end, exit, status.
set -uo pipefail
cli="$1" model="$2" effort="$3" deadline="$4"
mkdir -p ~/par
tasks=(
  "In this folder, write a Python package textstats with functions word_count(text), top_words(text, n) and flesch_reading_ease(text). Write pytest tests for each, run them with python3 -m pytest (install pytest with pip --user if it is missing), and fix the code until all tests pass. Then add a command-line entry point and a test for it, and make all tests pass again."
  "In this folder, create a Go module example.com/lru with a generic LRU cache (Get, Put, Len, eviction of the least recently used entry) and table-driven tests, including a test with -race. Run go test -race ./... and fix the code until it passes."
  "In this folder, write a Python command-line tool csv2json that reads a CSV file and writes JSON lines, with options for the delimiter and for selecting columns. Write pytest tests with sample files, run them, and fix the code until all tests pass."
  "In this folder, create a Go module example.com/health with an HTTP server that has /healthz and /version endpoints, structured logging with log/slog, and tests that use net/http/httptest. Run go vet and go test ./... and fix everything until both pass."
  "In this folder, write a Python module calc with a parser and evaluator for arithmetic expressions with + - * / and parentheses, respecting precedence, with clear errors for bad input. Write pytest tests, including error cases, run them, and fix the code until all tests pass."
)
n=0
while [ "$(date +%s)" -lt "${deadline}" ] && [ ! -e ~/STOP ]; do
    n=$((n + 1)); k=$(printf '%03d' "$n")
    task="${tasks[$(( (n - 1) % ${#tasks[@]} ))]}"
    d=~/par/work-$k; mkdir -p "$d"; cd "$d" || exit 1
    left=$(( deadline - $(date +%s) )); [ "$left" -gt 60 ] || break
    s=$(date +%s)
    if [ "$cli" = agy ]; then
        python3 -I -c 'import json,sys;print(json.dumps({"event":"user","message":{"content":sys.argv[1]}}))' "$task" |
            timeout $(( left + 30 )) agy -p "" --input-format stream-json --output-format stream-json \
                --mode accept-edits --print-timeout "${left}s" --model "$model" \
                > ~/par/turn-$k.jsonl 2> ~/par/turn-$k.err
        rc=$?
        status=$(grep -h '"event":"result"' ~/par/turn-$k.jsonl | tail -1 | python3 -I -c 'import json,sys
try: r=json.loads(sys.stdin.read())["result"]; print(r.get("status"), (r.get("error") or "")[:80].replace(" ","_"))
except Exception: print("no-result")')
    else
        echo "$task" |
            timeout $(( left + 30 )) claude -p --output-format stream-json --verbose --model "$model" \
                --effort "$effort" --permission-mode bypassPermissions --setting-sources user \
                --strict-mcp-config > ~/par/turn-$k.jsonl 2> ~/par/turn-$k.err
        rc=$?
        status=$(grep -h '"type":"result"' ~/par/turn-$k.jsonl | tail -1 | python3 -I -c 'import json,sys
try: r=json.loads(sys.stdin.read()); print("error" if r.get("is_error") else "ok", (str(r.get("result")) if r.get("is_error") else "")[:80].replace(" ","_"))
except Exception: print("no-result")')
    fi
    printf '%s\t%s\t%s\t%s\t%s\n' "$k" "$s" "$(date +%s)" "$rc" "$status" >> ~/par/summary.tsv
    case "$status" in *RESOURCE_EXHAUSTED*|*quota*|*limit*|*Limit*) touch ~/STOP ;; esac
done
