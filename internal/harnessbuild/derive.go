package harnessbuild

import (
	"fmt"
	"strings"
)

// modelMap translates Claude model aliases to agy tiers. Anything unlisted
// (sonnet, fable) is omitted, which agy reads as `inherit`.
var modelMap = map[string]string{"opus": "pro", "haiku": "flash"}

// toolMap translates Claude tool names to agy ones. An unmapped tool is an
// error rather than a pass-through: agy hangs a subagent whose tools list
// names something it does not recognise.
var toolMap = map[string]string{
	"Read":  "view_file",
	"Grep":  "grep_search",
	"Glob":  "find_by_name",
	"Bash":  "run_command",
	"Write": "write_to_file",
	"Edit":  "replace_file_content",
}

const (
	claudeSkillsDir = "~/.claude/skills/"
	agySkillsDir    = "~/.gemini/config/skills/"
)

// DeriveAgy renders the agy `agent.md` flavour of a Claude Code agent.
// Claude-only keys (effort, maxTurns, and the rest of agentKeys that agy
// lacks) are dropped: the vendor documents no equivalent, and the dispatcher
// names the budget in the dispatch text. `skills:` is dropped too, on
// evidence: on 2026-10-01 an agy subagent with the vendor's documented
// `skills: ["skills/<name>"]` key and one without it both read the skill by
// the path its body names, in every run, so a path in the body is the one
// way that works. The body's `~/.claude/skills/` paths are
// rewritten to the agy directory.
func DeriveAgy(a Agent) ([]byte, error) {
	out := []string{
		"name: " + a.Name,
		"description: " + yamlQuote(a.Description),
		"subagent: true",
		"mainAgent: false",
	}
	if tier, ok := modelMap[a.Front["model"]]; ok {
		out = append(out, "model: "+tier)
	}
	if tools := a.Front["tools"]; tools != "" {
		var mapped []string
		for _, t := range SplitList(tools) {
			agy, ok := toolMap[t]
			if !ok {
				return nil, fmt.Errorf("agent %s: tool %q has no agy mapping", a.Name, t)
			}
			mapped = append(mapped, agy)
		}
		out = append(out, "tools: ["+strings.Join(mapped, ", ")+"]")
	}
	body := strings.ReplaceAll(a.Body, claudeSkillsDir, agySkillsDir)
	return []byte("---\n" + strings.Join(out, "\n") + "\n---\n" + body), nil
}
