#!/usr/bin/env bash
# One case of the workspace-trust check: does agy load a workspace's .agents/hooks.json and
# .agents/mcp_config.json when the workspace is trusted (A) and when it is not (B)?
# Run as agent in a run sandbox with agy's login pulled. Prints one summary line.
set -uo pipefail
case="$1"   # trusted | untrusted
A=~/.gemini/antigravity-cli
mkdir -p ~/work/.agents
rm -rf /tmp/hook-ran "${A}/mcp/ws-marker"
cat > ~/work/.agents/hooks.json <<'EOF'
{"ws-marker": {
  "PreToolUse": [{"matcher": "run_command", "hooks": [{"command": "touch /tmp/hook-ran; echo '{}'"}]}],
  "PreInvocation": [{"type": "command", "command": "touch /tmp/hook-ran; echo '{}'"}]}}
EOF
printf '{"mcpServers":{"ws-marker":{"serverUrl":"http://127.0.0.1:8765/mcp","headers":{"Authorization":"Bearer made-up-bearer"}}}}\n' > ~/work/.agents/mcp_config.json
trusted='["/home/agent/work"]'
[ "$case" = untrusted ] && trusted='[]'
python3 -I - "$A/settings.json" "$trusted" <<'EOF'
import json, sys
p, t = sys.argv[1], json.loads(sys.argv[2])
try:
    d = json.load(open(p))
except Exception:
    d = {}
d.pop("toolPermission", None)
d["permissions"] = {"allow": ["command(*)", "read_file(*)", "write_file(*)", "mcp(*)"]}
d["trustedWorkspaces"] = t
d["useG1Credits"] = False
json.dump(d, open(p, "w"), indent=2)
EOF
python3 -I /tmp/mcp-standin.py 8765 made-up-bearer 2> ~/standin.log &
sp=$!
sleep 1
msg=$(python3 -I -c 'import json;print(json.dumps({"event":"user","message":{"content":"Run ls -a in the shell, then answer with the single word done."}}))')
s=$(date +%s)
( cd ~/work && echo "$msg" | AGY_CLI_DISABLE_AUTO_UPDATE=true timeout 300 agy -p "" --input-format stream-json --output-format stream-json \
    --print-timeout 240s --mode accept-edits --model gemini-3.8-flash ) > ~/"$case".jsonl 2>&1
code=$?
kill $sp 2>/dev/null
echo "# $case exit=$code s=$(( $(date +%s) - s )) hook-ran=$([ -e /tmp/hook-ran ] && echo yes || echo no) mcp-requests=$(grep -c POST ~/standin.log) tool-dir=$(ls "${A}/mcp" 2>/dev/null | tr '\n' ' ') result=$(grep -o '"status":"[A-Z_]*"' ~/"$case".jsonl | tail -1)"
rm -rf ~/work/.agents
