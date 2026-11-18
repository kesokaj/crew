package httpapi

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/gitstore"
)

// maxPushes is how many pushes of a ticket's branch its view reads, and maxTaskCommits the most
// commits it keeps over its groups and Other, the newest first, as the pushes are the newest: a push
// read once the view is full is read for its commits' ids alone, so none of them is shown as one no
// push brought, and what is past the most is counted in Omitted (6.1, since 1.09; the review of 1.09's
// change and the operator's popup answers of session 76, "IDs once full + 512 MiB raw" and "Newest,
// count on the page", and after their second round "IDs after a failure + UTF-8 only"). Each commit is
// kept to the cuts of a history, so the view holds at most about 25 MiB of them, and git's output it
// reads is about 2 GB at most: the messages of up to 1,499 commits of its pushes, of the 500 it reads
// for Other, and of one push past the bound of a read.
const (
	maxPushes       = 500
	maxTaskCommits  = 1000
	maxTaskGitCache = 64
)

type taskGitKey struct {
	dir        string
	maxOut     int64
	repo       string
	ticket     int64
	branch     string
	mergedAs   string
	mainHead   string
	branchHead string
	pushSum    [32]byte
}

var taskGitCache = struct {
	mu      sync.Mutex
	entries map[taskGitKey]TaskGit
}{entries: map[taskGitKey]TaskGit{}}

func cloneCommits(cs []gitstore.Commit) []gitstore.Commit {
	out := slices.Clone(cs)
	for i := range out {
		out[i].Parents = slices.Clone(out[i].Parents)
	}
	return out
}

func cloneTaskGit(g TaskGit) TaskGit {
	if g.Branch != nil {
		br := *g.Branch
		g.Branch = &br
	}
	if g.MergedAs != nil {
		m := *g.MergedAs
		g.MergedAs = &m
	}
	groups := make([]PushGroup, len(g.Groups))
	for i, grp := range g.Groups {
		grp.Commits = cloneCommits(grp.Commits)
		groups[i] = grp
	}
	g.Groups = groups
	g.Other = cloneCommits(g.Other)
	g.Files = slices.Clone(g.Files)
	return g
}

func readTaskGit(ctx context.Context, b *board.Board, p *board.Project, t *board.Ticket) (TaskGit, error) {
	g := TaskGit{Branch: t.Branch, Groups: []PushGroup{}, Other: []gitstore.Commit{}, Files: []gitstore.FileDiff{}}
	if t.Branch == nil {
		return g, nil
	}
	// The merge is the newest merge candidate whose checks passed, once the ticket is Done.
	if t.Stage == "done" {
		var c string
		err := b.Pool.QueryRow(ctx, `SELECT commit FROM check_runs WHERE ticket = $1 AND purpose = 'merge' AND state = 'passed'
			ORDER BY id DESC LIMIT 1`, t.ID).Scan(&c)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return g, err
		}
		if c != "" {
			g.MergedAs = &c
		}
	}
	// The newest pushes, at most maxPushes, oldest first: what is cut is the oldest.
	rows, err := b.Pool.Query(ctx, `SELECT who, old, new, at, number, title, role, mine FROM (
			SELECT x.id, x.who, coalesce(x.old, '') AS old, x.new, x.at, coalesce(s.number, 0) AS number, coalesce(s.title, '') AS title,
				coalesce(s.role, '') AS role, s.ticket = $3 AS mine
			FROM ref_pushes x LEFT JOIN runs r ON r.id = x.run LEFT JOIN subtasks s ON s.id = r.subtask
			WHERE x.repo = $1 AND x.ref = $2 ORDER BY x.id DESC LIMIT $4) p ORDER BY id`, p.Name, "refs/heads/"+*t.Branch, t.ID, maxPushes)
	if err != nil {
		return g, err
	}
	type push struct {
		who, old, new string
		at            time.Time
		n             int
		title, role   string
		mine          *bool
	}
	pushes, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (push, error) {
		var x push
		return x, r.Scan(&x.who, &x.old, &x.new, &x.at, &x.n, &x.title, &x.role, &x.mine)
	})
	if err != nil {
		return g, err
	}
	h := sha256.New()
	for _, x := range pushes {
		mine := 0
		if x.mine != nil && *x.mine {
			mine = 1
		}
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%d\x00%d\x00%s\x00%s\x00%d\n", x.who, x.old, x.new, x.at.UnixNano(), x.n, x.title, x.role, mine)
	}
	key := taskGitKey{dir: b.Git.Dir, maxOut: b.Git.MaxOutput, repo: p.Name, ticket: t.ID, branch: *t.Branch}
	copy(key.pushSum[:], h.Sum(nil))
	if g.MergedAs != nil {
		key.mergedAs = *g.MergedAs
	} else {
		key.mainHead = b.Git.Main(ctx, p.Name)
		key.branchHead = b.Git.Rev(ctx, p.Name, *t.Branch)
	}
	if key.mergedAs != "" || key.branchHead != "" {
		taskGitCache.mu.Lock()
		cached, ok := taskGitCache.entries[key]
		taskGitCache.mu.Unlock()
		if ok {
			return cloneTaskGit(cached), nil
		}
	}
	seen := map[string]bool{}
	kept := 0
	// keep keeps the newest of cs, which are oldest first, while the view has room.
	keep := func(cs []gitstore.Commit) []gitstore.Commit {
		n := min(len(cs), maxTaskCommits-kept)
		kept += n
		g.Omitted += len(cs) - n
		// A copy, so the commits left out are not held by the ones kept.
		return slices.Clone(cs[len(cs)-n:])
	}
	// The pushes are read newest first, and grouped oldest first. Once the view is full, or a push's
	// history is past the bound of a read, it and every older push are read for their ids alone, so one
	// view makes at most one read that fails.
	brought := make([][]gitstore.Commit, len(pushes))
	read := make([]bool, len(pushes))
	idsOnly := false
	for i := len(pushes) - 1; i >= 0; i-- {
		x := pushes[i]
		if kept < maxTaskCommits && !idsOnly {
			cs, err := b.Git.LogRange(ctx, p.Name, x.old, x.new)
			if err == nil {
				for _, c := range cs {
					seen[c.ID] = true
				}
				brought[i], read[i] = keep(cs), true
				continue
			}
			if !errors.Is(err, gitstore.ErrOutputTooLarge) {
				// A push the branch no longer holds, as one a later push replaced, is left out.
				continue
			}
			idsOnly = true
		}
		ids, err := b.Git.CommitIDs(ctx, p.Name, x.old, x.new)
		if err != nil {
			continue
		}
		for _, id := range ids {
			seen[id] = true
		}
		g.Omitted += len(ids)
	}
	for i, x := range pushes {
		if !read[i] {
			continue
		}
		cs := brought[i]
		sub := ""
		if x.mine != nil && *x.mine {
			sub = t.Key + "." + strconv.Itoa(x.n)
		}
		last := len(g.Groups) - 1
		if last >= 0 && sub != "" && g.Groups[last].Subtask == sub {
			g.Groups[last].To = x.new
			g.Groups[last].Commits = append(g.Groups[last].Commits, cs...)
		} else {
			g.Groups = append(g.Groups, PushGroup{Subtask: sub, Title: x.title, Role: x.role, Who: x.who, From: x.old, To: x.new, At: x.at, Commits: cs})
		}
	}
	// What the branch has that no recorded push brought: commits from before Crew recorded pushes.
	var all []gitstore.Commit
	switch {
	case g.MergedAs != nil:
		var m gitstore.Commit
		if m, _, err = b.Git.Show(ctx, p.Name, *g.MergedAs); err == nil && len(m.Parents) == 2 {
			all, err = b.Git.Log(ctx, p.Name, m.Parents[1], m.Parents[0], 500)
		}
	default:
		all, err = b.Git.Log(ctx, p.Name, *t.Branch, "main", 500)
		if errors.Is(err, gitstore.ErrNoRev) {
			return g, nil
		}
	}
	if err != nil {
		return g, err
	}
	g.Pushed = true
	// Other is newest first, as git lists it: the newest that have room are kept.
	var other []gitstore.Commit
	for _, c := range all {
		if !seen[c.ID] {
			other = append(other, c)
		}
	}
	slices.Reverse(other)
	other = keep(other)
	slices.Reverse(other)
	g.Other = append(g.Other, other...)
	if g.MergedAs != nil {
		_, g.Files, err = b.Git.Show(ctx, p.Name, *g.MergedAs)
	} else {
		g.Files, err = b.Git.Compare(ctx, p.Name, "main", *t.Branch)
	}
	if g.Files == nil {
		g.Files = []gitstore.FileDiff{}
	}
	if err == nil && (key.mergedAs != "" || key.branchHead != "") {
		taskGitCache.mu.Lock()
		if len(taskGitCache.entries) >= maxTaskGitCache {
			clear(taskGitCache.entries)
		}
		taskGitCache.entries[key] = cloneTaskGit(g)
		taskGitCache.mu.Unlock()
	}
	return g, err
}
