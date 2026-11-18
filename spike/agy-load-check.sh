#!/usr/bin/env bash
# Does agy load hooks or MCP servers from a workspace by other ways than the top .agents/hooks.json
# and .agents/mcp_config.json, which crew-run already takes out of a run that does not build (13.8)?
# Asked before M6 lets a run that judges use agy (handoff of M5's follow-ups, open item 1).
#
# Every way gets its own marker: a hook that leaves /tmp/hook-<way>, and an MCP server named m-<way>
# at http://127.0.0.1:8765/mcp/<way> (the stand-in logs the path of every request).
#
#   paths     one turn over a workspace with: a skill and an agent that declare a hook and a server in
#             their front matter, a plugin under .agents/plugins/, a plugin outside .agents/ named by
#             .agents/plugins.json, and the top files and a plugin in a nested sub/.agents/
#   midturn   one turn in which the agent copies a prepared .agents/ (top files and a plugin) into the
#             workspace, then runs a command: does what it wrote load in the same turn?
#   nextturn  the next turn of midturn's conversation, after taking out the top files as crew-run does
#             today: does the plugin it wrote load in the turn after?
#
# Run as agent in a run sandbox with agy's login pulled (crew-run creds), with mcp-standin.py copied to
# /tmp. Prints one summary line per case.
set -uo pipefail
A=~/.gemini/antigravity-cli
W=~/work
out=~/load-check
mkdir -p "$out" "$W"

hook() {  # hook NAME: a hooks.json whose hook leaves /tmp/hook-NAME
    # A PreToolUse hook answers with its decision; anything else denies the call (docs/hooks.md).
    python3 -I -c 'import json,sys; n=sys.argv[1]; print(json.dumps({"h-"+n: {
        "PreToolUse": [{"matcher": "run_command", "hooks": [{"command": "touch /tmp/hook-%s; echo %s" % (n, "\x27{\"decision\": \"allow\"}\x27")}]}],
        "PreInvocation": [{"type": "command", "command": "touch /tmp/hook-%s; echo {}" % n}]}}))' "$1"
}
server() {  # server NAME: an mcp_config.json with the server m-NAME
    printf '{"mcpServers":{"m-%s":{"serverUrl":"http://127.0.0.1:8765/mcp/%s","headers":{"Authorization":"Bearer made-up-bearer"}}}}\n' "$1" "$1"
}
plugin() {  # plugin DIR NAME: a plugin with its own hook and server
    mkdir -p "$1"
    printf '{"name": "p-%s"}\n' "$2" > "$1/plugin.json"
    hook "$2" > "$1/hooks.json"
    server "$2" > "$1/mcp_config.json"
}
frontmatter() {  # frontmatter NAME: front matter that declares a hook and a server, in the shape claude reads
    cat <<EOF
---
name: m-$1
description: Marker $1. Use it when asked to use m-$1.
hooks:
  PreToolUse:
    - matcher: run_command
      hooks:
        - type: command
          command: touch /tmp/hook-$1; echo '{"decision": "allow"}'
mcpServers:
  m-$1:
    serverUrl: http://127.0.0.1:8765/mcp/$1
    headers:
      Authorization: Bearer made-up-bearer
---
Answer with the word marker-$1.
EOF
}

settings() {
    python3 -I - "$A/settings.json" <<'EOF'
import json, sys
p = sys.argv[1]
try:
    d = json.load(open(p))
except Exception:
    d = {}
d.pop("toolPermission", None)
d["permissions"] = {"allow": ["command(*)", "read_file(*)", "write_file(*)", "mcp(*)"]}
d["trustedWorkspaces"] = ["/home/agent/work"]
d["useG1Credits"] = False
json.dump(d, open(p, "w"), indent=2)
EOF
}

reset() {
    rm -f /tmp/hook-*
    rm -rf "$A/mcp"
    : > "$out/standin.log"
}

turn() {  # turn NAME TEXT [CONVERSATION]
    local name=$1 text=$2 conv=${3:-} msg s code sp
    python3 -I /tmp/mcp-standin.py 8765 made-up-bearer 2>> "$out/standin.log" &
    sp=$!
    sleep 1
    msg=$(python3 -I -c 'import json,sys;print(json.dumps({"event":"user","message":{"content":sys.argv[1]}}))' "$text")
    s=$(date +%s)
    ( cd "$W" && echo "$msg" | AGY_CLI_DISABLE_AUTO_UPDATE=true timeout 300 agy -p "" ${conv:+--conversation "$conv"} \
        --input-format stream-json --output-format stream-json --print-timeout 240s --mode accept-edits \
        --model gemini-3.8-flash ) > "$out/$name.jsonl" 2>&1
    code=$?
    kill "$sp" 2>/dev/null
    wait "$sp" 2>/dev/null
    echo "# $name exit=$code s=$(( $(date +%s) - s )) hooks-ran=[$(ls /tmp/hook-* 2>/dev/null | sed 's|/tmp/hook-||' | tr '\n' ' ')]" \
        "mcp-paths=[$(grep -o 'POST /mcp/[a-z-]*' "$out/standin.log" | sort | uniq -c | awk '{print $3"x"$1}' | sed 's|/mcp/||' | tr '\n' ' ')]" \
        "tool-dirs=[$(ls "$A/mcp" 2>/dev/null | tr '\n' ' ')] result=$(grep -o '"status":"[A-Z_]*"' "$out/$name.jsonl" | tail -1)"
}

case_paths() {
    rm -rf "$W/.agents" "$W/sub" "$W/tools"
    mkdir -p "$W/.agents/skills/m-skill" "$W/.agents/agents/m-agent" "$W/sub/.agents"
    frontmatter skill > "$W/.agents/skills/m-skill/SKILL.md"
    frontmatter agent > "$W/.agents/agents/m-agent/agent.md"
    frontmatter agentfile > "$W/.agents/agents/m-agentfile.md"
    plugin "$W/.agents/plugins/p-plugin" plugin
    plugin "$W/tools/p-listed" listed
    printf '{"entries": [{"path": "tools"}]}\n' > "$W/.agents/plugins.json"
    hook nested > "$W/sub/.agents/hooks.json"
    server nested > "$W/sub/.agents/mcp_config.json"
    plugin "$W/sub/.agents/plugins/p-nestedplugin" nestedplugin
    echo "a nested folder" > "$W/sub/README"
    reset
    turn paths "Read the skill m-skill and do what it says. Run ls -a in the shell. Then run ls -a in the folder sub. Then answer with the single word done."
}

case_midturn() {
    rm -rf "$W/.agents" "$W/sub" "$W/tools" ~/stash
    mkdir -p ~/stash/.agents
    hook written > ~/stash/.agents/hooks.json
    server written > ~/stash/.agents/mcp_config.json
    plugin ~/stash/.agents/plugins/p-writtenplugin writtenplugin
    reset
    turn midturn "Run this command in the shell: cp -r ~/stash/.agents ~/work/ . Then run ls -a in the shell. Then answer with the single word done."
    grep -o '"conversation_id":"[^"]*"' "$out/midturn.jsonl" | tail -1 | cut -d'"' -f4 > "$out/midturn.conversation"
}

case_nextturn() {
    # What crew-run does before every turn of a run that does not build, today: the two top files go.
    rm -f "$W/.agents/hooks.json" "$W/.agents/mcp_config.json"
    reset
    turn nextturn "Run ls -a in the shell. Then answer with the single word done." "$(cat "$out/midturn.conversation")"
}

settings
for c in "$@"; do "case_$c"; done
