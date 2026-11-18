package board

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"go.yaml.in/yaml/v3"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/gitstore"
)

// A project's documentation (9.9): markdown pages on main, beside the code, which Crew indexes in the
// database when main changes, so the app shows, searches, and links them, and agents search them. A
// page names the code it covers in its front matter; a change of main that changes covered code and
// not the page marks the page as possibly out of date (R108).

const (
	maxPages    = 1000
	maxPageSize = 512 << 10
)

// DocPage is a page as Crew indexed it.
type DocPage struct {
	Path          string     `json:"path"`
	Title         string     `json:"title"`
	Kind          string     `json:"kind"`
	Covers        []string   `json:"covers"`
	Blob          string     `json:"blob"`
	LastCommit    string     `json:"lastCommit"`
	LastChangedAt time.Time  `json:"lastChangedAt"`
	LastTicket    *string    `json:"lastTicket"`
	OutdatedSince *time.Time `json:"outdatedSince"`
	OutdatedBy    *string    `json:"outdatedBy"`
	Body          string     `json:"body,omitempty"`
	// Tickets are the tickets that changed the page, newest first.
	Tickets []string `json:"tickets,omitempty"`
}

// Current says the page is not marked as possibly out of date.
func (d *DocPage) Current() bool { return d.OutdatedSince == nil }

// docsPlaces are where a project's pages are: its docs root, its changelog, and its overview.
type docsPlaces struct{ root, changelog string }

func (b *Board) docsPlaces(ctx context.Context, q db.Querier, p *Project) docsPlaces {
	doc, err := b.Settings(ctx, q, p)
	d := docsPlaces{root: "docs", changelog: "CHANGELOG.md"}
	if err == nil {
		if v := settingString(doc, "docs.root"); v != "" {
			d.root = strings.Trim(v, "/")
		}
		if v := settingString(doc, "docs.changelog"); v != "" {
			d.changelog = v
		}
	}
	return d
}

// isPage says a file of the repository is a page: the overview, the changelog, or markdown under the
// docs root.
func (d docsPlaces) isPage(p string) bool {
	return p == "README.md" || p == d.changelog || (strings.HasPrefix(p, d.root+"/") && strings.HasSuffix(strings.ToLower(p), ".md"))
}

// kindOf is a page's kind by its place (9.9).
func (d docsPlaces) kindOf(p string) string {
	switch {
	case p == "README.md":
		return "overview"
	case p == d.changelog:
		return "changelog"
	case p == d.root+"/architecture.md":
		return "architecture"
	case p == d.root+"/setup.md":
		return "setup"
	case strings.HasPrefix(p, d.root+"/decisions/"):
		return "decision"
	case strings.HasPrefix(p, d.root+"/guides/"):
		return "guide"
	case strings.HasPrefix(p, d.root+"/reference/"):
		return "reference"
	}
	return "page"
}

// pageFront is what a page's front matter may say.
type pageFront struct {
	Title  string   `yaml:"title"`
	Covers []string `yaml:"covers"`
}

// parsePage reads a page's title and what it covers: its front matter, then its first heading, then
// its file name.
func parsePage(p string, body []byte) (string, []string) {
	s := string(body)
	var fm pageFront
	if strings.HasPrefix(s, "---\n") {
		if end := strings.Index(s[4:], "\n---"); end >= 0 {
			_ = yaml.Unmarshal([]byte(s[4:4+end]), &fm)
			s = s[4+end+4:]
		}
	}
	title := strings.TrimSpace(fm.Title)
	if title == "" {
		for _, l := range strings.Split(s, "\n") {
			if strings.HasPrefix(l, "# ") {
				title = strings.TrimSpace(l[2:])
				break
			}
		}
	}
	if title == "" {
		title = strings.TrimSuffix(path.Base(p), path.Ext(p))
	}
	var covers []string
	for _, c := range fm.Covers {
		if c = strings.TrimSpace(c); c != "" && len(c) <= 200 && len(covers) < 50 {
			covers = append(covers, c)
		}
	}
	return cut(title, maxTitle), covers
}

var ticketTrailer = regexp.MustCompile(`(?m)^(?:Crew-)?Ticket: ([A-Z][A-Z0-9]{0,9}-\d+)\s*$`)

// IndexDocs indexes a project's pages as main holds them, when main moved since the last index. A page
// whose covered code changed and that did not change itself is marked; a page that changed is current
// again, and its change is recorded with the ticket that made it.
func (b *Board) IndexDocs(ctx context.Context, p *Project) error {
	if b.Git == nil {
		return nil
	}
	main := b.Git.Main(ctx, p.Name)
	if main == "" {
		return nil
	}
	var indexed *string
	if err := b.Pool.QueryRow(ctx, `SELECT docs_indexed FROM projects WHERE id = $1`, p.ID).Scan(&indexed); err != nil {
		return err
	}
	if indexed != nil && *indexed == main {
		return nil
	}
	places := b.docsPlaces(ctx, b.Pool, p)
	files, err := b.Git.Files(ctx, p.Name, main, places.isPage, maxPages)
	if err != nil {
		return err
	}
	var changed []string
	if indexed != nil && b.Git.Follows(ctx, p.Name, *indexed, main) {
		if changed, err = b.Git.ChangedOnMain(ctx, p.Name, *indexed, main); err != nil {
			return err
		}
	}
	// The ticket of a change of main, from the trailer of the commit Crew made on main.
	ticketIn := func(message string) *string {
		if m := ticketTrailer.FindStringSubmatch(message); m != nil {
			return &m[1]
		}
		return nil
	}
	type page struct {
		gitstore.TreeFile
		title, kind string
		covers      []string
		body        string
		commit      string
		at          time.Time
		ticket      *string
	}
	var pages []page
	have := map[string]string{}      // path → blob as indexed
	covered := map[string][]string{} // path → what it covers, as indexed
	rows, err := b.Pool.Query(ctx, `SELECT path, blob, covers FROM doc_pages WHERE project = $1`, p.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var pth, blob string
		var covers []string
		if err := rows.Scan(&pth, &blob, &covers); err != nil {
			rows.Close()
			return err
		}
		have[pth], covered[pth] = blob, covers
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, f := range files {
		if f.Size > maxPageSize {
			continue
		}
		pg := page{TreeFile: f, kind: places.kindOf(f.Path)}
		// A page as it was indexed is not read again: what it covers is in its row.
		if have[f.Path] == f.Blob {
			pg.covers = covered[f.Path]
			pages = append(pages, pg)
			continue
		}
		body, err := b.Git.Blob(ctx, p.Name, f.Blob, maxPageSize)
		if err != nil {
			return err
		}
		pg.body = string(body)
		pg.title, pg.covers = parsePage(f.Path, body)
		var msg string
		if pg.commit, pg.at, msg, err = b.Git.LastChange(ctx, p.Name, main, f.Path); err != nil {
			return err
		}
		pg.ticket = ticketIn(msg)
		pages = append(pages, pg)
	}
	// Which change of main touched covered code: the commit that last changed each such file.
	causeOf := func(file string) string {
		commit, _, msg, err := b.Git.LastChange(ctx, p.Name, main, file)
		if err != nil {
			return ""
		}
		if t := ticketIn(msg); t != nil {
			return *t
		}
		return commit[:12]
	}
	return b.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := projectByRef(ctx, tx, p.ID, true); err != nil {
			return err
		}
		keep := []string{}
		for _, pg := range pages {
			keep = append(keep, pg.Path)
			if have[pg.Path] == pg.Blob {
				// The page is as it was: is its covered code what changed?
				if pageChanged(pg.Path, changed) {
					continue
				}
				for _, f := range changed {
					if strings.HasPrefix(f, ".crew/") {
						continue
					}
					if f != pg.Path && matchesAny(pg.covers, []string{f}) {
						var fresh bool
						cause := causeOf(f)
						if err := tx.QueryRow(ctx, `UPDATE doc_pages SET outdated_since = coalesce(outdated_since, now()),
							outdated_by = coalesce(outdated_by, $3), indexed_at = now() WHERE project = $1 AND path = $2
							RETURNING outdated_by = $3 AND outdated_since = now()`,
							p.ID, pg.Path, cause).Scan(&fresh); err != nil {
							return err
						}
						// A page that falls behind now is a signal: code changed and its page did not (10.2).
						if fresh {
							var ticket int64
							if err := tx.QueryRow(ctx, `SELECT id FROM tickets WHERE project = $1 AND $2 = $3 || '-' || number`, p.ID, cause, p.Key).Scan(&ticket); err != nil && !errors.Is(err, pgx.ErrNoRows) {
								return err
							}
							if err := RecordSignal(ctx, tx, Signal{Project: p.ID, Ticket: ticket, Kind: "docs_behind",
								Detail: map[string]any{"page": pg.Path, "by": cause, "text": pg.Path + " is behind " + cause}}); err != nil {
								return err
							}
						}
						break
					}
				}
				continue
			}
			var ticket *int64
			if pg.ticket != nil {
				var id int64
				if err := tx.QueryRow(ctx, `SELECT t.id FROM tickets t WHERE t.project = $1 AND $2 = $3 || '-' || t.number`,
					p.ID, *pg.ticket, p.Key).Scan(&id); err == nil {
					ticket = &id
				}
			}
			if _, err := tx.Exec(ctx, `INSERT INTO doc_pages (project, path, title, kind, body, covers, blob, last_commit, last_changed_at, last_ticket)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
				ON CONFLICT (project, path) DO UPDATE SET title = $3, kind = $4, body = $5, covers = $6, blob = $7, last_commit = $8,
					last_changed_at = $9, last_ticket = $10, outdated_since = NULL, outdated_by = NULL, indexed_at = now()`,
				p.ID, pg.Path, pg.title, pg.kind, pg.body, nonNil(pg.covers), pg.Blob, pg.commit, pg.at, ticket); err != nil {
				return err
			}
			author := "git"
			if pg.ticket != nil {
				author = *pg.ticket
			}
			if _, err := tx.Exec(ctx, `INSERT INTO doc_page_tickets (project, path, ticket, commit, author, at) VALUES ($1, $2, $3, $4, $5, $6)
				ON CONFLICT DO NOTHING`, p.ID, pg.Path, ticket, pg.commit, author, pg.at); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM doc_pages WHERE project = $1 AND NOT (path = ANY($2))`, p.ID, keep); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE projects SET docs_indexed = $2 WHERE id = $1`, p.ID, main)
		return err
	})
}

func pageChanged(p string, changed []string) bool {
	for _, f := range changed {
		if f == p {
			return true
		}
	}
	return false
}

const docCols = `d.path, d.title, d.kind, d.covers, d.blob, d.last_commit, d.last_changed_at,
	(SELECT p.key || '-' || t.number FROM tickets t JOIN projects p ON p.id = t.project WHERE t.id = d.last_ticket),
	d.outdated_since, d.outdated_by`

func scanDoc(r pgx.Row, body bool) (*DocPage, error) {
	var d DocPage
	dest := []any{&d.Path, &d.Title, &d.Kind, &d.Covers, &d.Blob, &d.LastCommit, &d.LastChangedAt, &d.LastTicket, &d.OutdatedSince, &d.OutdatedBy}
	if body {
		dest = append(dest, &d.Body)
	}
	return &d, r.Scan(dest...)
}

// Docs lists a project's pages, by their place, without their bodies.
func (b *Board) Docs(ctx context.Context, p *Project) ([]*DocPage, error) {
	rows, err := b.Pool.Query(ctx, `SELECT `+docCols+` FROM doc_pages d WHERE d.project = $1 ORDER BY d.path`, p.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*DocPage
	for rows.Next() {
		d, err := scanDoc(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Doc is one page with its body and the tickets that changed it.
func (b *Board) Doc(ctx context.Context, p *Project, pth string) (*DocPage, error) {
	d, err := scanDoc(b.Pool.QueryRow(ctx, `SELECT `+docCols+`, d.body FROM doc_pages d WHERE d.project = $1 AND d.path = $2`, p.ID, pth), true)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errf(NotFound, "no page %s on main", pth)
	}
	if err != nil {
		return nil, err
	}
	rows, err := b.Pool.Query(ctx, `SELECT p.key || '-' || t.number FROM doc_page_tickets x
		JOIN tickets t ON t.id = x.ticket JOIN projects p ON p.id = t.project WHERE x.project = $1 AND x.path = $2
		GROUP BY p.key, t.number ORDER BY max(x.at) DESC, t.number DESC`, p.ID, pth)
	if err != nil {
		return nil, err
	}
	if d.Tickets, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return nil, err
	}
	return d, nil
}

// DocHit is a page that a search found, with the words around what matched.
type DocHit struct {
	Path    string `json:"path"`
	Title   string `json:"title"`
	Kind    string `json:"kind"`
	Snippet string `json:"snippet"`
	Current bool   `json:"current"`
}

// SearchDocs finds pages by their words, best first (9.9).
func (b *Board) SearchDocs(ctx context.Context, p *Project, query string, limit int) ([]DocHit, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errf(Invalid, "query: some words")
	}
	rows, err := b.Pool.Query(ctx, `SELECT d.path, d.title, d.kind,
			ts_headline('simple', d.body, q, 'MaxWords=30, MinWords=10, StartSel=**, StopSel=**'), d.outdated_since IS NULL
		FROM doc_pages d, websearch_to_tsquery('simple', $2) q
		WHERE d.project = $1 AND d.search @@ q ORDER BY ts_rank(d.search, q) DESC, d.path LIMIT $3`, p.ID, query, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (DocHit, error) {
		var h DocHit
		return h, r.Scan(&h.Path, &h.Title, &h.Kind, &h.Snippet, &h.Current)
	})
}

// EditPage is the operator's edit of a page in the app (9.9): a commit on main of the page alone, with
// the operator as its author, made from the commit the page was read at. A page that changed on main
// since is refused, so the operator reads the newer page first (21).
func (b *Board) EditPage(ctx context.Context, p *Project, pth, base, content string, who gitstore.Identity) (string, error) {
	places := b.docsPlaces(ctx, b.Pool, p)
	if path.Clean(pth) != pth || strings.HasPrefix(pth, "/") || strings.Contains(pth, "..") || !places.isPage(pth) || pth == places.changelog {
		return "", errf(Invalid, "path: a page of the documentation, which is README.md or markdown under %s/; the changelog is Crew's", places.root)
	}
	if len(content) > maxPageSize {
		return "", errf(Invalid, "the page is larger than %d bytes", maxPageSize)
	}
	if !gitstore.ValidCommit(base) {
		return "", errf(Invalid, "base: the commit the page was read at")
	}
	if p.State == "archived" {
		return "", errf(Refused, "%s is archived", p.Name)
	}
	commit, err := b.Git.CommitPage(ctx, p.Name, base, pth, []byte(content), fmt.Sprintf("Edit %s\n\nThe operator's edit of a page, made in Crew.\n", pth), who)
	if errors.Is(err, gitstore.ErrPageChanged) {
		return "", errf(Conflict, "%s changed on main since you opened it: read the newer page, and make your edit again", pth)
	}
	if errors.Is(err, gitstore.ErrMainMoved) {
		return "", errf(Conflict, "main moved while the edit was made: try again")
	}
	if err != nil {
		return "", err
	}
	if p.Remote != nil {
		if _, err := b.Pool.Exec(ctx, `INSERT INTO pushes (project, remote, refs, who) VALUES ($1, $2, '{refs/heads/main}', $3)`,
			p.ID, *p.Remote, Operator); err != nil {
			return "", err
		}
	}
	if err := b.inTx(ctx, func(tx pgx.Tx) error {
		return db.RecordProjectChange(ctx, tx, p.ID, Operator, "doc_pages", pth, map[string]any{"main": base}, map[string]any{"main": commit})
	}); err != nil {
		return "", err
	}
	return commit, b.IndexDocs(ctx, p)
}
