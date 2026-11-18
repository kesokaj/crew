// Package export writes a project out as a folder of files, and reads one back (design 6.2): for
// reading, for moving a project to another machine (R44), and for keeping a copy. Crew never reads
// an export while it runs. The folder travels as a tar stream: crew admin writes and reads the
// stream, and crew-ctl turns it into a folder on the machine and back.
//
// What is in it in M4: board.yaml, project.md, the plan, every ticket with its checks, links, and
// moves, and repo.bundle with every branch and tag of the code. Records, subtasks, and runs join it
// with the milestones that make them. Conversations, trajectories, and credentials are never in it.
package export

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.yaml.in/yaml/v3"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/settings"
)

// Format is the version of the layout. An export of another version is refused.
const Format = 1

// ProjectFile is project.md's front matter.
type ProjectFile struct {
	Format     int        `yaml:"format"`
	Exported   time.Time  `yaml:"exported"`
	CrewVer    string     `yaml:"crew_version"`
	Name       string     `yaml:"name"`
	Key        string     `yaml:"key"`
	Goal       string     `yaml:"goal"`
	State      string     `yaml:"state"`
	Pause      *string    `yaml:"pause_reason"`
	RepoSource string     `yaml:"repo_source"`
	NextNumber int        `yaml:"next_number"`
	OKAt       *time.Time `yaml:"ok_at"`
	Created    time.Time  `yaml:"created"`
}

// TaskFile is task.md's front matter: a ticket as 6.3 shows it, with its moves.
type TaskFile struct {
	ID           string            `yaml:"id"`
	Key          string            `yaml:"key"`
	Title        string            `yaml:"title"`
	Kind         string            `yaml:"kind"`
	Part         *string           `yaml:"part"`
	Source       *board.Source     `yaml:"source"`
	Why          string            `yaml:"why"`
	What         string            `yaml:"what"`
	Goal         string            `yaml:"goal"`
	Checks       []board.CheckSpec `yaml:"checks"`
	Approach     *string           `yaml:"approach"`
	Docs         []string          `yaml:"docs"`
	DocsReason   *string           `yaml:"docs_reason"`
	Stage        string            `yaml:"stage"`
	ClosedReason *string           `yaml:"closed_reason"`
	Skips        []string          `yaml:"skips"`
	SkipsReason  *string           `yaml:"skips_reason"`
	Origin       string            `yaml:"origin"`
	CreatedBy    string            `yaml:"created_by"`
	Seen         *string           `yaml:"seen"`
	Analysis     board.Analysis    `yaml:"analysis"`
	Links        []board.Link      `yaml:"links"`
	Priority     int               `yaml:"priority"`
	Position     int64             `yaml:"position"`
	// BudgetUSD and OverBudget are read from an export made before 0.53, and left out: a ticket has
	// no budget since (7.7). An export writes neither.
	BudgetUSD   *float64       `yaml:"budget_usd,omitempty"`
	Hold        bool           `yaml:"hold"`
	NoticeUntil *time.Time     `yaml:"notice_until"`
	Agents      map[string]any `yaml:"agents"`
	Network     string         `yaml:"network"`
	Branch      *string        `yaml:"branch"`
	PullRequest *int           `yaml:"pull_request"`
	FixRounds   int            `yaml:"fix_rounds"`
	OverBudget  *time.Time     `yaml:"over_budget,omitempty"`
	StageAt     time.Time      `yaml:"stage_at"`
	Created     time.Time      `yaml:"created"`
	Updated     time.Time      `yaml:"updated"`
	Moves       []Move         `yaml:"moves"`
}

// Move is one move of a ticket, with the checks that let it pass. Imported says the move came in
// with an import, here or on a machine before: it was not made by this board.
type Move struct {
	From     string         `yaml:"from"`
	To       string         `yaml:"to"`
	Actor    string         `yaml:"actor"`
	Reason   *string        `yaml:"reason,omitempty"`
	At       time.Time      `yaml:"at"`
	Imported bool           `yaml:"imported,omitempty"`
	Results  []board.Result `yaml:"checks,omitempty"`
}

// Exporter writes and reads exports.
type Exporter struct {
	Pool    *pgxpool.Pool
	Board   *board.Board
	Git     *gitstore.Store
	Version string
	Now     func() time.Time
}

func (x *Exporter) now() time.Time {
	if x.Now != nil {
		return x.Now()
	}
	return time.Now()
}

// Write writes the project ref out as a tar stream to w.
//
// The rows are read in one transaction that sees one snapshot, so a ticket that moves while the
// export is written is in it as it was at one moment, its moves with it.
func (x *Exporter) Write(ctx context.Context, ref string, w io.Writer) error {
	tx, err := x.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a read-only transaction: ending it has nothing to report
	p, err := board.ProjectIn(ctx, tx, ref)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(w)
	at := x.now().UTC().Truncate(time.Second)
	put := func(name string, body []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), ModTime: at, Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err := tw.Write(body)
		return err
	}
	doc, err := x.Board.Settings(ctx, tx, p)
	if err != nil {
		return err
	}
	b, err := settings.FormatProject(doc)
	if err != nil {
		return err
	}
	if err := put("board.yaml", b); err != nil {
		return err
	}
	var next int
	if err := tx.QueryRow(ctx, `SELECT next_number FROM projects WHERE id = $1`, p.ID).Scan(&next); err != nil {
		return err
	}
	pf := ProjectFile{Format: Format, Exported: at, CrewVer: x.Version, Name: p.Name, Key: p.Key, Goal: p.Goal,
		State: p.State, Pause: p.PauseReason, RepoSource: p.RepoSource, NextNumber: next, OKAt: p.OKAt, Created: p.CreatedAt}
	if b, err = document(pf, "# "+p.Name+"\n\n"+p.Goal+"\n"); err != nil {
		return err
	}
	if err := put("project.md", b); err != nil {
		return err
	}
	if err := x.writePlan(ctx, tx, p, put); err != nil {
		return err
	}
	ts, err := board.TicketsIn(ctx, tx, p.ID)
	if err != nil {
		return err
	}
	for _, t := range ts {
		tf, err := x.taskFile(ctx, tx, t)
		if err != nil {
			return err
		}
		if b, err = document(tf, ""); err != nil {
			return err
		}
		if err := put(fmt.Sprintf("tasks/%04d-%s/task.md", t.Number, slug(t.Title)), b); err != nil {
			return err
		}
	}
	// The bundle is written to a file first: a tar entry gives its size before its content.
	f, err := os.CreateTemp("", "crew-export-*.bundle")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := x.Git.Bundle(ctx, p.Name, f); err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: "repo.bundle", Mode: 0o644, Size: fi.Size(), ModTime: at, Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	if _, err := io.Copy(tw, f); err != nil {
		return err
	}
	return tw.Close()
}

func (x *Exporter) writePlan(ctx context.Context, q db.Querier, p *board.Project, put func(string, []byte) error) error {
	rows, err := q.Query(ctx, `SELECT kind, number, text, created_by, created_at FROM plans WHERE project = $1 ORDER BY kind, number`, p.ID)
	if err != nil {
		return err
	}
	type row struct {
		kind, text, by string
		n              int
		at             time.Time
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		return x, r.Scan(&x.kind, &x.n, &x.text, &x.by, &x.at)
	})
	if err != nil {
		return err
	}
	for _, r := range list {
		meta := planMeta{Number: r.n, CreatedBy: r.by, Created: r.at}
		b, err := document(meta, r.text)
		if err != nil {
			return err
		}
		if err := put(planPath(r.kind, r.n), b); err != nil {
			return err
		}
	}
	return nil
}

type planMeta struct {
	Number    int       `yaml:"number"`
	CreatedBy string    `yaml:"created_by"`
	Created   time.Time `yaml:"created"`
}

// Where each kind of plan row is in an export: the original and every version of the breakdown,
// the amendments, and the comments the breakdown was sent back with.
func planPath(kind string, n int) string {
	switch kind {
	case "original":
		return "plan/original.md"
	case "breakdown":
		return fmt.Sprintf("plan/breakdown/%02d.md", n)
	case "amendment":
		return fmt.Sprintf("plan/amendments/%02d.md", n)
	}
	return fmt.Sprintf("plan/sent-back/%02d.md", n)
}

func (x *Exporter) taskFile(ctx context.Context, q db.Querier, t *board.Ticket) (TaskFile, error) {
	tf := TaskFile{ID: fmt.Sprintf("%04d", t.Number), Key: t.Key, Title: t.Title, Kind: t.Kind, Part: t.Part, Source: t.Source,
		Why: t.Why, What: t.What, Goal: t.Goal, Checks: t.Checks, Approach: t.Approach, Docs: t.Docs, DocsReason: t.DocsReason,
		Stage: t.Stage, ClosedReason: t.ClosedReason, Skips: t.Skips, SkipsReason: t.SkipsReason, Origin: t.Origin,
		CreatedBy: t.CreatedBy, Seen: t.Seen, Analysis: t.Analysis, Links: t.Links, Priority: t.Priority, Position: t.Position,
		Hold: t.Hold, NoticeUntil: t.NoticeUntil, Branch: t.Branch, FixRounds: t.FixRounds,
		StageAt: t.StageAt, Created: t.CreatedAt, Updated: t.UpdatedAt}
	if err := q.QueryRow(ctx, `SELECT agents, network, pull_request FROM tickets WHERE id = $1`, t.ID).Scan(&tf.Agents, &tf.Network, &tf.PullRequest); err != nil {
		return tf, err
	}
	rows, err := q.Query(ctx, `SELECT id, from_stage, to_stage, actor, reason, at, imported FROM stage_moves WHERE ticket = $1 ORDER BY id`, t.ID)
	if err != nil {
		return tf, err
	}
	type mv struct {
		id int64
		m  Move
	}
	moves, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (mv, error) {
		var x mv
		return x, r.Scan(&x.id, &x.m.From, &x.m.To, &x.m.Actor, &x.m.Reason, &x.m.At, &x.m.Imported)
	})
	if err != nil {
		return tf, err
	}
	for _, m := range moves {
		rows, err := q.Query(ctx, `SELECT name, passed, detail FROM check_results WHERE move = $1 ORDER BY id`, m.id)
		if err != nil {
			return tf, err
		}
		if m.m.Results, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (board.Result, error) {
			var res board.Result
			return res, r.Scan(&res.Name, &res.Passed, &res.Detail)
		}); err != nil {
			return tf, err
		}
		tf.Moves = append(tf.Moves, m.m)
	}
	return tf, nil
}

// document writes front matter and a body as one markdown file.
func document(meta any, body string) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("---\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(meta); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	buf.WriteString("---\n")
	if body != "" {
		buf.WriteString("\n")
		buf.WriteString(body)
	}
	return buf.Bytes(), nil
}

// readDocument splits a markdown file into its front matter, decoded into meta, and its body.
func readDocument(b []byte, meta any) (string, error) {
	s := string(b)
	if !strings.HasPrefix(s, "---\n") {
		return "", errors.New("no front matter")
	}
	end := strings.Index(s[4:], "\n---\n")
	if end < 0 {
		return "", errors.New("the front matter does not end")
	}
	dec := yaml.NewDecoder(strings.NewReader(s[4 : 4+end+1]))
	dec.KnownFields(true)
	if err := dec.Decode(meta); err != nil {
		return "", err
	}
	return strings.TrimPrefix(s[4+end+5:], "\n"), nil
}

var notSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(title string) string {
	s := strings.Trim(notSlug.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	if s == "" {
		return "ticket"
	}
	return s
}

// As names the project an import makes. Empty keeps the export's own.
type As struct {
	Name string
	Key  string
}

// What an import takes: the files of the layout, each with its size limit.
var (
	taskPath = regexp.MustCompile(`^tasks/(\d{4,})-[a-z0-9-]+/task\.md$`)
	planFile = regexp.MustCompile(`^plan/(original\.md|(breakdown|amendments|sent-back)/(\d{2,})\.md)$`)
)

const (
	maxFile   = 4 << 20
	maxFiles  = 100000
	maxTotal  = 256 << 20 // every file but the bundle, together
	maxBundle = 8 << 30
)

// Read reads an export from r and makes the project it holds, with its repositories, under the name
// and the key of as or of the export. A name or a key that is taken is refused. Either the project and
// its repositories are made whole, or nothing is left.
//
// What is read is data from a file, which anyone who could write the folder may have changed. Every
// path, field, and value is checked as an API request would be, and the board's own rules are checked
// too: each ticket's moves go to the right from Planning to its stage, a ticket from In progress on
// was approved, and its skips are ones Analysis could have made. What the file cannot prove, it does
// not decide: the project lands paused, so it runs only when the operator resumes it, and every move
// that came with it is marked imported. Nothing in it runs.
func (x *Exporter) Read(ctx context.Context, r io.Reader, as As, who string) (*board.Project, error) {
	files := map[string][]byte{}
	var bundle string
	var total int64
	defer func() {
		if bundle != "" {
			os.Remove(bundle)
		}
	}()
	tr := tar.NewReader(r)
	for n := 0; ; n++ {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("the export: %w", err)
		}
		if n > maxFiles {
			return nil, errors.New("the export holds too many files")
		}
		name := path.Clean(strings.TrimPrefix(h.Name, "./"))
		if h.Typeflag == tar.TypeDir || name == "." {
			continue
		}
		if h.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("the export: %s is not a plain file", h.Name)
		}
		switch {
		case name == "repo.bundle":
			if bundle != "" {
				return nil, errors.New("the export: repo.bundle is there twice")
			}
			if h.Size > maxBundle {
				return nil, errors.New("the export: repo.bundle is too large")
			}
			f, err := os.CreateTemp("", "crew-import-*.bundle")
			if err != nil {
				return nil, err
			}
			bundle = f.Name()
			_, err = io.Copy(f, io.LimitReader(tr, maxBundle))
			f.Close()
			if err != nil {
				return nil, err
			}
		case name == "board.yaml", name == "project.md", planFile.MatchString(name), taskPath.MatchString(name):
			if h.Size > maxFile {
				return nil, fmt.Errorf("the export: %s is too large", name)
			}
			if _, dup := files[name]; dup {
				return nil, fmt.Errorf("the export: %s is there twice", name)
			}
			if total += h.Size; total > maxTotal {
				return nil, errors.New("the export's files are too large together")
			}
			b, err := io.ReadAll(io.LimitReader(tr, maxFile))
			if err != nil {
				return nil, err
			}
			files[name] = b
		default:
			// A file of a later layout, or of a record this version does not keep: not read.
		}
	}
	for _, need := range []string{"project.md", "board.yaml", "plan/original.md"} {
		if files[need] == nil {
			return nil, fmt.Errorf("the export has no %s", need)
		}
	}
	if bundle == "" {
		return nil, errors.New("the export has no repo.bundle")
	}
	var pf ProjectFile
	if _, err := readDocument(files["project.md"], &pf); err != nil {
		return nil, fmt.Errorf("project.md: %w", err)
	}
	if pf.Format != Format {
		return nil, fmt.Errorf("project.md: format %d; this version of Crew reads format %d", pf.Format, Format)
	}
	oldKey := pf.Key
	if as.Name != "" {
		pf.Name = as.Name
	}
	if as.Key != "" {
		pf.Key = as.Key
	}
	if !settings.ProjectName.MatchString(pf.Name) || settings.UUIDShaped(pf.Name) || !settings.ProjectKey.MatchString(pf.Key) || !settings.ProjectKey.MatchString(oldKey) {
		return nil, errors.New("project.md: the name or the key is not one Crew takes")
	}
	if !slices.Contains([]string{"planning", "running", "paused", "done", "archived"}, pf.State) {
		return nil, fmt.Errorf("project.md: state %q", pf.State)
	}
	doc, err := settings.Parse(files["board.yaml"])
	if err != nil {
		return nil, fmt.Errorf("board.yaml: %w", err)
	}
	doc["project"], doc["key"] = pf.Name, pf.Key
	defaults, err := settings.ProjectDefaults(pf.Name, pf.Key)
	if err != nil {
		return nil, err
	}
	// An export made before 0.53 still names the settings of a budget.
	settings.DropRetired(doc)
	if err := settings.ValidateProject(doc, defaults); err != nil {
		return nil, fmt.Errorf("board.yaml:\n%w", err)
	}
	tasks, err := readTasks(files, oldKey, pf.Key)
	if err != nil {
		return nil, err
	}
	pipe := board.PipelineOf(settings.Merge(defaults, doc))
	for i := range tasks {
		if err := boardRules(&tasks[i], pipe); err != nil {
			return nil, fmt.Errorf("%s: %w", tasks[i].Key, err)
		}
	}
	// A project that ran here runs only when the operator says so. One in planning still waits for
	// its OK, and an archived one stays archived.
	if pf.State == "running" || pf.State == "paused" || pf.State == "done" {
		why := "imported: resume it to let it run here"
		pf.State, pf.Pause = "paused", &why
	}
	var taken bool
	if err := x.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projects WHERE name = $1 OR key = $2)`, pf.Name, pf.Key).Scan(&taken); err != nil {
		return nil, err
	}
	if taken || x.Git.Exists(pf.Name) {
		return nil, fmt.Errorf("a project with the name %s or the key %s exists: import it with another name and key", pf.Name, pf.Key)
	}
	if err := x.Git.CreateFromBundle(ctx, pf.Name, bundle); err != nil {
		return nil, err
	}
	p, err := x.insert(ctx, pf, doc, files, tasks, oldKey, who)
	if err != nil {
		x.Git.Remove(pf.Name)
		return nil, err
	}
	return p, nil
}

// boardRules checks a ticket against the rules the board keeps when it makes the ticket itself: its
// stage is on the board, its moves go only to the right from where it began and end where it is, a
// ticket past Analysis was approved, and its skips are optional stages, never Review for a ticket of
// an agent (7.4, 7.6). Every move is marked imported.
func boardRules(t *TaskFile, pipe board.Pipeline) error {
	if _, ok := pipe.Stage(t.Stage); !ok && t.Stage != settings.StageClosed {
		return fmt.Errorf("stage %q is not on the board", t.Stage)
	}
	// A ticket starts in Planning, or in Backlog when it was added after the OK (AddTicket).
	at := settings.StagePlanning
	if len(t.Moves) > 0 && t.Moves[0].From == settings.StageBacklog {
		at = settings.StageBacklog
	} else if len(t.Moves) == 0 && t.Stage == settings.StageBacklog {
		at = settings.StageBacklog
	}
	for i := range t.Moves {
		m := &t.Moves[i]
		if m.From != at || !pipe.Right(m.From, m.To) {
			return fmt.Errorf("a move from %s to %s does not follow from %s to the right", m.From, m.To, at)
		}
		if f := pipe.FixedBetween(m.From, m.To); f != "" {
			return fmt.Errorf("a move from %s to %s passes over %s, which no ticket passes over", m.From, m.To, f)
		}
		// An optional stage that is on is passed over only when the ticket skips it, as the board's
		// own next stage is chosen (Pipeline.Next).
		if m.To != settings.StageClosed {
			if next, ok := pipe.Next(m.From, t.Skips); !ok || next.ID != m.To {
				return fmt.Errorf("a move from %s to %s passes over %s, which is on and which it does not skip", m.From, m.To, next.ID)
			}
		}
		at = m.To
		m.Imported = true
	}
	if at != t.Stage {
		return fmt.Errorf("its moves end in %s, and it is in %s", at, t.Stage)
	}
	past := t.Stage != settings.StagePlanning && t.Stage != settings.StageBacklog && t.Stage != settings.StageAnalysis && t.Stage != settings.StageClosed
	if past && t.Analysis.Status != board.StatusReady {
		return fmt.Errorf("it is in %s, and its analysis is %q, not ready", t.Stage, t.Analysis.Status)
	}
	optional := pipe.Optional()
	for _, s := range t.Skips {
		if !slices.Contains(optional, s) {
			return fmt.Errorf("skips %q, which is not an optional stage", s)
		}
		if s == settings.StageReview && board.MustReview(t.Origin) {
			return fmt.Errorf("a ticket of an %s cannot skip Review", t.Origin)
		}
	}
	if len(t.Agents) > 0 {
		if errs := settings.CheckTicketAgents(t.Agents); len(errs) > 0 {
			return fmt.Errorf("agents: %s", strings.Join(errs, "; "))
		}
	}
	return nil
}

// readTasks reads and checks every task.md. Links and keys name the export's key, and are read under
// the new one.
func readTasks(files map[string][]byte, oldKey, newKey string) ([]TaskFile, error) {
	var names []string
	for n := range files {
		if taskPath.MatchString(n) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	seen := map[int]bool{}
	var out []TaskFile
	for _, n := range names {
		var t TaskFile
		if _, err := readDocument(files[n], &t); err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		num, err := strconv.Atoi(t.ID)
		if err != nil || num < 1 || t.Key != fmt.Sprintf("%s-%d", oldKey, num) || seen[num] {
			return nil, fmt.Errorf("%s: the id %q and the key %q do not agree", n, t.ID, t.Key)
		}
		if m := taskPath.FindStringSubmatch(n); m == nil || strings.TrimLeft(m[1], "0") != strconv.Itoa(num) {
			return nil, fmt.Errorf("%s: the folder does not hold ticket %d", n, num)
		}
		seen[num] = true
		t.Key = fmt.Sprintf("%s-%d", newKey, num)
		if err := checkTask(&t, oldKey, newKey); err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		out = append(out, t)
	}
	deps := map[string][]string{}
	for _, t := range out {
		for _, l := range t.Links {
			n, _ := strconv.Atoi(strings.TrimPrefix(l.To, newKey+"-"))
			if !seen[n] {
				return nil, fmt.Errorf("%s links to %s, which the export does not hold", t.Key, l.To)
			}
			if l.To == t.Key {
				return nil, fmt.Errorf("%s links to itself", t.Key)
			}
			if l.Kind == "depends_on" {
				deps[t.Key] = append(deps[t.Key], l.To)
			}
		}
	}
	if k := circle(deps); k != "" {
		return nil, fmt.Errorf("%s depends on itself through its dependencies: the board takes no circle (7.9)", k)
	}
	return out, nil
}

// circle returns a ticket on a circle of depends_on, or "".
func circle(deps map[string][]string) string {
	const (
		open = 1
		done = 2
	)
	state := map[string]int{}
	var visit func(k string) string
	visit = func(k string) string {
		switch state[k] {
		case open:
			return k
		case done:
			return ""
		}
		state[k] = open
		for _, d := range deps[k] {
			if c := visit(d); c != "" {
				return c
			}
		}
		state[k] = done
		return ""
	}
	keys := make([]string, 0, len(deps))
	for k := range deps {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if c := visit(k); c != "" {
			return c
		}
	}
	return ""
}

var (
	kinds     = []string{"feature", "fix", "chore", "docs"}
	origins   = []string{"plan", "agent", "operator", "token", "conversation", "chat"}
	linkKinds = []string{"depends_on", "created_from", "duplicates", "fixes_bug", "replaces"}
	statuses  = []string{"", board.StatusWaiting, board.StatusProposed, board.StatusAnalysing, board.StatusReady, board.StatusSkipped, board.StatusNotice, board.StatusOperator}
	networks  = []string{"inherit", "internet", "model_only", "lan"}
)

func checkTask(t *TaskFile, oldKey, newKey string) error {
	switch {
	case strings.TrimSpace(t.Title) == "" || len(t.Title) > 200 || strings.ContainsAny(t.Title, "\n\r"):
		return errors.New("title: one line of at most 200 characters")
	case !slices.Contains(kinds, t.Kind):
		return fmt.Errorf("kind %q", t.Kind)
	case !slices.Contains(origins, t.Origin):
		return fmt.Errorf("origin %q", t.Origin)
	case !slices.Contains(statuses, t.Analysis.Status):
		return fmt.Errorf("analysis status %q", t.Analysis.Status)
	case t.Priority < 0 || t.Priority > 3:
		return fmt.Errorf("priority %d", t.Priority)
	case (t.Stage == settings.StageClosed) != (t.ClosedReason != nil):
		return errors.New("a closed ticket says why, and only a closed one")
	case t.ClosedReason != nil && *t.ClosedReason != "discarded" && *t.ClosedReason != "cancelled":
		return fmt.Errorf("closed_reason %q", *t.ClosedReason)
	case t.Network != "" && !slices.Contains(networks, t.Network):
		return fmt.Errorf("network %q", t.Network)
	case t.Branch != nil && !gitstore.ValidBranch(*t.Branch):
		return fmt.Errorf("branch %q", *t.Branch)
	case len(t.Why) > 20000 || len(t.What) > 20000 || len(t.Goal) > 20000:
		return errors.New("why, what, and goal: at most 20000 characters each")
	}
	for _, c := range t.Checks {
		if (c.Kind != "cmd" && c.Kind != "judge") || strings.TrimSpace(c.Spec) == "" || strings.ContainsAny(c.Spec, "\n\r\x00") {
			return errors.New("checks: a cmd or a judge of one line")
		}
	}
	for i, l := range t.Links {
		rest, ok := strings.CutPrefix(l.To, oldKey+"-")
		if n, err := strconv.Atoi(rest); !ok || err != nil || n < 1 || !slices.Contains(linkKinds, l.Kind) {
			return fmt.Errorf("link %v", l)
		}
		t.Links[i].To = newKey + "-" + rest
	}
	if l := t.Analysis.LookAgain; l != nil {
		if !slices.Contains([]string{"part_done", "ticket_done", "date", "all_done"}, l.Kind) {
			return fmt.Errorf("look_again %q", l.Kind)
		}
		if l.Kind == "ticket_done" {
			rest, ok := strings.CutPrefix(l.Value, oldKey+"-")
			if n, err := strconv.Atoi(rest); !ok || err != nil || n < 1 {
				return fmt.Errorf("look_again: %q is not a ticket of the export", l.Value)
			}
			l.Value = newKey + "-" + rest
		}
	}
	for _, m := range t.Moves {
		if m.From == "" || m.To == "" || m.Actor == "" {
			return errors.New("a move needs its stages and who made it")
		}
	}
	return nil
}

func (x *Exporter) insert(ctx context.Context, pf ProjectFile, doc settings.Doc, files map[string][]byte, tasks []TaskFile, oldKey, who string) (*board.Project, error) {
	var id string
	err := pgx.BeginFunc(ctx, x.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO projects (id, name, key, goal, state, pause_reason, repo_source, next_number, ok_at, created_at)
			VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id::text`,
			pf.Name, pf.Key, pf.Goal, pf.State, pf.Pause, "import", nextNumber(pf, tasks), pf.OKAt, pf.Created).Scan(&id); err != nil {
			return err
		}
		for name, b := range files {
			m := planFile.FindStringSubmatch(name)
			if m == nil {
				continue
			}
			var meta planMeta
			text, err := readDocument(b, &meta)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			kind := map[string]string{"": "original", "breakdown": "breakdown", "amendments": "amendment", "sent-back": "sent_back"}[m[2]]
			if kind == "original" {
				meta.Number = 1
			} else if n, _ := strconv.Atoi(m[3]); n != meta.Number {
				return fmt.Errorf("%s: its number is %d", name, meta.Number)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO plans (project, kind, number, text, created_by, created_at) VALUES ($1, $2, $3, $4, $5, $6)`,
				id, kind, meta.Number, text, meta.CreatedBy, meta.Created); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO git_repos (name, project, seeded_from) VALUES ($1, $2, 'import')`, pf.Name, id); err != nil {
			return err
		}
		if err := settings.SaveProject(ctx, tx, id, pf.Name, pf.Key, doc, who); err != nil {
			return err
		}
		ids := map[int]int64{}
		for _, t := range tasks {
			num, _ := strconv.Atoi(t.ID)
			network := t.Network
			if network == "" {
				network = "inherit"
			}
			agents := t.Agents
			if agents == nil {
				agents = map[string]any{}
			}
			var tid int64
			err := tx.QueryRow(ctx, `INSERT INTO tickets (project, number, title, kind, part, source, why, what, goal, approach, docs,
				docs_reason, stage, closed_reason, skips, skips_reason, origin, created_by, seen, analysis, priority, position,
				hold, notice_until, agents, network, branch, pull_request, fix_rounds, stage_at, created_at, updated_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23,
				$24, $25, $26, $27, $28, $29, $30, $31, $32) RETURNING id`,
				id, num, t.Title, t.Kind, t.Part, t.Source, t.Why, t.What, t.Goal, t.Approach, nonNil(t.Docs), t.DocsReason,
				t.Stage, t.ClosedReason, nonNil(t.Skips), t.SkipsReason, t.Origin, t.CreatedBy, t.Seen, t.Analysis, t.Priority,
				t.Position, t.Hold, t.NoticeUntil, agents, network, t.Branch, t.PullRequest, t.FixRounds,
				t.StageAt, t.Created, t.Updated).Scan(&tid)
			if err != nil {
				return fmt.Errorf("%s: %w", t.Key, err)
			}
			ids[num] = tid
			for i, c := range t.Checks {
				if _, err := tx.Exec(ctx, `INSERT INTO checks (ticket, position, kind, spec) VALUES ($1, $2, $3, $4)`, tid, i+1, c.Kind, c.Spec); err != nil {
					return err
				}
			}
			for _, m := range t.Moves {
				var mid int64
				if err := tx.QueryRow(ctx, `INSERT INTO stage_moves (ticket, from_stage, to_stage, actor, reason, at, imported) VALUES ($1, $2, $3, $4, $5, $6, true) RETURNING id`,
					tid, m.From, m.To, m.Actor, m.Reason, m.At).Scan(&mid); err != nil {
					return err
				}
				for _, r := range m.Results {
					if _, err := tx.Exec(ctx, `INSERT INTO check_results (ticket, move, stage, name, passed, detail, at, imported) VALUES ($1, $2, $3, $4, $5, $6, $7, true)`,
						tid, mid, m.From, r.Name, r.Passed, r.Detail, m.At); err != nil {
						return err
					}
				}
			}
		}
		for _, t := range tasks {
			num, _ := strconv.Atoi(t.ID)
			for _, l := range t.Links {
				n, _ := strconv.Atoi(l.To[strings.LastIndex(l.To, "-")+1:])
				if _, err := tx.Exec(ctx, `INSERT INTO ticket_links (from_ticket, to_ticket, kind) VALUES ($1, $2, $3)`, ids[num], ids[n], l.Kind); err != nil {
					return fmt.Errorf("%s: %w", t.Key, err)
				}
			}
		}
		return db.RecordProjectChange(ctx, tx, id, who, "projects", id, nil,
			map[string]any{"imported": map[string]any{"from": pf.Name, "key": oldKey, "exported": pf.Exported, "crewVersion": pf.CrewVer, "tickets": len(tasks)}})
	})
	if err != nil {
		return nil, err
	}
	return x.Board.Project(ctx, id)
}

// nextNumber is the counter of ticket numbers: past the export's own, and past every ticket in it.
func nextNumber(pf ProjectFile, tasks []TaskFile) int {
	n := pf.NextNumber
	for _, t := range tasks {
		if num, _ := strconv.Atoi(t.ID); num >= n {
			n = num + 1
		}
	}
	return max(n, 1)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
