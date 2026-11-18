// Package improve is the way from the improver's proposal to the baseline (10.4, 10.5): what a patch
// may touch, the generic check, the evals in every project with cases, the decision, the ticket on
// Crew's own project, and the measurement after it is applied, with the roll back of a worse result.
// It is a protected part of Crew: no improvement changes it (16.8).
package improve

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/baseline"
	harnesstree "github.com/kesokaj/crew/harness"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/gitstore"
)

// Current is the text a file of the baseline or the harness has in the running version, by its path
// in Crew's repository, such as baseline/stages/in_progress.md.
func Current(path string) (string, error) {
	switch {
	case strings.HasPrefix(path, "baseline/"):
		return baseline.Text(strings.TrimPrefix(path, "baseline/"))
	case strings.HasPrefix(path, "harness/"):
		return harnesstree.Text(strings.TrimPrefix(path, "harness/"))
	}
	return "", fmt.Errorf("%s is no file of the baseline or the harness", path)
}

// Allowed says whether a patch may give path the text next (10.4, since 0.49): the markdown of the
// baseline's agents, stages, guides, and templates, and of the harness; not a file of a role that
// checks, nor the model or the effort of an agent. waits says the change waits for the operator: the
// prompt writer's rules, which no eval case measures (7.2).
func Allowed(path, next string) (waits bool, err error) {
	if strings.Contains(path, "..") || !strings.HasSuffix(path, ".md") {
		return false, fmt.Errorf("%s: a patch changes markdown of the baseline or the harness only", path)
	}
	cur, err := Current(path)
	if err != nil {
		return false, fmt.Errorf("%s: no such file of the baseline or the harness; a patch replaces a file that exists", path)
	}
	// The evals measure a change by the prompts they give the agents, which a change of the harness is
	// not; and the harness holds what the roles that check read too. So a patch of the harness waits
	// until the evals can carry one (security review of M15).
	if strings.HasPrefix(path, "harness/") {
		return false, fmt.Errorf("%s: a change of the harness cannot be measured by the evals yet: a patch changes the baseline's files", path)
	}
	if rest, ok := strings.CutPrefix(path, "baseline/"); ok {
		if !baseline.Overridable(rest) {
			return false, fmt.Errorf("%s: not a file a patch may change", path)
		}
		if baseline.Checking(rest) {
			return false, fmt.Errorf("%s: a file of a role that checks waits for the operator, and no improvement changes it", path)
		}
		// A change no eval can measure waits for the operator (security review of M15).
		waits = baseline.WaitsForOperator(rest)
	}
	if strings.TrimSpace(next) == "" || len(next) > 100000 {
		return false, fmt.Errorf("%s: the whole new file, at most 100 kB", path)
	}
	if strings.TrimSpace(next) == strings.TrimSpace(cur) {
		return false, fmt.Errorf("%s: the text is the file as it is", path)
	}
	for _, key := range []string{"model", "effort"} {
		if frontLine(cur, key) != frontLine(next, key) {
			return false, fmt.Errorf("%s: a patch does not change the %s of an agent: the backend or the operator chooses it (12.3)", path, key)
		}
	}
	return waits, nil
}

// frontLine is the value of a key in a file's front matter, or "".
func frontLine(text, key string) string {
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return ""
	}
	front, _, _ := strings.Cut(rest, "\n---")
	for _, l := range strings.Split(front, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), key+":"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// added are the lines a patch adds: what it says that the files do not say now. Only these can carry a
// project's content into the baseline.
func added(patch map[string]string) string {
	var b strings.Builder
	for path, next := range patch {
		cur, _ := Current(path)
		have := map[string]bool{}
		for _, l := range strings.Split(cur, "\n") {
			have[strings.TrimSpace(l)] = true
		}
		for _, l := range strings.Split(next, "\n") {
			if t := strings.TrimSpace(l); t != "" && !have[t] {
				b.WriteString(t)
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}

var word = regexp.MustCompile(`[\p{L}\p{N}]+`)

// shingleSize is how many words in a row a patch may share with a project's text (10.5, since 0.49).
const shingleSize = 10

func words(s string) []string {
	ws := word.FindAllString(strings.ToLower(s), -1)
	return ws
}

func shingles(s string) map[string]bool {
	ws := words(s)
	out := map[string]bool{}
	for i := 0; i+shingleSize <= len(ws); i++ {
		out[strings.Join(ws[i:i+shingleSize], " ")] = true
	}
	return out
}

func shares(set map[string]bool, text string) bool {
	if len(set) == 0 {
		return false
	}
	ws := words(text)
	for i := 0; i+shingleSize <= len(ws); i++ {
		if set[strings.Join(ws[i:i+shingleSize], " ")] {
			return true
		}
	}
	return false
}

// baselineText is the text of the baseline's own files, which every project gets.
var baselineText = sync.OnceValue(func() string {
	var b strings.Builder
	for _, p := range baseline.Paths() {
		if t, err := baseline.Text(p); err == nil {
			b.WriteString(t)
			b.WriteString("\n")
		}
	}
	return b.String()
})

// baselineNames says the baseline's own text names path whole: a path such as AGENTS.md is in every
// project, so a ticket that changed it says nothing of its project (design 0.50). A path below a
// folder the baseline names, such as .agents/skills/<name>/SKILL.md, is not named, and still counts.
func baselineNames(path string) bool {
	t := baselineText()
	for i := 0; ; {
		j := strings.Index(t[i:], path)
		if j < 0 {
			return false
		}
		if wholeAt(t, path, i+j) {
			return true
		}
		i += j + 1
	}
}

// onlyWhole says every time s holds path it is the whole path, never a part of a longer one such as
// services/payments/AGENTS.md or AGENTS.md.bak (review of 0.50).
func onlyWhole(s, path string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], path)
		if j < 0 {
			return true
		}
		if !wholeAt(s, path, i+j) {
			return false
		}
		i += j + 1
	}
}

// wholeAt says path at s[j:] is a whole path: no character of a path touches it, but for a full
// stop that ends a sentence.
func wholeAt(s, path string, j int) bool {
	inPath := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("_-/", r) }
	if r, _ := utf8.DecodeLastRuneInString(s[:j]); j > 0 && (inPath(r) || r == '.') {
		return false
	}
	rest := s[j+len(path):]
	r, n := utf8.DecodeRuneInString(rest)
	if r == '.' {
		r, _ = utf8.DecodeRuneInString(rest[n:])
		return rest[n:] == "" || !(inPath(r) || r == '.')
	}
	return rest == "" || !inPath(r)
}

// Generic is the generic check (10.5, step 2): it refuses a patch whose added lines hold any project's
// name, ticket number, repository name or address, or a path a cited ticket changed that the baseline
// does not name itself, or share a run of ten words with any project's summaries, records, memory,
// decisions, feedback, and bugs, or with the files the cited tickets changed. The reason names what was found, never the text it was found in.
func Generic(ctx context.Context, q db.Querier, git *gitstore.Store, patch map[string]string, cited []string) error {
	text := added(patch)
	lower := strings.ToLower(text)
	rows, err := q.Query(ctx, `SELECT p.id::text, p.name, p.key, coalesce((SELECT array_agg(g.name) FROM git_repos g WHERE g.project = p.id), '{}'),
			coalesce(p.remote, '') FROM projects p WHERE p.id <> 'f1c50000-0000-4000-8000-000000000001'`)
	if err != nil {
		return err
	}
	type proj struct {
		id, name, key, remote string
		repos                 []string
	}
	var projects []proj
	for rows.Next() {
		var p proj
		if err := rows.Scan(&p.id, &p.name, &p.key, &p.repos, &p.remote); err != nil {
			rows.Close()
			return err
		}
		projects = append(projects, p)
	}
	rows.Close()
	hasWord := func(w string) bool {
		if len(w) < 3 {
			return false
		}
		re := regexp.MustCompile(`(^|[^\p{L}\p{N}])` + regexp.QuoteMeta(strings.ToLower(w)) + `($|[^\p{L}\p{N}])`)
		return re.MatchString(lower)
	}
	for _, p := range projects {
		if hasWord(p.name) {
			return fmt.Errorf("it names a project")
		}
		if regexp.MustCompile(`\b` + regexp.QuoteMeta(p.key) + `-\d+\b`).MatchString(text) {
			return fmt.Errorf("it names a ticket of a project")
		}
		for _, r := range p.repos {
			if hasWord(r) {
				return fmt.Errorf("it names a project's repository")
			}
		}
		if p.remote != "" && strings.Contains(lower, strings.ToLower(strings.TrimSuffix(p.remote, ".git"))) {
			return fmt.Errorf("it names a project's repository address")
		}
	}
	set := shingles(text)
	// The cited tickets' changed paths and files.
	// A cited ticket whose files cannot be read refuses the patch: the check keeps out what it cannot
	// see (review of M15). A key that names no ticket, or a ticket that merged nothing, has no files.
	for _, key := range cited {
		var name, stage, base, merge string
		err := q.QueryRow(ctx, `SELECT pr.name, t.stage, coalesce((SELECT r.commit FROM check_runs r WHERE r.ticket = t.id AND r.purpose = 'merge' AND r.state = 'passed'
				ORDER BY r.id DESC LIMIT 1), '') FROM tickets t JOIN projects pr ON pr.id = t.project WHERE pr.key || '-' || t.number = $1`, key).Scan(&name, &stage, &merge)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		// A ticket not Done changed nothing on main; one Done whose merge Crew cannot find, such as one
		// merged outside Crew, cannot be read (round 2 of the review of M15).
		if merge == "" {
			if stage == "done" {
				return fmt.Errorf("the files of the cited ticket %s cannot be read", key)
			}
			continue
		}
		if git == nil {
			return fmt.Errorf("the files of the cited ticket %s cannot be read", key)
		}
		if base, err = git.FirstParent(ctx, name, merge); err != nil {
			return fmt.Errorf("the files of the cited ticket %s cannot be read", key)
		}
		files, err := git.ChangedFiles(ctx, name, base, merge)
		if err != nil {
			return fmt.Errorf("the files of the cited ticket %s cannot be read", key)
		}
		for i, f := range files {
			if (strings.Contains(f, "/") || strings.Contains(f, ".")) && strings.Contains(text, f) {
				if !onlyWhole(text, f) || !baselineNames(f) {
					return fmt.Errorf("it names a file a cited ticket changed")
				}
			}
			if i < 200 {
				if b, err := git.FileAt(ctx, name, merge, f); err == nil && len(b) < 200_000 && shares(set, string(b)) {
					return fmt.Errorf("it shares a run of %d words with a file a cited ticket changed", shingleSize)
				}
			}
		}
	}
	if len(set) == 0 {
		return nil
	}
	for _, src := range []struct{ what, sql string }{
		{"a summary", `SELECT coalesce(done, '') || ' ' || coalesce(how, '') || ' ' || coalesce(next, '') FROM summaries`},
		{"a record", `SELECT body FROM records`},
		{"a memory entry", `SELECT title || ' ' || body FROM memory`},
		{"a decision", `SELECT question || ' ' || choice || ' ' || reason FROM decisions`},
		{"feedback", `SELECT body FROM feedback`},
		{"a bug", `SELECT title || ' ' || body FROM bugs`},
	} {
		rows, err := q.Query(ctx, src.sql)
		if err != nil {
			return err
		}
		for rows.Next() {
			var t string
			if err := rows.Scan(&t); err != nil {
				rows.Close()
				return err
			}
			if shares(set, t) {
				rows.Close()
				return fmt.Errorf("it shares a run of %d words with %s of a project", shingleSize, src.what)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	return nil
}
