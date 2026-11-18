package settings

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"go.yaml.in/yaml/v3"

	"github.com/kesokaj/crew/internal/db"
)

// boardYAML is the listing of board.yaml in section 14.1 of the design, as it is there: a project's
// settings. A test keeps the two the same.
//
//go:embed board.yaml
var boardYAML []byte

// The stages every board has, and the optional stages Crew knows (7.4). A project may add stages of
// its own between In progress and Done.
const (
	StagePlanning     = "planning"
	StageBacklog      = "backlog"
	StageAnalysis     = "analysis"
	StageInProgress   = "in_progress"
	StageReview       = "review"
	StageTest         = "test"
	StageE2E          = "e2e"
	StageVerification = "verification"
	StageDone         = "done"
	// Closed is where a ticket that was discarded or cancelled is. It is not a column (7.4).
	StageClosed = "closed"
)

// OptionalStages are Crew's own optional stages, in their order.
var OptionalStages = []string{StageReview, StageTest, StageE2E, StageVerification}

// DefaultStagesOn are the optional stages a new project has on when nothing says otherwise.
func DefaultStagesOn() []string {
	d, err := ProjectDefaults("default", "D")
	if err != nil {
		panic(err) // board.yaml is built in, and its own test reads it
	}
	var on []string
	for _, st := range ParseStages(d) {
		if st.Optional && st.On {
			on = append(on, st.ID)
		}
	}
	return on
}

// CheckNames are the checks a stage can name in its enter and exit lists (7.1, 14.1). A stage of a
// project's own can also name a command, "cmd: <command>", or a criterion, "judge: <criterion>".
var CheckNames = []string{
	"has_why_what_goal", "has_check", "analysis_approved", "dependencies_done",
	"branch_pushed", "has_commits", "note_exists", "task_cmd_checks_pass", "task_judge_checks_pass", "docs_done", "skills_vendored",
	"review_verdict_approve", "test_result_pass", "task_checks_pass", "e2e_result_pass", "verify_pass",
	"merges_and_passes_on_main", "stage_verdict_pass",
}

// ownStageChecks are what a project's own stage may name in its exit list beside a command, "cmd:
// <command>", and a criterion, "judge: <criterion>" (7.4, from M10): each is a check Crew evaluates.
var ownStageChecks = []string{"stage_verdict_pass", "task_checks_pass", "task_cmd_checks_pass", "docs_done"}

// OwnAgent is where a project's own stage's agent definition is on main: a file the CLI reads as
// code, so it changes only through the operator's Allow, and is read from main (13.8).
var OwnAgent = regexp.MustCompile(`^\.agents/agents/[A-Za-z0-9][A-Za-z0-9_.-]{0,80}\.md$`)

// Stage is one stage of a board, as a project's settings give it.
type Stage struct {
	ID       string   `json:"id"`
	Optional bool     `json:"optional,omitempty"`
	On       bool     `json:"on"`
	Role     string   `json:"role,omitempty"`
	WIP      int      `json:"wip,omitempty"`
	Enter    []string `json:"enter,omitempty"`
	Exit     []string `json:"exit,omitempty"`
	Run      string   `json:"run,omitempty"`
	// A project's own stage (7.4): its name on the board, its agent's definition on main, and its
	// prompt, which the operator wrote.
	Title  string `json:"title,omitempty"`
	Agent  string `json:"agent,omitempty"`
	Prompt string `json:"prompt,omitempty"`
}

// Own says the stage is one of the project's own, not a stage of Crew's board.
func (s Stage) Own() bool {
	return s.Optional && !contains(OptionalStages, s.ID)
}

var (
	// ProjectName is a project's name, which is also its repositories' name (6.1).
	ProjectName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	// ProjectKey is the prefix of a project's ticket numbers (7.12).
	ProjectKey = regexp.MustCompile(`^[A-Z][A-Z0-9]{0,9}$`)
	stageID    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	uuidShape  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// UUIDShaped says a name looks like a project's id. A route names a project by its name or its id,
// so no name may be read as another project's id.
func UUIDShaped(name string) bool { return uuidShape.MatchString(name) }

var projectSchema = &schema{
	nullable: map[string]string{
		"git.remote":                    "string",
		"hosting.hostname":              "string",
		"sandbox.suspend_after_minutes": "int",
		"sandbox.stop_after_minutes":    "int",
		"sandbox.max_snapshots":         "int",
	},
	enums: map[string][]string{
		"planning.rewrite":        {"inherit", "suggest", "off"},
		"hosting.access":          {"private", "listed", "google", "public"},
		"hosting.size":            {"small", "large"},
		"approvals.project":       {"operator"},
		"approvals.created_task":  {"agent", "operator"},
		"approvals.new_feature":   {"notice", "operator", "agent"},
		"approvals.agent_change":  {"auto", "operator"},
		"approvals.feature_specs": {"autonomous", "manual"},
		"sandbox.network":         {"inherit", "internet", "model_only", "lan"},
		"sandbox.check_network":   {"inherit", "internet", "model_only", "lan", "sources"},
		"git.merge":               {"auto", "merge_commit", "pull_request"},
	},
	// The project and its key are given when it is created, and never change.
	fixed: map[string]bool{"version": true, "project": true, "key": true},
	special: map[string]func(any) []string{
		"stages":   checkStages,
		"agents":   checkAgents,
		"git.push": checkPush,
	},
	bounds: projectBounds,
}

// ProjectDefaults returns a project's settings as the listing gives them, for a project of that
// name and key: no remote, and no agent of its own.
func ProjectDefaults(name, key string) (Doc, error) {
	var d Doc
	if err := yaml.Unmarshal(boardYAML, &d); err != nil {
		return nil, fmt.Errorf("the defaults: %w", err)
	}
	d = normalize(d).(Doc)
	d["project"], d["key"], d["agents"] = name, key, Doc{}
	d["git"].(Doc)["remote"] = nil
	return d, nil
}

// ValidateProject checks a project's settings against its defaults, as Validate does the machine's.
func ValidateProject(d, defaults Doc) error { return projectSchema.validate(d, defaults) }

func checkStages(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return []string{"must be a list of stages"}
	}
	var errs []string
	var ids []string
	seen := map[string]bool{}
	for i, e := range list {
		m, ok := e.(Doc)
		if !ok {
			errs = append(errs, fmt.Sprintf("[%d]: must be a stage", i))
			continue
		}
		id, _ := m["id"].(string)
		if !stageID.MatchString(id) || id == StageClosed {
			errs = append(errs, fmt.Sprintf("[%d]: id must be a word of small letters, such as review", i))
			continue
		}
		if seen[id] {
			errs = append(errs, id+": named twice")
		}
		seen[id] = true
		ids = append(ids, id)
		errs = append(errs, checkStage(id, m)...)
	}
	if len(errs) > 0 {
		return errs
	}
	fixedFirst := []string{StagePlanning, StageBacklog, StageAnalysis, StageInProgress}
	if len(ids) < len(fixedFirst)+1 || !reflect.DeepEqual(ids[:len(fixedFirst)], fixedFirst) || ids[len(ids)-1] != StageDone {
		return []string{"must begin with planning, backlog, analysis, and in_progress, and end with done"}
	}
	var builtIn []string
	for _, id := range ids[len(fixedFirst) : len(ids)-1] {
		if contains(OptionalStages, id) {
			builtIn = append(builtIn, id)
		}
	}
	if !reflect.DeepEqual(builtIn, OptionalStages) {
		errs = append(errs, "must hold review, test, e2e, and verification once each, in that order, between in_progress and done; switch one off with on: false")
	}
	return errs
}

func checkStage(id string, m Doc) []string {
	var errs []string
	fixedStage := contains([]string{StagePlanning, StageBacklog, StageAnalysis, StageInProgress, StageDone}, id)
	own := !fixedStage && !contains(OptionalStages, id)
	allowed := map[string]bool{"id": true, "optional": true, "on": true, "role": true, "wip": true, "enter": true, "exit": true, "run": true,
		"title": own, "agent": own, "prompt": own}
	for k := range m {
		if !allowed[k] {
			errs = append(errs, id+"."+k+": not a setting of a stage")
		}
	}
	if opt, ok := m["optional"]; ok && opt != !fixedStage {
		errs = append(errs, id+".optional: must be "+fmt.Sprint(!fixedStage))
	}
	if _, ok := m["on"]; ok && fixedStage {
		errs = append(errs, id+".on: a fixed stage cannot be switched off")
	}
	if on, ok := m["on"]; ok && kindOf(on) != "bool" {
		errs = append(errs, id+".on: must be true or false")
	}
	if !fixedStage && m["optional"] != true {
		errs = append(errs, id+".optional: must be true")
	}
	agent := !fixedStage || id == StageAnalysis || id == StageInProgress
	if agent {
		if r, ok := m["role"].(string); !ok || !stageID.MatchString(r) {
			errs = append(errs, id+".role: must name the agent of the stage")
		}
		if w, ok := m["wip"].(int); !ok || w < 1 {
			errs = append(errs, id+".wip: must be a whole number of 1 or more")
		}
	} else {
		for _, k := range []string{"role", "wip", "exit", "run"} {
			if _, ok := m[k]; ok {
				errs = append(errs, id+"."+k+": this stage has no agent")
			}
		}
	}
	for _, k := range []string{"enter", "exit"} {
		v, ok := m[k]
		if !ok {
			continue
		}
		l, ok := v.([]any)
		if !ok {
			errs = append(errs, id+"."+k+": must be a list of checks")
			continue
		}
		for _, c := range l {
			name, _ := c.(string)
			if !ValidCheck(name) {
				errs = append(errs, fmt.Sprintf("%s.%s: %v is not a check", id, k, c))
			}
		}
	}
	if agent && id != StageInProgress && id != StageAnalysis {
		if l, _ := m["exit"].([]any); len(l) == 0 {
			errs = append(errs, id+".exit: a stage with an agent needs an exit check")
		}
	}
	if r, ok := m["run"]; ok && kindOf(r) != "string" {
		errs = append(errs, id+".run: must be a command")
	}
	if own {
		errs = append(errs, checkOwnStage(id, m)...)
	} else {
		// A stage of Crew's own has its fixed checks: a command or a criterion is a project's own stage's.
		l, _ := m["exit"].([]any)
		for _, c := range l {
			name, _ := c.(string)
			if strings.HasPrefix(name, "cmd: ") || strings.HasPrefix(name, "judge: ") || name == "stage_verdict_pass" {
				errs = append(errs, fmt.Sprintf("%s.exit: %s is for a stage of the project's own", id, name))
			}
		}
	}
	return errs
}

// checkOwnStage checks what a project's own stage needs (7.4, R101): its agent's definition on main,
// its prompt, a name, and an exit list of checks Crew evaluates, so no ticket waits on a check nobody
// answers.
func checkOwnStage(id string, m Doc) []string {
	var errs []string
	if a, _ := m["agent"].(string); !OwnAgent.MatchString(a) {
		errs = append(errs, id+".agent: the path of its agent's definition on main, such as .agents/agents/security.md")
	}
	if p, _ := m["prompt"].(string); strings.TrimSpace(p) == "" || len(p) > 8000 {
		errs = append(errs, id+".prompt: what the stage's agent does, at most 8000 characters")
	}
	if t, ok := m["title"]; ok {
		if s, _ := t.(string); strings.TrimSpace(s) == "" || len(s) > 40 || strings.ContainsAny(s, "\n\r") {
			errs = append(errs, id+".title: one line of at most 40 characters")
		}
	}
	if _, ok := m["run"]; ok {
		errs = append(errs, id+".run: a stage of the project's own names its commands in its exit list, as cmd: <command>")
	}
	l, _ := m["exit"].([]any)
	verdict := false
	for _, c := range l {
		name, _ := c.(string)
		switch {
		case name == "stage_verdict_pass":
			verdict = true
		case strings.HasPrefix(name, "cmd: "), strings.HasPrefix(name, "judge: "):
			if len(name) > 2000 || strings.ContainsAny(name, "\n\r\x00") {
				errs = append(errs, fmt.Sprintf("%s.exit: %q: one line of at most 2000 characters", id, name))
			}
		case !contains(ownStageChecks, name):
			errs = append(errs, fmt.Sprintf("%s.exit: %s is not a check of a stage of the project's own: stage_verdict_pass, cmd: <command>, judge: <criterion>, %s",
				id, name, strings.Join(ownStageChecks[1:], ", ")))
		}
	}
	if !verdict {
		errs = append(errs, id+".exit: must hold stage_verdict_pass: the stage's agent decides beside its checks")
	}
	return errs
}

// ValidCheck says name can stand in a stage's enter or exit list.
func ValidCheck(name string) bool {
	if contains(CheckNames, name) {
		return true
	}
	for _, p := range []string{"cmd: ", "judge: "} {
		if rest, ok := strings.CutPrefix(name, p); ok && strings.TrimSpace(rest) != "" {
			return true
		}
	}
	return false
}

// The roles a project may give a model or an effort of its own: those of the machine's settings.
func roleNames() map[string]bool {
	var d Doc
	_ = yaml.Unmarshal(defaultsYAML, &d)
	out := map[string]bool{}
	if a, ok := normalize(d).(Doc)["agents"].(Doc); ok {
		if r, ok := a["roles"].(Doc); ok {
			for k := range r {
				out[k] = true
			}
		}
	}
	return out
}

var cliModel = regexp.MustCompile(`^[a-z][a-z0-9-]*/[A-Za-z0-9][A-Za-z0-9._:-]*$`)

func checkAgents(v any) []string {
	m, ok := v.(Doc)
	if !ok {
		return []string{"must be a group of settings"}
	}
	var errs []string
	roles := roleNames()
	for k, e := range m {
		switch {
		case k == "checks_use_another_family":
			if kindOf(e) != "bool" {
				errs = append(errs, k+": must be true or false")
			}
		case k == "at_limit":
			if s, _ := e.(string); s != "move" && s != "wait" {
				errs = append(errs, k+": must be one of move, wait")
			}
		case roles[k]:
			entry, ok := e.(Doc)
			if !ok {
				errs = append(errs, k+": must be a group of settings")
				continue
			}
			for ek, ev := range entry {
				switch ek {
				case "model":
					if s, _ := ev.(string); !cliModel.MatchString(s) {
						errs = append(errs, k+".model: must be a CLI and a model, such as agy/claude-sonnet-5-5")
					}
				case "effort":
					if s, _ := ev.(string); !contains(Efforts, s) {
						errs = append(errs, k+".effort: must be one of "+strings.Join(Efforts, ", "))
					}
				default:
					errs = append(errs, k+"."+ek+": not a setting")
				}
			}
		default:
			errs = append(errs, k+": not an agent")
		}
	}
	return errs
}

// CheckTicketAgents checks a ticket's pins: a model, with its CLI, or an effort for a role (6.3).
func CheckTicketAgents(v map[string]any) []string {
	if lead, ok := v["lead"]; ok {
		if _, has := v["curator"]; !has {
			v["curator"] = lead
		}
		delete(v, "lead")
	}
	for k := range v {
		if k == "at_limit" || k == "checks_use_another_family" {
			return []string{k + ": not a pin of a ticket"}
		}
	}
	return checkAgents(normalize(v))
}

func checkPush(v any) []string {
	l, ok := v.([]any)
	if !ok {
		return []string{"must be a list"}
	}
	for _, e := range l {
		if e != "main" && e != "task_branches" {
			return []string{"may hold main and task_branches"}
		}
	}
	return nil
}

// ParseStages reads the stages of a project's settings, which ValidateProject has accepted. A fixed
// stage is always on.
func ParseStages(d Doc) []Stage {
	list, _ := d["stages"].([]any)
	out := make([]Stage, 0, len(list))
	for _, e := range list {
		m, _ := e.(Doc)
		s := Stage{ID: str(m["id"])}
		s.Optional, _ = m["optional"].(bool)
		s.On = true
		if on, ok := m["on"].(bool); ok && s.Optional {
			s.On = on
		}
		s.Role = str(m["role"])
		s.WIP, _ = m["wip"].(int)
		s.Enter, s.Exit, s.Run = strs(m["enter"]), strs(m["exit"]), str(m["run"])
		s.Title, s.Agent, s.Prompt = str(m["title"]), str(m["agent"]), str(m["prompt"])
		out = append(out, s)
	}
	return out
}

func str(v any) string { s, _ := v.(string); return s }

func strs(v any) []string {
	l, _ := v.([]any)
	out := make([]string, 0, len(l))
	for _, e := range l {
		out = append(out, str(e))
	}
	return out
}

// SetStagesOn switches optional stages on or off in a project's settings: a change applies to the
// tickets that have not reached that stage yet (7.4). A fixed stage, or one the board does not have,
// is refused.
func SetStagesOn(d Doc, on map[string]bool) (Doc, error) {
	list, _ := d["stages"].([]any)
	out := make([]any, len(list))
	found := map[string]bool{}
	for i, e := range list {
		m, _ := e.(Doc)
		c := Doc{}
		for k, v := range m {
			c[k] = v
		}
		id := str(m["id"])
		if v, ok := on[id]; ok {
			if m["optional"] != true {
				return nil, &InvalidError{Problems: []string{id + ": a fixed stage cannot be switched off"}}
			}
			c["on"] = v
			found[id] = true
		}
		out[i] = c
	}
	for id := range on {
		if !found[id] {
			return nil, &InvalidError{Problems: []string{id + ": the board has no such optional stage"}}
		}
	}
	n := Doc{}
	for k, v := range d {
		n[k] = v
	}
	n["stages"] = out
	return n, nil
}

// LoadProject returns the operator's own values of a project's settings, as stored.
func LoadProject(ctx context.Context, q db.Querier, project string) (Doc, error) {
	var raw []byte
	err := q.QueryRow(ctx, `SELECT doc FROM settings WHERE project = $1`, project).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return Doc{}, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeJSON(raw)
}

// EffectiveProject returns a project's settings as they apply: its defaults with its own values.
func EffectiveProject(ctx context.Context, q db.Querier, project, name, key string) (Doc, error) {
	defaults, err := ProjectDefaults(name, key)
	if err != nil {
		return nil, err
	}
	own, err := LoadProject(ctx, q, project)
	if err != nil {
		return nil, err
	}
	return Merge(defaults, own), nil
}

// SaveProject validates a whole set of a project's settings and stores what differs from its
// defaults, with its row in changes, in the transaction given. A set that does not validate changes
// nothing (14.2). The project and the key of the set must be the project's.
func SaveProject(ctx context.Context, tx pgx.Tx, project, name, key string, d Doc, who string) error {
	defaults, err := ProjectDefaults(name, key)
	if err != nil {
		return err
	}
	DropRetired(d)
	if err := ValidateProject(d, defaults); err != nil {
		return err
	}
	if p, ok := d["project"]; ok && p != name {
		return &InvalidError{Problems: []string{"project: these are the settings of " + fmt.Sprint(p) + ", not of " + name}}
	}
	if k, ok := d["key"]; ok && k != key {
		return &InvalidError{Problems: []string{"key: a project's key does not change"}}
	}
	own := Diff(d, defaults)
	before, err := LoadProject(ctx, tx, project)
	if err != nil {
		return err
	}
	if reflect.DeepEqual(before, own) {
		return nil
	}
	ownJSON, err := json.Marshal(own)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO settings (project, doc, updated_by) VALUES ($1, $2, $3)
		ON CONFLICT ((coalesce(project::text, 'machine'))) DO UPDATE
		SET doc = excluded.doc, updated_by = excluded.updated_by, updated_at = now()`, project, ownJSON, who); err != nil {
		return err
	}
	return db.RecordProjectChange(ctx, tx, project, who, "settings", project, before, json.RawMessage(ownJSON))
}

// FormatProject writes a project's settings as the board.yaml of an export.
func FormatProject(d Doc) ([]byte, error) {
	var buf strings.Builder
	buf.WriteString("# A project's settings (board.yaml). The defaults and what each setting does are in\n")
	buf.WriteString("# section 14.1 of the design.\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(d); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return []byte(buf.String()), nil
}
