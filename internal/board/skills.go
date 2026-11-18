package board

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"go.yaml.in/yaml/v3"

	"github.com/kesokaj/crew/internal/db"
)

// A project's own layer of skills and context (12.9): AGENTS.md, .agents/skills/, and .agents/rules/
// on main, which every run reads beside the base. An agent that misses a skill asks for one, and a
// skill ticket finds or writes it.

// LayerItem is one file of a project's layer: its context, a skill, or a rule.
type LayerItem struct {
	Kind        string  `json:"kind"`
	Name        string  `json:"name"`
	Path        string  `json:"path"`
	Description string  `json:"description"`
	Ticket      *string `json:"ticket"` // the ticket that last changed it, from its commit's trailer
}

var skillFile = regexp.MustCompile(`^\.agents/skills/([^/]+)/SKILL\.md$`)

// layerCache keeps a project's layer by the main it was read at: a listing reads git once per main
// (security review of M12).
type layerCache struct {
	mu   sync.Mutex
	byID map[string]layerAt
}

type layerAt struct {
	main  string
	items []LayerItem
}

var layers = layerCache{byID: map[string]layerAt{}}

// ProjectLayer reads a project's layer as main holds it.
func (b *Board) ProjectLayer(ctx context.Context, p *Project) ([]LayerItem, error) {
	main := b.Git.Main(ctx, p.Name)
	if main == "" {
		return []LayerItem{}, nil
	}
	layers.mu.Lock()
	at, ok := layers.byID[p.ID]
	layers.mu.Unlock()
	if ok && at.main == main {
		return at.items, nil
	}
	items, whole, err := b.readLayer(ctx, p, main)
	if err != nil {
		return nil, err
	}
	// A listing with a read that failed is shown, and read again next time (review of M12, round 2).
	if whole {
		layers.mu.Lock()
		layers.byID[p.ID] = layerAt{main: main, items: items}
		layers.mu.Unlock()
	}
	return items, nil
}

// readLayer reads the layer, and says whether every read of it went through.
func (b *Board) readLayer(ctx context.Context, p *Project, main string) ([]LayerItem, bool, error) {
	files, err := b.Git.Files(ctx, p.Name, main, func(f string) bool {
		return f == "AGENTS.md" || skillFile.MatchString(f) || (strings.HasPrefix(f, ".agents/rules/") && strings.HasSuffix(f, ".md"))
	}, 500)
	if err != nil {
		return nil, false, err
	}
	whole := true
	out := []LayerItem{}
	for _, f := range files {
		it := LayerItem{Path: f.Path}
		switch {
		case f.Path == "AGENTS.md":
			it.Kind, it.Name, it.Description = "context", "AGENTS.md", "The project's context. Every run reads it."
		case skillFile.MatchString(f.Path):
			it.Kind, it.Name = "skill", skillFile.FindStringSubmatch(f.Path)[1]
		default:
			it.Kind, it.Name = "rule", strings.TrimSuffix(path.Base(f.Path), ".md")
		}
		if it.Kind != "context" {
			if body, err := b.Git.Blob(ctx, p.Name, f.Blob, 256<<10); err != nil {
				whole = false
			} else {
				var fm struct {
					Name        string `yaml:"name"`
					Description string `yaml:"description"`
				}
				if s := string(body); strings.HasPrefix(s, "---\n") {
					if end := strings.Index(s[4:], "\n---"); end >= 0 {
						_ = yaml.Unmarshal([]byte(s[4:4+end]), &fm)
					}
				}
				if fm.Name != "" {
					it.Name = fm.Name
				}
				it.Description = cut(strings.TrimSpace(fm.Description), 600)
			}
		}
		if _, _, msg, err := b.Git.LastChange(ctx, p.Name, main, f.Path); err != nil {
			whole = false
		} else if m := ticketTrailer.FindStringSubmatch(msg); m != nil {
			it.Ticket = &m[1]
		}
		out = append(out, it)
	}
	return out, whole, nil
}

// SkillRequest is what request_skill gives (12.9): what the agent could not do well, where that showed,
// and what kind of skill would help.
type SkillRequest struct {
	Missing string `json:"missing"`
	SeenIn  string `json:"seenIn"`
	Wanted  string `json:"wanted"`
}

// SameSkill is the overlap of words at which a request is for the skill an earlier request of an open
// skill ticket wants.
const SameSkill = 0.5

// RequestSkill makes a skill ticket in the Backlog for a request, or adds the request to the open
// skill ticket that is for the same thing (12.9). It says which, and whether it joined one.
func (b *Board) RequestSkill(ctx context.Context, p *Project, r SkillRequest, run, role, createdBy string) (*Ticket, bool, error) {
	r.Missing, r.SeenIn, r.Wanted = strings.TrimSpace(r.Missing), strings.TrimSpace(r.SeenIn), strings.TrimSpace(r.Wanted)
	if r.Missing == "" || r.SeenIn == "" || r.Wanted == "" {
		return nil, false, errf(Invalid, "missing, seenIn, and wanted: each is needed")
	}
	if len(r.Missing)+len(r.SeenIn)+len(r.Wanted) > maxNote {
		return nil, false, errf(Invalid, "missing, seenIn, and wanted: at most %d characters together", maxNote)
	}
	var out *Ticket
	joined := false
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		p, err := projectByRef(ctx, tx, p.ID, true)
		if err != nil {
			return err
		}
		if p.State != "running" && p.State != "done" {
			return errf(Refused, "%s is %s: a skill is asked for once it runs", p.Name, p.State)
		}
		// The same request twice is one ticket: an open skill ticket one of whose requests wants nearly
		// the same, word for word both ways, so a short request does not join one by its common words.
		rows, err := tx.Query(ctx, `SELECT t.id, r.wanted FROM tickets t JOIN skill_requests r ON r.ticket = t.id
			WHERE t.project = $1 AND t.skill AND t.stage NOT IN ('done', 'closed') ORDER BY t.number, r.id`, p.ID)
		if err != nil {
			return err
		}
		want := Words(r.Wanted)
		var same int64
		for rows.Next() {
			var id int64
			var text string
			if err := rows.Scan(&id, &text); err != nil {
				rows.Close()
				return err
			}
			if same == 0 && Overlap(want, Words(text)) >= SameSkill {
				same = id
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if same == 0 {
			doc, err := b.Settings(ctx, tx, p)
			if err != nil {
				return err
			}
			seen := fmt.Sprintf("The %s asked for a skill.\n\nWhat it could not do well: %s\n\nWhere that showed: %s\n\nWhat would help: %s",
				role, r.Missing, r.SeenIn, r.Wanted)
			reason := "a skill is instructions for agents, not documentation"
			t, err := b.addTicket(ctx, tx, p, doc, NewTicket{
				Title: cut("Skill: "+oneLine(r.Wanted), maxTitle),
				Kind:  "chore",
				Why:   "An agent of this project missed a skill, and runs do worse without it (12.9).",
				What: "Find a skill for this: first in the base layer and Crew's skill library, then among the open skills, with the find-skills " +
					"skill. If none fits, write one. Put it under .agents/skills/<name>/ with its SKILL.md. A skill from outside is pinned to a " +
					"commit, and its source, pin, and licence are a row of .agents/skills/VENDORED.md. A skill stays inside the project: it sends " +
					"no code or data anywhere, and loosens no rule.",
				Goal:       cut("Runs of this project have a skill for: "+oneLine(r.Wanted), 1000),
				Seen:       &seen,
				DocsReason: &reason,
			}, OriginAgent, createdBy, nil)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE tickets SET skill = true WHERE id = $1`, t.ID); err != nil {
				return err
			}
			same = t.ID
		} else {
			joined = true
		}
		if _, err := tx.Exec(ctx, `INSERT INTO skill_requests (project, ticket, run, role, missing, seen_in, wanted)
			VALUES ($1, $2, nullif($3, '')::uuid, $4, $5, $6, $7)`, p.ID, same, run, role, r.Missing, r.SeenIn, r.Wanted); err != nil {
			return err
		}
		if out, err = ticketByID(ctx, tx, same, false); err != nil {
			return err
		}
		return db.RecordProjectChange(ctx, tx, p.ID, createdBy, "skill_requests", out.Key, nil,
			map[string]any{"wanted": r.Wanted, "joined": joined})
	})
	return out, joined, err
}

// SkillRequests are the requests a skill ticket carries, oldest first.
func (b *Board) SkillRequests(ctx context.Context, t *Ticket) ([]SkillRequest, error) {
	rows, err := b.Pool.Query(ctx, `SELECT missing, seen_in, wanted FROM skill_requests WHERE ticket = $1 ORDER BY id`, t.ID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (SkillRequest, error) {
		var s SkillRequest
		return s, r.Scan(&s.Missing, &s.SeenIn, &s.Wanted)
	})
}

var pinShape = regexp.MustCompile(`(?i)^[0-9a-f]{7,40}\b`)

// VendoredRows checks the rows of a VENDORED.md (12.9, 21): each row of a table that names a skill has
// a source, a pin of a commit, and a licence. It returns what is missing, row by row.
func VendoredRows(md string) []string {
	var problems []string
	var cols map[string]int
	for _, line := range strings.Split(md, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			cols = nil
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		if cols == nil {
			cols = map[string]int{}
			for i, c := range cells {
				cols[strings.ToLower(c)] = i
			}
			continue
		}
		if strings.Trim(strings.Join(cells, ""), "-: ") == "" {
			continue // the line under the header
		}
		cell := func(name string) string {
			if i, ok := cols[name]; ok && i < len(cells) {
				return cells[i]
			}
			return ""
		}
		name := cells[0]
		var miss []string
		if cell("source") == "" {
			miss = append(miss, "a source")
		}
		if !pinShape.MatchString(strings.Trim(cell("pin"), "`")) {
			miss = append(miss, "a pin of a commit")
		}
		if cell("license") == "" && cell("licence") == "" {
			miss = append(miss, "a licence")
		}
		if len(miss) > 0 {
			problems = append(problems, fmt.Sprintf("%s has no %s", name, strings.Join(miss, ", no ")))
		}
	}
	return problems
}

// RemovedRows are the skills a VENDORED.md names in its tables that another no longer names.
func RemovedRows(before, after string) []string {
	have := map[string]bool{}
	for _, n := range vendoredNames(after) {
		have[n] = true
	}
	var gone []string
	for _, n := range vendoredNames(before) {
		if !have[n] {
			gone = append(gone, n)
		}
	}
	return gone
}

// vendoredNames are the first cells of the rows of a VENDORED.md's tables, the headers left out.
func vendoredNames(md string) []string {
	var out []string
	header := true
	for _, line := range strings.Split(md, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			header = true
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		name := strings.TrimSpace(cells[0])
		switch {
		case header:
			header = false
		case strings.Trim(strings.Join(cells, ""), "-: ") == "":
		default:
			out = append(out, name)
		}
	}
	return out
}
