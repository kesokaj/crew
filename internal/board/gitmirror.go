package board

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"go.yaml.in/yaml/v3"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/settings"
)

// A project's board state mirrored in Git under .crew/ on main (design 6.2).
const maxMirrorFile = 2 << 20

// MirrorTicket is the front matter of .crew/tickets/<KEY>.md.
type MirrorTicket struct {
	ID            string      `yaml:"id"`
	Key           string      `yaml:"key"`
	Title         string      `yaml:"title"`
	Kind          string      `yaml:"kind"`
	Part          *string     `yaml:"part,omitempty"`
	Source        *Source     `yaml:"source,omitempty"`
	Stage         string      `yaml:"stage"`
	ClosedReason  *string     `yaml:"closed_reason,omitempty"`
	Skips         []string    `yaml:"skips,omitempty"`
	SkipsReason   *string     `yaml:"skips_reason,omitempty"`
	Origin        string      `yaml:"origin"`
	CreatedBy     string      `yaml:"created_by"`
	Priority      int         `yaml:"priority"`
	Position      int64       `yaml:"position"`
	Hold          bool        `yaml:"hold,omitempty"`
	Branch        *string     `yaml:"branch,omitempty"`
	FixRounds     int         `yaml:"fix_rounds,omitempty"`
	Analysis      Analysis    `yaml:"analysis,omitempty"`
	Checks        []CheckSpec `yaml:"checks,omitempty"`
	Links         []Link      `yaml:"links,omitempty"`
	Docs          []string    `yaml:"docs,omitempty"`
	DocsReason    *string     `yaml:"docs_reason,omitempty"`
	Why           string      `yaml:"why,omitempty"`
	What          string      `yaml:"what,omitempty"`
	Goal          string      `yaml:"goal,omitempty"`
	Approach      *string     `yaml:"approach,omitempty"`
	Seen          *string     `yaml:"seen,omitempty"`
	Spec          string      `yaml:"spec,omitempty"`
	DependsOn     []string    `yaml:"depends_on,omitempty"`
	MaxTries      int         `yaml:"max_tries,omitempty"`
	TriesUsed     int         `yaml:"tries_used,omitempty"`
	AssignedAgent string      `yaml:"assigned_agent,omitempty"`
	BudgetTokens  int64       `yaml:"budget_tokens,omitempty"`
	MayChange     []string    `yaml:"may_change,omitempty"`
}

func formatMirrorTicket(t *Ticket, stage string) ([]byte, error) {
	var deps []string
	for _, l := range t.Links {
		if l.Kind == "depends_on" && l.To != "" {
			deps = append(deps, l.To)
		}
	}
	mt := MirrorTicket{
		ID: fmt.Sprintf("%04d", t.Number), Key: t.Key, Title: t.Title, Kind: t.Kind, Part: t.Part,
		Source: t.Source, Stage: stage, ClosedReason: t.ClosedReason, Skips: t.Skips, SkipsReason: t.SkipsReason,
		Origin: t.Origin, CreatedBy: t.CreatedBy, Priority: t.Priority, Position: t.Position, Hold: t.Hold,
		Branch: t.Branch, FixRounds: t.FixRounds, Analysis: t.Analysis, Checks: t.Checks, Links: t.Links,
		Docs: t.Docs, DocsReason: t.DocsReason, Why: t.Why, What: t.What, Goal: t.Goal, Approach: t.Approach, Seen: t.Seen,
		Spec: t.SpecRef, DependsOn: deps, MaxTries: t.MaxTries, TriesUsed: t.TriesUsed,
		AssignedAgent: t.AssignedAgent, BudgetTokens: t.BudgetTokens, MayChange: t.Analysis.MayChange,
	}
	var buf bytes.Buffer
	buf.WriteString("---\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(mt); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	fmt.Fprintf(&buf, "---\n\n# %s: %s\n", t.Key, oneLine(t.Title))
	for _, sec := range [][2]string{{"Why", t.Why}, {"What", t.What}, {"Goal", t.Goal}, {"Approach", deref(t.Approach)}} {
		if body := strings.TrimSpace(sec[1]); body != "" {
			fmt.Fprintf(&buf, "\n## %s\n\n%s\n", sec[0], body)
		}
	}
	return buf.Bytes(), nil
}

func parseMirrorTicket(raw []byte) (MirrorTicket, error) {
	var mt MirrorTicket
	s := string(raw)
	if !strings.HasPrefix(s, "---\n") {
		return mt, errors.New("no front matter")
	}
	end := strings.Index(s[4:], "\n---\n")
	if end < 0 {
		return mt, errors.New("the front matter does not end")
	}
	dec := yaml.NewDecoder(strings.NewReader(s[4 : 4+end+1]))
	return mt, dec.Decode(&mt)
}

// MirrorOverlay builds the .crew/ files for a project on main. When merged is given, it renders
// .crew/board.yaml, .crew/plans/original.md, and .crew/tickets/<KEY>.md for the merged ticket.
func (b *Board) MirrorOverlay(ctx context.Context, q db.Querier, p *Project, merged *Ticket) (gitstore.Overlay, error) {
	o := gitstore.Overlay{}
	doc, err := b.Settings(ctx, q, p)
	if err != nil {
		return nil, err
	}
	boardBytes, err := settings.FormatProject(doc)
	if err != nil {
		return nil, err
	}
	o[".crew/board.yaml"] = func([]byte) []byte { return boardBytes }
	rows, err := q.Query(ctx, `SELECT kind, number, text FROM plans WHERE project = $1 AND kind IN ('original', 'amendment') ORDER BY kind, number`, p.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var kind, text string
		var num int
		if err := rows.Scan(&kind, &num, &text); err != nil {
			rows.Close()
			return nil, err
		}
		pth := ".crew/plans/original.md"
		if kind == "amendment" {
			if merged != nil {
				continue
			}
			pth = fmt.Sprintf(".crew/plans/amendment-%02d.md", num)
		}
		body := []byte(strings.TrimRight(text, "\n") + "\n")
		o[pth] = func([]byte) []byte { return body }
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if merged != nil {
		raw, err := formatMirrorTicket(merged, settings.StageDone)
		if err != nil {
			return nil, err
		}
		o[fmt.Sprintf(".crew/tickets/%s.md", merged.Key)] = func([]byte) []byte { return raw }
		return o, nil
	}
	tks, err := TicketsIn(ctx, q, p.ID)
	if err != nil {
		return nil, err
	}
	for _, tk := range tks {
		raw, err := formatMirrorTicket(tk, tk.Stage)
		if err != nil {
			return nil, err
		}
		o[fmt.Sprintf(".crew/tickets/%s.md", tk.Key)] = func([]byte) []byte { return raw }
	}
	return o, nil
}

// SyncMirror writes the full .crew/ mirror of a project onto main when any mirror file changed.
func (b *Board) SyncMirror(ctx context.Context, p *Project, message string) (string, error) {
	if b.Git == nil || !b.Git.Exists(p.Name) {
		return "", nil
	}
	if b.Identity == nil {
		return "", errf(Refused, "no git identity: set it up in Setup first")
	}
	id, err := b.Identity(ctx)
	if err != nil {
		return "", err
	}
	o, err := b.MirrorOverlay(ctx, b.Pool, p, nil)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(message) == "" {
		message = p.Key + ": update .crew/ board mirror"
	}
	before := b.Git.Main(ctx, p.Name)
	commit, err := b.Git.CommitOverlay(ctx, p.Name, o, message, id)
	if err == nil && commit != "" && commit != before && p.Remote != nil {
		_, _ = b.Pool.Exec(ctx, `INSERT INTO pushes (project, remote, refs, who) VALUES ($1, $2, '{refs/heads/main}', $3)`,
			p.ID, *p.Remote, Backend)
	}
	return commit, err
}

var (
	mirrorTicketFile = regexp.MustCompile(`^\.crew/tickets/([A-Z][A-Z0-9]{0,9})-(\d+)\.md$`)
	mirrorAmendFile  = regexp.MustCompile(`^\.crew/plans/amendment-(\d+)\.md$`)
	mirrorKinds      = []string{"feature", "fix", "chore", "docs", "revert"}
	mirrorOrigins    = []string{"plan", "agent", "operator", "token", "conversation", "chat"}
	mirrorLinks      = []string{"depends_on", "created_from", "duplicates", "fixes_bug", "replaces"}
)

// ImportMirror restores a project's settings, plans, and tickets from .crew/ on main into tx.
func (b *Board) ImportMirror(ctx context.Context, tx pgx.Tx, p *Project, doc settings.Doc, who string) (bool, error) {
	files, names, err := b.readMirrorFiles(ctx, p)
	if err != nil || len(files) == 0 {
		return false, err
	}
	if err := applyMirrorSettingsAndPlans(ctx, tx, p, doc, files, names, who); err != nil {
		return false, err
	}
	defaults, err := settings.ProjectDefaults(p.Name, p.Key)
	if err != nil {
		return false, err
	}
	tickets, err := decodeMirrorTickets(p, PipelineOf(settings.Merge(defaults, doc)), files, names)
	if err != nil {
		return false, err
	}
	return true, insertMirrorTickets(ctx, tx, p, tickets, who)
}

func (b *Board) readMirrorFiles(ctx context.Context, p *Project) (map[string][]byte, []string, error) {
	if b.Git == nil {
		return nil, nil, nil
	}
	main := b.Git.Main(ctx, p.Name)
	if main == "" {
		return nil, nil, nil
	}
	list, err := b.Git.Files(ctx, p.Name, main, func(pth string) bool { return strings.HasPrefix(pth, ".crew/") }, 2000)
	if err != nil || len(list) == 0 {
		return nil, nil, err
	}
	files := map[string][]byte{}
	var names []string
	hasMirror := false
	for _, f := range list {
		if f.Size > maxMirrorFile {
			continue
		}
		raw, err := b.Git.Blob(ctx, p.Name, f.Blob, maxMirrorFile)
		if err != nil {
			return nil, nil, err
		}
		files[f.Path] = raw
		names = append(names, f.Path)
		if f.Path == ".crew/board.yaml" || mirrorTicketFile.MatchString(f.Path) {
			hasMirror = true
		}
	}
	if !hasMirror {
		return nil, nil, nil
	}
	sort.Strings(names)
	return files, names, nil
}

func applyMirrorSettingsAndPlans(ctx context.Context, tx pgx.Tx, p *Project, doc settings.Doc, files map[string][]byte, names []string, who string) error {
	if raw := files[".crew/board.yaml"]; raw != nil {
		parsed, err := settings.Parse(raw)
		if err != nil {
			return fmt.Errorf(".crew/board.yaml: %w", err)
		}
		parsed["project"], parsed["key"] = p.Name, p.Key
		defaults, err := settings.ProjectDefaults(p.Name, p.Key)
		if err != nil {
			return err
		}
		settings.DropRetired(parsed)
		if err := settings.ValidateProject(parsed, defaults); err != nil {
			return fmt.Errorf(".crew/board.yaml: %w", err)
		}
		for k, v := range parsed {
			doc[k] = v
		}
	}
	if orig := files[".crew/plans/original.md"]; len(bytes.TrimSpace(orig)) > 0 {
		if _, err := tx.Exec(ctx, `INSERT INTO plans (project, kind, number, text, created_by) VALUES ($1, 'original', 1, $2, $3) ON CONFLICT DO NOTHING`,
			p.ID, string(orig), who); err != nil {
			return err
		}
	}
	for _, k := range names {
		if m := mirrorAmendFile.FindStringSubmatch(k); m != nil {
			if num, _ := strconv.Atoi(m[1]); num >= 1 {
				if _, err := tx.Exec(ctx, `INSERT INTO plans (project, kind, number, text, created_by) VALUES ($1, 'amendment', $2, $3, $4) ON CONFLICT DO NOTHING`,
					p.ID, num, string(files[k]), who); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func decodeMirrorTickets(p *Project, pipe Pipeline, files map[string][]byte, names []string) ([]MirrorTicket, error) {
	var tickets []MirrorTicket
	seenNum := map[int]bool{}
	for _, k := range names {
		m := mirrorTicketFile.FindStringSubmatch(k)
		if m == nil {
			continue
		}
		num, err := strconv.Atoi(m[2])
		if err != nil || num < 1 || seenNum[num] {
			continue
		}
		mt, err := parseMirrorTicket(files[k])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		mt.ID, mt.Key = fmt.Sprintf("%04d", num), fmt.Sprintf("%s-%d", p.Key, num)
		if strings.TrimSpace(mt.Title) == "" || len(mt.Title) > 200 {
			return nil, fmt.Errorf("%s: invalid title", k)
		}
		if !slices.Contains(mirrorKinds, mt.Kind) {
			mt.Kind = "feature"
		}
		if !slices.Contains(mirrorOrigins, mt.Origin) {
			mt.Origin = OriginPlan
		}
		if _, ok := pipe.Stage(mt.Stage); !ok && mt.Stage != settings.StageClosed {
			mt.Stage = settings.StageBacklog
		}
		if mt.Stage != settings.StagePlanning && mt.Stage != settings.StageBacklog && mt.Stage != settings.StageAnalysis && mt.Stage != settings.StageClosed {
			mt.Analysis.Status = StatusReady
		} else if mt.Analysis.Status == "" {
			mt.Analysis.Status = StatusWaiting
		}
		if len(mt.Analysis.MayChange) == 0 && len(mt.MayChange) > 0 {
			mt.Analysis.MayChange = slices.Clone(mt.MayChange)
		}
		if mt.MaxTries < 1 || mt.MaxTries > 20 {
			mt.MaxTries = 3
		}
		if mt.TriesUsed < 0 {
			mt.TriesUsed = 0
		}
		if mt.BudgetTokens < 0 {
			mt.BudgetTokens = 0
		}
		for _, dep := range mt.DependsOn {
			dep = strings.TrimSpace(dep)
			if dep == "" {
				continue
			}
			exists := slices.ContainsFunc(mt.Links, func(l Link) bool { return l.Kind == "depends_on" && l.To == dep })
			if !exists {
				mt.Links = append(mt.Links, Link{Kind: "depends_on", To: dep})
			}
		}
		fillMarkdownSections(&mt, files[k])
		seenNum[num] = true
		tickets = append(tickets, mt)
	}
	sort.Slice(tickets, func(i, j int) bool { return tickets[i].ID < tickets[j].ID })
	return tickets, nil
}

func fillMarkdownSections(mt *MirrorTicket, raw []byte) {
	s := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return
	}
	end := strings.Index(s[4:], "\n---\n")
	if end < 0 {
		return
	}
	secs := parseMarkdownSections(s[4+end+5:])
	if strings.TrimSpace(mt.Why) == "" && secs["why"] != "" {
		mt.Why = secs["why"]
	}
	if strings.TrimSpace(mt.What) == "" && secs["what"] != "" {
		mt.What = secs["what"]
	}
	if strings.TrimSpace(mt.Goal) == "" && secs["goal"] != "" {
		mt.Goal = secs["goal"]
	}
	if mt.Approach == nil && secs["approach"] != "" {
		a := secs["approach"]
		mt.Approach = &a
	}
}

func parseMarkdownSections(body string) map[string]string {
	out := map[string]string{}
	var cur string
	var curLines []string
	flush := func() {
		if cur != "" {
			out[cur] = strings.TrimSpace(strings.Join(curLines, "\n"))
		}
	}
	for _, ln := range strings.Split(body, "\n") {
		if strings.HasPrefix(ln, "## ") {
			flush()
			cur = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(ln, "## ")))
			curLines = nil
			continue
		}
		if cur != "" {
			curLines = append(curLines, ln)
		}
	}
	flush()
	return out
}

func insertMirrorTickets(ctx context.Context, tx pgx.Tx, p *Project, tickets []MirrorTicket, who string) error {
	ids := map[int]int64{}
	maxNum := 0
	for _, mt := range tickets {
		num, _ := strconv.Atoi(mt.ID)
		maxNum = max(maxNum, num)
		pos := max(mt.Position, int64(num)*1000)
		by := mt.CreatedBy
		if by == "" {
			by = who
		}
		maxTries := mt.MaxTries
		if maxTries < 1 || maxTries > 20 {
			maxTries = 3
		}
		var tid int64
		if err := tx.QueryRow(ctx, `INSERT INTO tickets (project, number, title, kind, part, source, why, what, goal, approach, docs,
			docs_reason, stage, closed_reason, skips, skips_reason, origin, created_by, seen, analysis, priority, position, hold, branch, fix_rounds,
			spec_ref, max_tries, tries_used, assigned_agent, budget_tokens)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30) RETURNING id`,
			p.ID, num, mt.Title, mt.Kind, mt.Part, mt.Source, mt.Why, mt.What, mt.Goal, mt.Approach, nonNil(mt.Docs),
			mt.DocsReason, mt.Stage, mt.ClosedReason, nonNil(mt.Skips), mt.SkipsReason, mt.Origin, by, mt.Seen,
			mt.Analysis, min(max(mt.Priority, 0), 3), pos, mt.Hold, mt.Branch, mt.FixRounds,
			mt.Spec, maxTries, max(mt.TriesUsed, 0), mt.AssignedAgent, max(mt.BudgetTokens, int64(0))).Scan(&tid); err != nil {
			return fmt.Errorf("%s: %w", mt.Key, err)
		}
		ids[num] = tid
		for i, c := range mt.Checks {
			if (c.Kind == "cmd" || c.Kind == "judge") && strings.TrimSpace(c.Spec) != "" {
				if _, err := tx.Exec(ctx, `INSERT INTO checks (ticket, position, kind, spec, keeps) VALUES ($1, $2, $3, $4, $5)`,
					tid, i+1, c.Kind, c.Spec, c.Keeps); err != nil {
					return err
				}
			}
		}
		if mt.Stage != settings.StagePlanning && mt.Stage != settings.StageBacklog {
			if _, err := tx.Exec(ctx, `INSERT INTO stage_moves (ticket, from_stage, to_stage, actor, reason, imported)
				VALUES ($1, 'backlog', $2, $3, 'restored from .crew/', true)`, tid, mt.Stage, who); err != nil {
				return err
			}
		}
	}
	if err := insertMirrorLinks(ctx, tx, tickets, ids); err != nil {
		return err
	}
	if len(tickets) == 0 {
		return nil
	}
	why := "restored from .crew/ on main: resume it to let it run"
	_, err := tx.Exec(ctx, `UPDATE projects SET next_number = greatest(next_number, $2),
		state = CASE WHEN state = 'planning' THEN 'paused' ELSE state END,
		pause_reason = CASE WHEN state = 'planning' THEN $3 ELSE pause_reason END,
		ok_at = coalesce(ok_at, now()) WHERE id = $1`, p.ID, maxNum+1, why)
	return err
}

func insertMirrorLinks(ctx context.Context, tx pgx.Tx, tickets []MirrorTicket, ids map[int]int64) error {
	for _, mt := range tickets {
		num, _ := strconv.Atoi(mt.ID)
		for _, l := range mt.Links {
			if !slices.Contains(mirrorLinks, l.Kind) {
				continue
			}
			rest := l.To[strings.LastIndex(l.To, "-")+1:]
			if toNum, err := strconv.Atoi(rest); err == nil && ids[toNum] != 0 && toNum != num {
				if _, err := tx.Exec(ctx, `INSERT INTO ticket_links (from_ticket, to_ticket, kind) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
					ids[num], ids[toNum], l.Kind); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
