package versions

import (
	"context"
	"path/filepath"

	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/release"
)

// Changes is what a release of a commit holds (16.2 step 6, since 0.54): the tickets merged into Crew's
// project's main since the release before it, and the commits from outside, the operator's own; or,
// in Note, why that cannot be read.
type Changes struct {
	Since   *ChangesSince  `json:"since"`
	Tickets []ChangeTicket `json:"tickets"`
	Outside []ChangeCommit `json:"outside"`
	Cut     ChangesCut     `json:"cut"`
	Note    string         `json:"note,omitempty"`
}

// ChangesSince is the release before: its version and its commit.
type ChangesSince struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// ChangeTicket is a ticket in a release, by its key, its title, and its merge commit.
type ChangeTicket struct {
	Key    string `json:"key"`
	Title  string `json:"title"`
	Commit string `json:"commit"`
}

// ChangeCommit is a commit from outside, with the first line of its message.
type ChangeCommit struct {
	Commit  string `json:"commit"`
	Subject string `json:"subject"`
}

// ChangesCut says which list was cut at maxChanges.
type ChangesCut struct {
	Tickets bool `json:"tickets"`
	Outside bool `json:"outside"`
}

// maxChanges is how many tickets, and how many commits from outside, Changes lists: the newest.
// maxMerges is how many merges no ticket made it reads the commits of, after which the list is cut.
var (
	maxChanges = 200
	maxMerges  = 50
)

// releaseBefore is the release a release of commit follows, as a build's plan takes it: the newest
// release folder other than version whose commit the repository holds and is an ancestor of commit.
// One built from the operator's checkout before Crew's project was made may not be held.
func (b *Builder) releaseBefore(ctx context.Context, project, version, commit string) (*release.Folder, error) {
	folders, _, err := release.List(b.Releases)
	if err != nil {
		return nil, err
	}
	for i := len(folders) - 1; i >= 0; i-- {
		f := folders[i]
		if f.Manifest.Version != version && b.Git.Follows(ctx, project, f.Manifest.Commit, commit) {
			return f, nil
		}
	}
	return nil, nil
}

// Changes reads what a release of commit holds, on project, Crew's. A commit of main's own line since
// the release before that a passed merge check names is that ticket's merge; any other commit of the
// line is from outside, itself when it has one parent, and when it is a merge no ticket made, the
// commits it brought in. A ticket's merge always has two parents (gitstore.MergeCandidate), and the
// commits of its branch are its own. Titles and subjects are screened as a release's title is.
func (b *Builder) Changes(ctx context.Context, project, version, commit string) (*Changes, error) {
	// What a release whose folder holds this commit holds does not change: it is read once, however
	// often the panel asks (reviews of 95d116d).
	key := project + "\x00" + version + "\x00" + commit
	b.heldMu.Lock()
	if ch, ok := b.held[key]; ok {
		b.heldMu.Unlock()
		return ch, nil
	}
	b.heldMu.Unlock()
	ch, err := b.changes(ctx, project, version, commit)
	if err != nil || ch.Note != "" {
		return ch, err
	}
	if f, ferr := release.Read(filepath.Join(b.Releases, version)); ferr == nil && f.Manifest.Commit == commit {
		b.heldMu.Lock()
		if b.held == nil || len(b.held) >= 100 {
			b.held = map[string]*Changes{}
		}
		b.held[key] = ch
		b.heldMu.Unlock()
	}
	return ch, nil
}

func (b *Builder) changes(ctx context.Context, project, version, commit string) (*Changes, error) {
	out := &Changes{Tickets: []ChangeTicket{}, Outside: []ChangeCommit{}}
	switch {
	case project == "":
		out.Note = "Crew's project is not made"
		return out, nil
	case !gitstore.ValidCommit(commit) || !b.Git.Follows(ctx, project, commit, commit):
		out.Note = "Crew's project does not hold the commit " + abbrev(commit, 12) + ": a build from a checkout"
		return out, nil
	}
	before, err := b.releaseBefore(ctx, project, version, commit)
	if err != nil {
		return nil, err
	}
	since := ""
	if before != nil {
		since = before.Manifest.Commit
		out.Since = &ChangesSince{Version: before.Manifest.Version, Commit: since}
	}
	// The line is read well past both bounds, so that a cut list is cut at its newest maxChanges; a line
	// longer than that, a release before none, cuts both.
	limit := 10 * maxChanges
	line, err := b.Git.FirstParentLog(ctx, project, since, commit, limit)
	if err != nil {
		return nil, err
	}
	if len(line) == limit {
		out.Cut = ChangesCut{Tickets: true, Outside: true}
	}
	ids := make([]string, len(line))
	for i, c := range line {
		ids[i] = c.ID
	}
	tickets, err := b.ticketsOf(ctx, project, ids)
	if err != nil {
		return nil, err
	}
	addOutside := func(c gitstore.Commit) {
		if len(out.Outside) == maxChanges {
			out.Cut.Outside = true
			return
		}
		out.Outside = append(out.Outside, ChangeCommit{Commit: c.ID, Subject: plain(c.Subject)})
	}
	merges := 0
	for _, c := range line {
		switch t, ok := tickets[c.ID]; {
		case ok:
			if len(out.Tickets) == maxChanges {
				out.Cut.Tickets = true
				continue
			}
			t.Commit = c.ID
			out.Tickets = append(out.Tickets, t)
		case len(c.Parents) < 2:
			addOutside(c)
		case out.Cut.Outside:
			// The list is full: what more merges brought in is not read.
		case merges == maxMerges:
			out.Cut.Outside = true
		default:
			merges++
			brought, err := b.Git.Log(ctx, project, c.Parents[1], c.Parents[0], maxChanges+1)
			if err != nil {
				return nil, err
			}
			if len(brought) > maxChanges {
				out.Cut.Outside = true
			}
			for _, m := range brought {
				if len(m.Parents) < 2 {
					addOutside(m)
				}
			}
		}
	}
	return out, nil
}

// ticketsOf are the tickets of project whose passed merge check names a commit of ids, by that commit:
// the newest check of a commit names its ticket. Its stage does not matter: a merge on main is its
// change in the release, also when its move to Done did not follow.
func (b *Builder) ticketsOf(ctx context.Context, project string, ids []string) (map[string]ChangeTicket, error) {
	rows, err := b.Pool.Query(ctx, `SELECT DISTINCT ON (c.commit) c.commit, p.key || '-' || t.number, t.title
		FROM check_runs c JOIN tickets t ON t.id = c.ticket JOIN projects p ON p.id = t.project
		WHERE p.name = $1 AND c.purpose = 'merge' AND c.state = 'passed' AND c.commit = ANY($2)
		ORDER BY c.commit, c.id DESC`, project, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ChangeTicket{}
	for rows.Next() {
		var c string
		var t ChangeTicket
		if err := rows.Scan(&c, &t.Key, &t.Title); err != nil {
			return nil, err
		}
		t.Title = plain(t.Title)
		out[c] = t
	}
	return out, rows.Err()
}

// Holds is what a release of commit holds, on Crew's project, as the detail of version shows it; nil on
// a server that builds nothing.
func (s *Service) Holds(ctx context.Context, version, commit string) (*Changes, error) {
	if s.Builder == nil {
		return nil, nil
	}
	project, _, err := s.crewProject(ctx)
	if err != nil {
		return nil, err
	}
	return s.Builder.Changes(ctx, project, version, commit)
}

// KindView is a release's kind against the running release, as the detail shows it: how many paths
// changed, not each.
type KindView struct {
	Kind      string   `json:"kind"`
	Changed   int      `json:"changed"`
	Protected []string `json:"protected"`
}

// KindOf is f's kind against the running release's folder (16.3); with none to compare, it is code.
func (s *Service) KindOf(f *release.Folder) *KindView {
	running, err := release.Read(filepath.Join(s.Releases, s.Running))
	if err != nil {
		running = nil
	}
	k := kindOf(f, running, 0)
	return &KindView{Kind: k.Kind, Changed: len(k.Changed), Protected: k.Protected}
}
