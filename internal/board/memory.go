package board

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/redact"
)

// Memory is a project's short hints for later runs (9.3). Any role writes an entry, which is active at
// once; the lead keeps them healthy in housekeeping, and the operator's entries win over an agent's.

// MemoryKinds are the kinds of an entry.
var MemoryKinds = []string{"convention", "fact", "decision", "lesson"}

// NearDuplicate is the overlap of words at which a new entry is the same as one there is (9.3).
const NearDuplicate = 0.8

// Entry is one memory entry.
type Entry struct {
	ID            int64     `json:"-"`
	Number        int       `json:"number"`
	Kind          string    `json:"kind"`
	Title         string    `json:"title"`
	Body          string    `json:"body"`
	AppliesTo     []string  `json:"appliesTo"`
	Paths         []string  `json:"paths"`
	Keywords      []string  `json:"keywords"`
	SourceRun     *string   `json:"sourceRun"`
	SourceTicket  *string   `json:"sourceTicket"`
	SourceFeedbk  *int      `json:"sourceFeedback"`
	Author        string    `json:"author"`
	Status        string    `json:"status"`
	RetiredReason *string   `json:"retiredReason"`
	ChangedBy     *string   `json:"changedBy"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// NewEntry is what remember and the operator give.
type NewEntry struct {
	Kind      string   `json:"kind"`
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	AppliesTo []string `json:"appliesTo"`
	Paths     []string `json:"paths"`
	Keywords  []string `json:"keywords"`
}

// MemorySource is where an entry comes from: the run, its ticket, and the feedback it carries out.
type MemorySource struct {
	Run      string
	Ticket   int64
	Feedback int64
}

const entryCols = `m.id, m.number, m.kind, m.title, m.body, m.applies_to, m.paths, m.keywords, m.source_run::text,
	(SELECT p.key || '-' || t.number FROM tickets t JOIN projects p ON p.id = t.project WHERE t.id = m.source_ticket),
	(SELECT f.number FROM feedback f WHERE f.id = m.source_feedback), m.author, m.status, m.retired_reason, m.changed_by,
	m.created_at, m.updated_at`

func scanEntry(r pgx.Row) (*Entry, error) {
	var e Entry
	err := r.Scan(&e.ID, &e.Number, &e.Kind, &e.Title, &e.Body, &e.AppliesTo, &e.Paths, &e.Keywords, &e.SourceRun,
		&e.SourceTicket, &e.SourceFeedbk, &e.Author, &e.Status, &e.RetiredReason, &e.ChangedBy, &e.CreatedAt, &e.UpdatedAt)
	return &e, err
}

func (n *NewEntry) check() error {
	n.Title, n.Body = strings.TrimSpace(n.Title), strings.TrimSpace(n.Body)
	if !slices.Contains(MemoryKinds, n.Kind) {
		return errf(Invalid, "kind: one of %s", strings.Join(MemoryKinds, ", "))
	}
	if n.Title == "" || utf8.RuneCountInString(n.Title) > 200 || strings.ContainsAny(n.Title, "\n\r") {
		return errf(Invalid, "title: one line of at most 200 characters")
	}
	if utf8.RuneCountInString(n.Body) > 4000 {
		return errf(Invalid, "body: at most 4000 characters")
	}
	if len(n.AppliesTo) == 0 {
		n.AppliesTo = []string{"all"}
	}
	for _, list := range [][]string{n.AppliesTo, n.Paths, n.Keywords} {
		if len(list) > 20 {
			return errf(Invalid, "at most 20 roles, paths, or keywords")
		}
		for i := range list {
			if list[i] = strings.TrimSpace(list[i]); list[i] == "" || len(list[i]) > 200 || strings.ContainsAny(list[i], "\n\r") {
				return errf(Invalid, "a role, a path, or a keyword is one line of at most 200 characters")
			}
		}
	}
	for _, p := range n.Paths {
		if _, err := path.Match(strings.ReplaceAll(p, "**", "*"), ""); err != nil {
			return errf(Invalid, "paths: %q is not a pattern", p)
		}
	}
	// A secret in memory would reach every later run (9.3).
	if HoldsSecret(nil, append([]string{n.Title, n.Body}, slices.Concat(n.AppliesTo, n.Paths, n.Keywords)...)...) {
		return errf(Invalid, "the entry looks like it holds a secret, and memory reaches every run: leave the secret out")
	}
	return nil
}

// HoldsSecret says one of the texts holds one of the values, or the shape of a secret.
func HoldsSecret(values []string, texts ...string) bool {
	r := redact.New(values...)
	for _, t := range texts {
		if r.Text(t) != t {
			return true
		}
	}
	return false
}

// Words are the lower-cased words of a text, for the overlap of two texts.
func Words(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
		out[w] = true
	}
	return out
}

// Overlap is the Jaccard overlap of two sets of words: the words both have, over the words either has.
func Overlap(a, b map[string]bool) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	both := 0
	for w := range a {
		if b[w] {
			both++
		}
	}
	return float64(both) / float64(len(a)+len(b)-both)
}

// IndexLine is an entry's line in the memory index (9.3).
func (e *Entry) IndexLine() string {
	return fmt.Sprintf("- %04d (%s) %s", e.Number, e.Kind, e.Title)
}

// Remember saves an entry, active at once (9.3). An agent's is refused while the index is over its
// cap, when it is near another active entry, or when it looks like a secret; the operator's only for
// the last two, since the operator decides what the project keeps.
func (b *Board) Remember(ctx context.Context, p *Project, n NewEntry, src MemorySource, author string) (*Entry, error) {
	var out *Entry
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = b.remember(ctx, tx, p, n, src, author)
		return err
	})
	return out, err
}

// TryRemember says whether Remember would save the entry, and saves nothing.
func (b *Board) TryRemember(ctx context.Context, p *Project, n NewEntry, author string) error {
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := b.remember(ctx, tx, p, n, MemorySource{}, author); err != nil {
			return err
		}
		return errTried
	})
	if errors.Is(err, errTried) {
		return nil
	}
	return err
}

// remember saves an entry in the transaction given. Every way an entry is made comes here, so each is
// checked the same: a rule of the operator's too (9.5).
func (b *Board) remember(ctx context.Context, tx pgx.Tx, p *Project, n NewEntry, src MemorySource, author string) (*Entry, error) {
	if err := n.check(); err != nil {
		return nil, err
	}
	p, err := projectByRef(ctx, tx, p.ID, true)
	if err != nil {
		return nil, err
	}
	active, err := entries(ctx, tx, p.ID, `m.status = 'active'`)
	if err != nil {
		return nil, err
	}
	words := Words(n.Title + " " + n.Body)
	for _, e := range active {
		if Overlap(words, Words(e.Title+" "+e.Body)) >= NearDuplicate {
			return nil, errf(Conflict, "memory %04d says nearly the same: %q; update it instead of adding another", e.Number, e.Title)
		}
	}
	// The operator decides what the project keeps: also what they sent on from the chat (11.9).
	if author != Operator && author != ChatAuthor {
		doc, err := b.Settings(ctx, tx, p)
		if err != nil {
			return nil, err
		}
		if limit, _ := settingNumber(doc, "knowledge.memory.index_max_chars"); limit > 0 && indexLength(active) > int(limit) {
			return nil, errf(Refused, "the memory index is over its cap of %d characters: the lead consolidates it in housekeeping before new entries are taken; say it in a note", int(limit))
		}
	}
	var num int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(number), 0) + 1 FROM memory WHERE project = $1`, p.ID).Scan(&num); err != nil {
		return nil, err
	}
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO memory (project, number, kind, title, body, applies_to, paths, keywords, source_run, source_ticket, source_feedback, author)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING id`,
		p.ID, num, n.Kind, n.Title, n.Body, n.AppliesTo, nonNil(n.Paths), nonNil(n.Keywords), nullIf(src.Run), nullID(src.Ticket), nullID(src.Feedback), author).Scan(&id)
	if err != nil {
		return nil, err
	}
	e, err := scanEntry(tx.QueryRow(ctx, `SELECT `+entryCols+` FROM memory m WHERE m.id = $1`, id))
	if err != nil {
		return nil, err
	}
	// A lesson is a signal: the same lesson in several projects belongs in a role's definition (10.2).
	if n.Kind == "lesson" {
		if err := RecordSignal(ctx, tx, Signal{Project: p.ID, Ticket: src.Ticket, Run: src.Run, Kind: "lesson_recorded",
			Detail: map[string]any{"entry": num, "appliesTo": n.AppliesTo, "keywords": nonNil(n.Keywords), "text": n.Title}}); err != nil {
			return nil, err
		}
	}
	return e, db.RecordProjectChange(ctx, tx, p.ID, author, "memory", fmt.Sprintf("%04d", num), nil, map[string]any{"kind": n.Kind, "title": n.Title})
}

func indexLength(es []*Entry) int {
	n := 0
	for _, e := range es {
		n += len(e.IndexLine()) + 1
	}
	return n
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func entries(ctx context.Context, q db.Querier, project, where string, args ...any) ([]*Entry, error) {
	return entriesBy(ctx, q, `SELECT `+entryCols+` FROM memory m WHERE m.project = $1 AND `+where+` ORDER BY m.number DESC`,
		append([]any{project}, args...)...)
}

func entriesBy(ctx context.Context, q db.Querier, sql string, args ...any) ([]*Entry, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Memory lists a project's entries, newest first: all of them, or the active ones.
func (b *Board) Memory(ctx context.Context, p *Project, all bool) ([]*Entry, error) {
	if all {
		return entries(ctx, b.Pool, p.ID, `true`)
	}
	return entries(ctx, b.Pool, p.ID, `m.status = 'active'`)
}

// MemoryEntry is one entry by its number.
func (b *Board) MemoryEntry(ctx context.Context, p *Project, number int) (*Entry, error) {
	e, err := scanEntry(b.Pool.QueryRow(ctx, `SELECT `+entryCols+` FROM memory m WHERE m.project = $1 AND m.number = $2`, p.ID, number))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errf(NotFound, "no memory entry %04d", number)
	}
	return e, err
}

// SearchMemory finds active entries by their words, best first (9.3).
func (b *Board) SearchMemory(ctx context.Context, p *Project, query string, limit int) ([]*Entry, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errf(Invalid, "query: some words")
	}
	return entriesBy(ctx, b.Pool, `SELECT `+entryCols+` FROM memory m, websearch_to_tsquery('simple', $2) q
		WHERE m.project = $1 AND m.status = 'active' AND m.search @@ q ORDER BY ts_rank(m.search, q) DESC, m.number DESC LIMIT $3`, p.ID, query, limit)
}

// MemoryChange is the lead's or the operator's change of an entry. A nil field keeps what it is.
type MemoryChange struct {
	Kind      *string   `json:"kind"`
	Title     *string   `json:"title"`
	Body      *string   `json:"body"`
	AppliesTo *[]string `json:"appliesTo"`
	Paths     *[]string `json:"paths"`
	Keywords  *[]string `json:"keywords"`
	// Retire takes the entry out of the index and the search, with the reason.
	Retire *string `json:"retire"`
}

// ChangeMemory changes or retires an entry. An agent cannot change an entry the operator wrote (9.3).
func (b *Board) ChangeMemory(ctx context.Context, p *Project, number int, c MemoryChange, who string) (*Entry, error) {
	var out *Entry
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		e, err := scanEntry(tx.QueryRow(ctx, `SELECT `+entryCols+` FROM memory m WHERE m.project = $1 AND m.number = $2 FOR UPDATE OF m`, p.ID, number))
		if errors.Is(err, pgx.ErrNoRows) {
			return errf(NotFound, "no memory entry %04d", number)
		}
		if err != nil {
			return err
		}
		if e.Author == Operator && who != Operator {
			return errf(Refused, "memory %04d is the operator's: an agent does not change it", number)
		}
		before := map[string]any{"status": e.Status, "kind": e.Kind, "title": e.Title}
		if c.Retire != nil {
			reason := strings.TrimSpace(*c.Retire)
			if reason == "" {
				return errf(Invalid, "retire: the reason")
			}
			if e.Status == "retired" {
				return errf(Refused, "memory %04d is retired already", number)
			}
			if _, err := tx.Exec(ctx, `UPDATE memory SET status = 'retired', retired_reason = $2, changed_by = $3, updated_at = now() WHERE id = $1`,
				e.ID, reason, who); err != nil {
				return err
			}
		} else {
			n := NewEntry{Kind: e.Kind, Title: e.Title, Body: e.Body, AppliesTo: e.AppliesTo, Paths: e.Paths, Keywords: e.Keywords}
			set := func(dst *string, v *string) {
				if v != nil {
					*dst = *v
				}
			}
			set(&n.Kind, c.Kind)
			set(&n.Title, c.Title)
			set(&n.Body, c.Body)
			for _, s := range []struct{ dst, v *[]string }{{&n.AppliesTo, c.AppliesTo}, {&n.Paths, c.Paths}, {&n.Keywords, c.Keywords}} {
				if s.v != nil {
					*s.dst = *s.v
				}
			}
			if err := n.check(); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE memory SET kind = $2, title = $3, body = $4, applies_to = $5, paths = $6, keywords = $7, changed_by = $8, updated_at = now() WHERE id = $1`,
				e.ID, n.Kind, n.Title, n.Body, n.AppliesTo, nonNil(n.Paths), nonNil(n.Keywords), who); err != nil {
				return err
			}
		}
		if out, err = scanEntry(tx.QueryRow(ctx, `SELECT `+entryCols+` FROM memory m WHERE m.id = $1`, e.ID)); err != nil {
			return err
		}
		return db.RecordProjectChange(ctx, tx, p.ID, who, "memory", fmt.Sprintf("%04d", number), before,
			map[string]any{"status": out.Status, "kind": out.Kind, "title": out.Title})
	})
	return out, err
}

// MemoryFor is what a run is given of memory (9.3): the index of the entries for its role, cut at the
// cap, and the entries whose paths match the files its ticket names or whose keywords its text holds.
type MemoryFor struct {
	Index   []string
	Omitted int
	Matched []*Entry
}

// MemoryForRun reads what a run of a role is given, for its ticket's files and text.
func (b *Board) MemoryForRun(ctx context.Context, p *Project, role string, files []string, text string) (*MemoryFor, error) {
	active, err := entries(ctx, b.Pool, p.ID, `m.status = 'active'`)
	if err != nil {
		return nil, err
	}
	doc, err := b.Settings(ctx, b.Pool, p)
	if err != nil {
		return nil, err
	}
	limit, _ := settingNumber(doc, "knowledge.memory.index_max_chars")
	out := &MemoryFor{}
	size := 0
	lower := strings.ToLower(text)
	for _, e := range active {
		if !slices.Contains(e.AppliesTo, "all") && !slices.Contains(e.AppliesTo, role) {
			continue
		}
		line := e.IndexLine()
		if limit > 0 && size+len(line)+1 > int(limit) {
			out.Omitted++
		} else {
			out.Index = append(out.Index, line)
			size += len(line) + 1
		}
		if matchesAny(e.Paths, files) || containsAny(lower, e.Keywords) {
			out.Matched = append(out.Matched, e)
		}
	}
	// Dreamstore valid entries (9.10, R215): quarantined (< 0.40) rows are never surfaced to agents.
	dRows, dErr := b.Pool.Query(ctx, `SELECT number, kind, title, body, applies_to, paths, keywords, validity_score
		FROM dreamstore_entries
		WHERE project_id = $1 AND status IN ('active', 'drift') AND validity_score >= 0.40
		ORDER BY validity_score DESC, number DESC LIMIT 100`, p.ID)
	if dErr == nil {
		defer dRows.Close()
		for dRows.Next() {
			var num int
			var kind, title, body string
			var applies, paths, keywords []string
			var score float64
			if ser := dRows.Scan(&num, &kind, &title, &body, &applies, &paths, &keywords, &score); ser != nil {
				continue
			}
			if len(applies) > 0 && !slices.Contains(applies, "all") && !slices.Contains(applies, role) {
				continue
			}
			line := fmt.Sprintf("- D%04d (%s, validity %.2f) %s", num, kind, score, title)
			if limit > 0 && size+len(line)+1 > int(limit) {
				out.Omitted++
			} else {
				out.Index = append(out.Index, line)
				size += len(line) + 1
			}
			if matchesAny(paths, files) || containsAny(lower, keywords) {
				out.Matched = append(out.Matched, &Entry{
					Number:    num,
					Kind:      kind,
					Title:     title,
					Body:      body,
					AppliesTo: applies,
					Paths:     paths,
					Keywords:  keywords,
				})
			}
		}
	}
	if len(out.Index) == 0 && len(out.Matched) == 0 {
		return nil, nil
	}
	return out, nil
}

func containsAny(lower string, words []string) bool {
	for _, w := range words {
		if w = strings.ToLower(strings.TrimSpace(w)); w != "" && strings.Contains(lower, w) {
			return true
		}
	}
	return false
}

// matchesAny says one of the files is matched by one of the patterns. A pattern matches the path
// itself, a folder it names, and with ** any depth.
func matchesAny(patterns, files []string) bool {
	for _, pat := range patterns {
		for _, f := range files {
			if PathMatch(pat, f) {
				return true
			}
		}
	}
	return false
}

// PathMatch says a path is matched by a pattern: a glob, where ** is any number of folders, or a
// folder (with or without its slash) that holds the path; a path that is itself a folder above the
// pattern matches too, for a ticket that names a folder.
func PathMatch(pat, file string) bool {
	pat, file = strings.TrimPrefix(strings.TrimSpace(pat), "./"), strings.TrimPrefix(strings.TrimSpace(file), "./")
	if pat == "" || file == "" {
		return false
	}
	if strings.HasSuffix(pat, "/") || !strings.ContainsAny(pat, "*?[") {
		dir := strings.TrimSuffix(pat, "/")
		if file == dir || strings.HasPrefix(file, dir+"/") || strings.HasPrefix(dir, strings.TrimSuffix(file, "/")+"/") {
			return true
		}
	}
	return globMatch(strings.Split(pat, "/"), strings.Split(strings.TrimSuffix(file, "/"), "/"))
}

// globMatch matches the parts of a path against the parts of a pattern, where ** is any number of
// parts. It is a table over the two, so a pattern an agent wrote with many ** costs no more than the
// length of each times the other (security review of M12).
func globMatch(pat, parts []string) bool {
	// ok[j] says parts[j:] matches the rest of the pattern, from its end backwards.
	ok := make([]bool, len(parts)+1)
	ok[len(parts)] = true
	for i := len(pat) - 1; i >= 0; i-- {
		next := make([]bool, len(parts)+1)
		for j := len(parts); j >= 0; j-- {
			switch {
			case pat[i] == "**":
				next[j] = ok[j] || (j < len(parts) && next[j+1])
			case j < len(parts):
				m, _ := path.Match(pat[i], parts[j])
				next[j] = m && ok[j+1]
			}
		}
		ok = next
	}
	return ok[0]
}

// IndexOver says how long a project's memory index is in all, and its cap (9.3).
func (b *Board) IndexOver(ctx context.Context, p *Project) (size, limit int, err error) {
	active, err := entries(ctx, b.Pool, p.ID, `m.status = 'active'`)
	if err != nil {
		return 0, 0, err
	}
	doc, err := b.Settings(ctx, b.Pool, p)
	if err != nil {
		return 0, 0, err
	}
	l, _ := settingNumber(doc, "knowledge.memory.index_max_chars")
	return indexLength(active), int(l), nil
}
