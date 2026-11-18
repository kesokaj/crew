#!/usr/bin/env bash
# Spike items 11, 15, 28/41, 33, 48, 79 for agy. Run inside a spike sandbox as
# the user agent, with a working agy login in the home. Each case writes to
# ~/agy-checks/<case>.* and one summary line "# <case> ..." to standard output.
set -uo pipefail
out=~/agy-checks
mkdir -p "${out}"
A=~/.gemini/antigravity-cli
ms() { echo $(( ($(date +%s%N) - $1) / 1000000 )); }
msg() { python3 -I -c 'import json,sys;print(json.dumps({"event":"user","message":{"content":sys.argv[1]}}))' "$1"; }
turn() {  # turn <file> <model> <prompt> [extra flags...]: one stream-json turn in ~/work
    local f="$1" m="$2" p="$3"; shift 3
    ( cd ~/work && msg "$p" | timeout 300 agy -p "" --input-format stream-json --output-format stream-json \
        --print-timeout 240s --model "$m" "$@" ) > "$f" 2>&1
}

case_effort() {  # 48: --effort with a name that already holds an effort
    local s
    for e in "" low high; do
        s=$(date +%s%N)
        turn "${out}/effort-${e:-none}.jsonl" gemini-3.8-flash-low \
            "In one sentence, why is the sky blue?" ${e:+--effort "$e"}
        echo "# effort-${e:-none} exit=$? ms=$(ms "$s")"
    done
}

case_notools() {  # 79: one call with no tools, under a settings file that allows nothing
    cp "${A}/settings.json" "${A}/settings.json.spike"
    printf '{"toolPermission":"request-review","permissions":{"allow":[]},"trustedWorkspaces":[]}\n' > "${A}/settings.json"
    local i s
    for i in 1 2 3; do
        s=$(date +%s%N)
        turn "${out}/notools-$i.jsonl" gemini-3.8-flash-low "Answer with the single word ok."
        echo "# notools-$i exit=$? ms=$(ms "$s")"
    done
    mv "${A}/settings.json.spike" "${A}/settings.json"
}

case_skills() {  # 33: which skill folders agy reads, by a marker skill in each
    local d
    for d in "${A}/skills/spike-cli-dir" ~/.gemini/config/skills/spike-config-dir ~/work/.agents/skills/spike-workspace-dir; do
        mkdir -p "$d"
        printf -- '---\nname: %s\ndescription: Spike marker skill %s. Never use it.\n---\nDo nothing.\n' \
            "$(basename "$d")" "$(basename "$d")" > "$d/SKILL.md"
    done
    ( cd ~/work && timeout 60 agy -p /skills ) > "${out}/skills.txt" 2>&1
    echo "# skills exit=$? markers: $(grep -o 'spike-[a-z]*-dir' "${out}/skills.txt" | sort -u | tr '\n' ' ')"
}

case_workspace() {  # 28 and 41: does a workspace's .agents/hooks.json and mcp_config.json load?
    mkdir -p ~/work/.agents
    rm -f /tmp/spike-hook-ran
    # A named hook in the shape agy's own embedded documentation gives; if it
    # runs, it leaves a file.
    cat > ~/work/.agents/hooks.json <<'EOF'
{"spike-marker": {
  "PreToolUse": [{"matcher": "run_command", "hooks": [{"command": "touch /tmp/spike-hook-ran; echo '{}'"}]}],
  "PreInvocation": [{"type": "command", "command": "touch /tmp/spike-hook-ran; echo '{}'"}]}}
EOF
    printf '{"mcpServers":{"spike-ws":{"serverUrl":"http://127.0.0.1:8765/mcp","headers":{"Authorization":"Bearer spike-bearer-made-up"}}}}\n' \
        > ~/work/.agents/mcp_config.json
    : > ~/mcp-standin.log
    local s=$(date +%s%N)
    turn "${out}/workspace.jsonl" gemini-3.8-flash-low "Run ls -a in the shell, then answer with the single word done."
    echo "# workspace exit=$? ms=$(ms "$s") hook-ran=$([ -e /tmp/spike-hook-ran ] && echo yes || echo no) mcp-requests=$(grep -c POST ~/mcp-standin.log) tool-dir=$(ls "${A}/mcp" | tr '\n' ' ')"
    rm -rf ~/work/.agents
}

case_refused_login() {  # 11: a login file that is present but no longer accepted
    local t="${A}/antigravity-oauth-token"
    cp "$t" "$t.spike"
    python3 -I - "$t" <<'EOF'
import json, sys
p = sys.argv[1]; d = json.load(open(p))
d["token"]["access_token"] = "ya29" + "." + "spike-made-up-and-refused"
d["token"]["refresh_token"] = "spike-made-up-and-refused"
d["token"]["expiry"] = "2026-01-01T00:00:00Z"
json.dump(d, open(p, "w"))
EOF
    local s=$(date +%s%N)
    ( cd ~/work && timeout 90 agy models ) > "${out}/refused-login-models.txt" 2>&1
    echo "# refused-login models exit=$? ms=$(ms "$s")"
    s=$(date +%s%N)
    ( cd ~/work && timeout 90 agy -p "Answer ok." --output-format stream-json </dev/null ) > "${out}/refused-login-run.jsonl" 2>&1
    echo "# refused-login run exit=$? ms=$(ms "$s")"
    mv "$t.spike" "$t"
}

case_subagent() {  # 15: does step_update carry subagent_info for a helper?
    local s=$(date +%s%N)
    turn "${out}/subagent.jsonl" gemini-3.8-flash-low \
        "Start one subagent (a helper agent, using your tool for subagents) whose only job is to run 'echo from-helper' in the shell and report the output. Wait for it, then answer with what it reported."
    echo "# subagent exit=$? ms=$(ms "$s") subagent_info-lines=$(grep -c subagent_info "${out}/subagent.jsonl")"
}

for c in "$@"; do "case_${c//-/_}"; done
