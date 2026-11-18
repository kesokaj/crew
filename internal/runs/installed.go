package runs

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// installCmd finds a package being installed in what a tool was asked to run: a tool that most runs
// install belongs in the runtime image (10.2, 10.4).
var installCmd = regexp.MustCompile(`\b(?:sudo\s+)?(?:` +
	`(apt-get|apt|apk|dnf|yum)\s+(?:-\S+\s+)*(?:install|add)` +
	`|(pip3?|uv\s+pip|pipx)\s+install` +
	`|(npm|pnpm|yarn)\s+(?:i|install|add)\s+(?:-g|--global)` +
	`|(go)\s+install` +
	`|(cargo)\s+install` +
	`|(gem)\s+install` +
	`)((?:\s+[-\w.@/:=+]+)+)`)

// installed returns the install command in a tool's input, as "manager package …", or "" when it
// installs nothing.
func installed(input string) string {
	// A tool's input is JSON: a command in it has its quotes and line breaks escaped.
	input = strings.NewReplacer(`\"`, `"`, `\n`, " ", `\t`, " ").Replace(input)
	m := installCmd.FindStringSubmatch(input)
	if m == nil {
		return ""
	}
	manager := ""
	for _, g := range m[1:7] {
		if g != "" {
			manager = strings.Fields(g)[0]
			break
		}
	}
	var pkgs []string
	for _, w := range strings.Fields(m[7]) {
		if !strings.HasPrefix(w, "-") {
			pkgs = append(pkgs, w)
		}
	}
	if manager == "" || len(pkgs) == 0 {
		return ""
	}
	return trim(manager+" "+strings.Join(pkgs, " "), 200)
}

// recordInstalled records a tool installed in a run once for each command, in the transaction that
// stores the event.
func recordInstalled(ctx context.Context, tx pgx.Tx, run, cmd string) error {
	detail, _ := json.Marshal(map[string]any{"command": cmd, "text": cmd})
	_, err := tx.Exec(ctx, `INSERT INTO signals (project, ticket, run, kind, role, cli, model, effort, crew_version, detail)
		SELECT s.project, s.ticket, r.id, 'tool_installed', s.role, r.cli, r.model, r.effort, r.crew_version, $2
		FROM runs r JOIN subtasks s ON s.id = r.subtask WHERE r.id = $1
		  AND NOT EXISTS (SELECT 1 FROM signals g WHERE g.run = r.id AND g.kind = 'tool_installed' AND g.detail->>'command' = $3)`,
		run, detail, cmd)
	return err
}
