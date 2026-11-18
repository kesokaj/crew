package board

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/baseline"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/settings"
	"go.yaml.in/yaml/v3"
)

// WorkflowAgent declares a bounded custom specialist peer agent in WORKFLOW.md (design 11.10, 13.8).
type WorkflowAgent struct {
	Slug         string   `json:"slug"`
	PromptFile   string   `json:"promptFile,omitempty"`
	ModelBracket string   `json:"modelBracket"`
	ReadOnly     bool     `json:"readOnly"`
	Docker       bool     `json:"docker"`
	Skills       []string `json:"skills,omitempty"`
}

// WorkflowDef is the parsed WORKFLOW.md contract pinned strictly from main (design 6.8, 13.8).
type WorkflowDef struct {
	ProjectID    string          `json:"projectId"`
	MainSHA      string          `json:"mainSha"`
	RawMarkdown  string          `json:"rawMarkdown"`
	Frontmatter  map[string]any  `json:"frontmatter"`
	Harness      string          `json:"harness"`
	MaxTries     int             `json:"maxTries"`
	MaxAgentHops int             `json:"maxAgentHops"`
	FeatureSpecs string          `json:"featureSpecs"`
	Agents       []WorkflowAgent `json:"agents,omitempty"`
	SyncedAt     time.Time       `json:"syncedAt"`
}

// HeldWorkflowBranch identifies an open ticket branch whose unapproved WORKFLOW.md edit is held under 13.8.
type HeldWorkflowBranch struct {
	TicketKey string `json:"ticketKey"`
	Branch    string `json:"branch"`
	Approved  bool   `json:"approved"`
}

// WorkflowView is returned by GetWorkflow and GET /api/projects/{p}/workflow.
type WorkflowView struct {
	WorkflowDef
	HeldBranches []HeldWorkflowBranch `json:"heldBranches"`
}

func defaultWorkflowFile(n NewProject) string {
	return baseline.RenderWorkflow(n.Name, n.Key)
}

// ParseWorkflow extracts YAML frontmatter from WORKFLOW.md while keeping safe bounds and defaults.
func ParseWorkflow(raw string) (WorkflowDef, error) {
	def := WorkflowDef{
		RawMarkdown:  raw,
		Frontmatter:  map[string]any{},
		Harness:      "agy",
		MaxTries:     3,
		MaxAgentHops: 3,
		FeatureSpecs: "autonomous",
	}
	if strings.TrimSpace(raw) == "" {
		return def, nil
	}
	norm := strings.ReplaceAll(raw, "\r\n", "\n")
	if !strings.HasPrefix(norm, "---\n") {
		return def, nil
	}
	end := strings.Index(norm[4:], "\n---")
	if end < 0 {
		return def, errors.New("WORKFLOW.md frontmatter does not terminate")
	}
	fmText := norm[4 : 4+end]
	parsed := map[string]any{}
	if err := yaml.Unmarshal([]byte(fmText), &parsed); err != nil {
		return def, err
	}
	if parsed != nil {
		def.Frontmatter = parsed
	}
	if h, ok := parsed["harness"].(string); ok && strings.TrimSpace(h) != "" {
		def.Harness = strings.TrimSpace(h)
	}
	if n, ok := intFromAny(parsed["max_tries"]); ok {
		def.MaxTries = min(max(n, 1), 20)
	}
	if n, ok := intFromAny(parsed["max_agent_hops"]); ok {
		def.MaxAgentHops = min(max(n, 1), 20)
	}
	if fs := featureSpecMode(parsed); fs != "" {
		def.FeatureSpecs = fs
	}
	if rawAgents, hasAgents := parsed["agents"]; hasAgents {
		agents, aerr := parseWorkflowAgents(rawAgents)
		if aerr != nil {
			return def, aerr
		}
		def.Agents = agents
	}
	return def, nil
}

const maxWorkflowAgents = 16

var (
	workflowAgentSlugRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	workflowSkillRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	reservedAgentSlugs  = []string{
		"user", "operator", "system", "crew", "improver", "assistant", "stage", "judge", "summary", "prompt",
		"coordinator", "planner", "researcher", "builder", "reviewer", "tester", "e2e", "verifier", "security", "dreamer", "curator",
	}
)

func parseWorkflowAgents(raw any) ([]WorkflowAgent, error) {
	if raw == nil {
		return nil, nil
	}
	var entries []map[string]any
	switch x := raw.(type) {
	case []any:
		for _, item := range x {
			m, ok := item.(map[string]any)
			if !ok {
				return nil, errors.New("WORKFLOW.md agents entries must be objects")
			}
			entries = append(entries, m)
		}
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			m, ok := x[k].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("WORKFLOW.md agent %q must be an object", k)
			}
			cp := make(map[string]any, len(m)+1)
			for mk, mv := range m {
				cp[mk] = mv
			}
			if _, hasSlug := cp["slug"]; !hasSlug {
				cp["slug"] = k
			}
			entries = append(entries, cp)
		}
	default:
		return nil, errors.New("WORKFLOW.md agents must be a list or map")
	}
	if len(entries) > maxWorkflowAgents {
		return nil, fmt.Errorf("WORKFLOW.md defines %d agents (max %d)", len(entries), maxWorkflowAgents)
	}
	seen := map[string]bool{}
	out := make([]WorkflowAgent, 0, len(entries))
	for _, m := range entries {
		ag, err := parseOneWorkflowAgent(m)
		if err != nil {
			return nil, err
		}
		if seen[ag.Slug] {
			return nil, fmt.Errorf("WORKFLOW.md duplicate agent slug %q", ag.Slug)
		}
		seen[ag.Slug] = true
		out = append(out, ag)
	}
	return out, nil
}

func parseOneWorkflowAgent(m map[string]any) (WorkflowAgent, error) {
	rawSlug, _ := m["slug"].(string)
	slug := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(rawSlug), "@"))
	if !workflowAgentSlugRe.MatchString(slug) || slices.Contains(reservedAgentSlugs, slug) {
		return WorkflowAgent{}, fmt.Errorf("WORKFLOW.md invalid custom agent slug %q", rawSlug)
	}
	bracket := "pro"
	if b := firstString(m, "model_bracket", "modelBracket"); b != "" {
		b = strings.ToLower(strings.TrimSpace(b))
		if b != "pro" && b != "flash" {
			return WorkflowAgent{}, fmt.Errorf("WORKFLOW.md agent %q invalid modelBracket %q (want pro or flash)", slug, b)
		}
		bracket = b
	}
	readOnly := true
	if v, ok := firstBool(m, "read_only", "readOnly"); ok {
		readOnly = v
	}
	docker := false
	if v, ok := firstBool(m, "docker"); ok {
		docker = v
	}
	promptFile := firstString(m, "prompt_file", "promptFile")
	if promptFile != "" {
		if err := validateAgentPromptPath(promptFile); err != nil {
			return WorkflowAgent{}, fmt.Errorf("WORKFLOW.md agent %q: %w", slug, err)
		}
		promptFile = path.Clean(promptFile)
	}
	var skills []string
	if rawSkills, ok := m["skills"].([]any); ok {
		if len(rawSkills) > maxWorkflowAgents {
			return WorkflowAgent{}, fmt.Errorf("WORKFLOW.md agent %q lists too many skills (%d > %d)", slug, len(rawSkills), maxWorkflowAgents)
		}
		for _, s := range rawSkills {
			name, _ := s.(string)
			name = strings.TrimSpace(name)
			if !workflowSkillRe.MatchString(name) {
				return WorkflowAgent{}, fmt.Errorf("WORKFLOW.md agent %q invalid skill slug %q", slug, name)
			}
			if !slices.Contains(skills, name) {
				skills = append(skills, name)
			}
		}
	}
	return WorkflowAgent{
		Slug:         slug,
		PromptFile:   promptFile,
		ModelBracket: bracket,
		ReadOnly:     readOnly,
		Docker:       docker,
		Skills:       skills,
	}, nil
}

func validateAgentPromptPath(raw string) error {
	if strings.Contains(raw, `\`) || strings.HasPrefix(raw, "/") || strings.Contains(raw, "..") {
		return fmt.Errorf("invalid promptFile %q", raw)
	}
	clean := path.Clean(raw)
	if !strings.HasSuffix(clean, ".md") || (!strings.HasPrefix(clean, ".agents/agents/") && !strings.HasPrefix(clean, ".crew/agents/")) {
		return fmt.Errorf("promptFile %q must be a .md file under .agents/agents/ or .crew/agents/", raw)
	}
	return nil
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func firstBool(m map[string]any, keys ...string) (bool, bool) {
	for _, k := range keys {
		if v, ok := m[k].(bool); ok {
			return v, true
		}
	}
	return false, false
}

func intFromAny(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case uint64:
		return int(x), true
	case float64:
		return int(x), true
	default:
		return 0, false
	}
}

func featureSpecMode(fm map[string]any) string {
	if app, ok := fm["approvals"].(map[string]any); ok {
		if v, ok := app["feature_specs"].(string); ok {
			if v == "manual" || v == "autonomous" {
				return v
			}
		}
	}
	if v, ok := fm["feature_specs"].(string); ok {
		if v == "manual" || v == "autonomous" {
			return v
		}
	}
	return ""
}

// SyncMainWorkflow reads WORKFLOW.md strictly from canonical main and stores it in workflow_defs,
// rejecting symbolic links in WORKFLOW.md, .crew, and .agents (13.8 fail-closed).
func (b *Board) SyncMainWorkflow(ctx context.Context, q db.Querier, p *Project) (*WorkflowDef, error) {
	var mainSHA, raw string
	var symlinkErr error
	if b.Git != nil && b.Git.Exists(p.Name) {
		mainSHA = b.Git.Main(ctx, p.Name)
		if mainSHA != "" {
			for _, guardPath := range []string{".crew", ".agents"} {
				if cerr := b.Git.CheckNoSymlinkChain(ctx, p.Name, mainSHA, guardPath); errors.Is(cerr, gitstore.ErrSymlinkRefused) {
					symlinkErr = cerr
					break
				}
			}
			if symlinkErr == nil {
				blob, err := b.Git.ReadRegularBlob(ctx, p.Name, mainSHA, "WORKFLOW.md")
				if errors.Is(err, gitstore.ErrSymlinkRefused) {
					symlinkErr = err
				} else if err != nil && !errors.Is(err, gitstore.ErrNoRev) {
					return nil, err
				} else if err == nil && !blob.Binary {
					raw = blob.Content
				}
			}
		}
	}
	def, parseErr := ParseWorkflow(raw)
	def.ProjectID = p.ID
	def.MainSHA = mainSHA
	if symlinkErr != nil {
		def.Agents = nil
		def.Frontmatter = map[string]any{"symlink_error": symlinkErr.Error()}
	} else if parseErr != nil {
		def.Agents = nil
		def.Frontmatter = map[string]any{"parse_error": parseErr.Error()}
	}
	fmJSON, err := json.Marshal(def.Frontmatter)
	if err != nil {
		return nil, err
	}
	err = q.QueryRow(ctx, `INSERT INTO workflow_defs (project_id, main_sha, raw_markdown, frontmatter, harness, max_tries, max_agent_hops, feature_specs, synced_at)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, $8, now())
		ON CONFLICT (project_id) DO UPDATE SET
			main_sha = EXCLUDED.main_sha,
			raw_markdown = EXCLUDED.raw_markdown,
			frontmatter = EXCLUDED.frontmatter,
			harness = EXCLUDED.harness,
			max_tries = EXCLUDED.max_tries,
			max_agent_hops = EXCLUDED.max_agent_hops,
			feature_specs = EXCLUDED.feature_specs,
			synced_at = now()
		RETURNING synced_at`,
		p.ID, def.MainSHA, def.RawMarkdown, fmJSON, def.Harness, def.MaxTries, def.MaxAgentHops, def.FeatureSpecs).Scan(&def.SyncedAt)
	if err != nil {
		return nil, err
	}
	if symlinkErr != nil {
		return &def, symlinkErr
	}
	return &def, nil
}

// LoadCustomWorkflowAgent resolves a custom specialist agent declared on canonical main's
// WORKFLOW.md and verifies its promptFile (if any) contains no git symlinks (13.8).
func (b *Board) LoadCustomWorkflowAgent(ctx context.Context, p *Project, slug string) (*WorkflowAgent, string, error) {
	if b == nil || p == nil {
		return nil, "", errf(Invalid, "project is required")
	}
	norm := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(slug), "@"))
	wf, err := b.GetWorkflow(ctx, b.Pool, p)
	if err != nil {
		return nil, "", err
	}
	var found *WorkflowAgent
	for i := range wf.Agents {
		if wf.Agents[i].Slug == norm {
			ag := wf.Agents[i]
			found = &ag
			break
		}
	}
	if found == nil {
		return nil, "", errf(NotFound, "custom specialist agent %q not found in WORKFLOW.md", slug)
	}
	var promptBody string
	if found.PromptFile != "" && b.Git != nil && b.Git.Exists(p.Name) {
		rev := wf.MainSHA
		if rev == "" {
			rev = "main"
		}
		blob, rerr := b.Git.ReadRegularBlob(ctx, p.Name, rev, found.PromptFile)
		if rerr != nil {
			return nil, "", rerr
		}
		if blob.Binary {
			return nil, "", errf(Invalid, "custom agent promptFile %q is binary", found.PromptFile)
		}
		promptBody = blob.Content
	}
	return found, promptBody, nil
}

// GetWorkflow returns main's WORKFLOW.md definition, resyncing automatically whenever main moves,
// and lists open branches carrying unmerged WORKFLOW.md edits held under 13.8.
func (b *Board) GetWorkflow(ctx context.Context, q db.Querier, p *Project) (*WorkflowView, error) {
	var currentMain string
	if b.Git != nil && b.Git.Exists(p.Name) {
		currentMain = b.Git.Main(ctx, p.Name)
	}
	def, err := readWorkflowRow(ctx, q, p.ID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && currentMain != "" && def.MainSHA != currentMain) {
		def, err = b.SyncMainWorkflow(ctx, q, p)
	}
	if err != nil {
		return nil, err
	}
	view := &WorkflowView{WorkflowDef: *def, HeldBranches: []HeldWorkflowBranch{}}
	if b.Git == nil || !b.Git.Exists(p.Name) || currentMain == "" {
		return view, nil
	}
	tks, err := TicketsIn(ctx, q, p.ID)
	if err != nil {
		return nil, err
	}
	for _, tk := range tks {
		if tk.Branch == nil || tk.Stage == settings.StageDone || tk.Stage == settings.StageClosed {
			continue
		}
		if !b.branchChangesWorkflow(ctx, p, currentMain, *tk.Branch) {
			continue
		}
		head, _ := b.Head(ctx, p, tk)
		view.HeldBranches = append(view.HeldBranches, HeldWorkflowBranch{
			TicketKey: tk.Key,
			Branch:    *tk.Branch,
			Approved:  CodeAllowed(tk, head),
		})
	}
	return view, nil
}

func (b *Board) branchChangesWorkflow(ctx context.Context, p *Project, mainSHA, branch string) bool {
	if gitstore.ValidBranch(branch) {
		br, err := b.Git.ReadBranch(ctx, p.Name, branch)
		return err == nil && br.Pushed && slices.Contains(br.Changed, "WORKFLOW.md")
	}
	touched, err := b.Git.Touched(ctx, p.Name, mainSHA, branch)
	if err != nil {
		return false
	}
	_, ok := touched["WORKFLOW.md"]
	return ok
}

func readWorkflowRow(ctx context.Context, q db.Querier, projectID string) (*WorkflowDef, error) {
	var def WorkflowDef
	var fmJSON []byte
	err := q.QueryRow(ctx, `SELECT project_id::text, main_sha, raw_markdown, frontmatter, harness, max_tries, max_agent_hops, feature_specs, synced_at
		FROM workflow_defs WHERE project_id = $1`, projectID).Scan(
		&def.ProjectID, &def.MainSHA, &def.RawMarkdown, &fmJSON, &def.Harness, &def.MaxTries, &def.MaxAgentHops, &def.FeatureSpecs, &def.SyncedAt)
	if err != nil {
		return nil, err
	}
	if len(fmJSON) > 0 {
		_ = json.Unmarshal(fmJSON, &def.Frontmatter)
	}
	if def.Frontmatter == nil {
		def.Frontmatter = map[string]any{}
	}
	if parsed, perr := ParseWorkflow(def.RawMarkdown); perr == nil {
		def.Agents = parsed.Agents
	}
	if _, hasSymErr := def.Frontmatter["symlink_error"]; hasSymErr {
		def.Agents = nil
	}
	return &def, nil
}
