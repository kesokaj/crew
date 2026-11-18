package board

import (
	"context"
	"fmt"
	"strings"

	"github.com/kesokaj/crew/internal/settings"
)

// Crew's own projects (16.2, since 0.51): the project that holds Crew's source, made from
// updates.source_repo by one button, and the gate's fixture project crew-gate, which no list shows.
const (
	CrewKey     = "CREW"
	GateProject = "crew-gate"
	GateKey     = "GATE"
)

// CrewSource is where Crew's project comes from: the repository updates.source_repo names, read once
// from Clone with the header ReadHeader gives it, and kept with Remote, its SSH address, which Crew
// fetches main from and pushes merges to with the key of Setup (6.6).
type CrewSource struct {
	Repo, Clone, Remote string
}

// GitHubSource is the CrewSource of a repository on GitHub.
func GitHubSource(repo string) CrewSource {
	return CrewSource{Repo: repo, Clone: "https://github.com/" + repo + ".git", Remote: settings.CrewRemote(repo)}
}

const crewPlan = `## Goal

Crew's own source. Crew builds Crew (design 16.2): its agents work the tickets that are put on this
board, from improvements that passed their evals, the daily look for new versions, and the operator.
The operator's own coding agent still pushes to main; Crew takes that as work from outside.
`

// MakeCrewProject makes Crew's project (16.2): the repository src names, imported with its history as
// an ordinary project whose remote is its SSH address, running with no breakdown, so that only the
// tickets put on its board are worked. current is improvements.crew_project, which the caller sets
// to the project's name after: one is made, and a second is refused. The gate's fixture project is
// made first, once. A press that made the project but not the setting is finished by the next press,
// which takes the project only when it is this repository's (review of M16 P2).
func (b *Board) MakeCrewProject(ctx context.Context, src CrewSource, current, who string) (*Project, error) {
	if current != "" {
		return nil, errf(Refused, "Crew's project is %s already: there is one", current)
	}
	if !settings.SourceRepo.MatchString(src.Repo) {
		return nil, errf(Refused, "updates.source_repo is %q: set it to the repository Crew builds from, as owner/name, first", src.Repo)
	}
	name := strings.TrimSuffix(strings.ToLower(src.Repo[strings.Index(src.Repo, "/")+1:]), ".git")
	if !settings.ProjectName.MatchString(name) || settings.UUIDShaped(name) {
		return nil, errf(Refused, "the repository's name %q is no project's name: small letters, digits, and dashes", name)
	}
	if err := b.makeGate(ctx, who); err != nil {
		return nil, err
	}
	if p, err := projectByRef(ctx, b.Pool, name, false); err == nil {
		if p.Key == CrewKey && !p.Hidden && p.State != "archived" && p.Remote != nil && *p.Remote == src.Remote {
			return p, nil
		}
		return nil, errf(Conflict, "a project with the name %s exists, and is not %s's", name, src.Repo)
	}
	var taken bool
	if err := b.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projects WHERE key = $1)`, CrewKey).Scan(&taken); err != nil {
		return nil, err
	}
	if taken || b.Git.Exists(name) {
		return nil, errf(Conflict, "a project with the name %s or the key %s exists", name, CrewKey)
	}
	n := NewProject{Name: name, Key: CrewKey, Goal: "Crew's own source: Crew builds Crew.", Plan: crewPlan, Repo: Repo{Kind: "existing", URL: src.Clone}, own: true, protected: true}
	doc, err := settings.ProjectDefaults(n.Name, n.Key)
	if err != nil {
		return nil, err
	}
	CrewChecks(doc)
	header, done := "", func() {}
	if b.ReadHeader != nil {
		if header, done, err = b.ReadHeader(ctx, src.Clone); err != nil {
			return nil, errf(Refused, "%s cannot be read: %v", src.Repo, err)
		}
	}
	err = b.Git.CreateFrom(ctx, n.Name, src.Clone, header)
	done()
	if err != nil {
		return nil, b.gitError(err)
	}
	// Its main is the remote's as it was read, so nothing waits to be pushed, and it runs as it is.
	p, err := b.insertProject(ctx, n, doc, "existing "+src.Clone, src.Remote, 0, who)
	if err != nil {
		b.Git.Remove(n.Name)
		return nil, err
	}
	return p, nil
}

// CrewChecks gives Crew's project its checks (16.2, since 0.53; the operator's choice of report 8):
// make check, with no setup, in a check sandbox with a Docker daemon of its own and no socket of the
// machine, in the network mode Sources, which lets the package sources through and not the model's
// hosts. Schema step 61 gives the same to the project made before.
func CrewChecks(doc settings.Doc) {
	sb := doc["sandbox"].(settings.Doc)
	sb["setup"], sb["test"], sb["check_network"] = "", "make check", "sources"
}

const gatePlan = `## Goal

The fixture of Crew's gate (16.2): each release's real ticket adds a small Go function and its test
here, through In progress, Review, and Test. Nothing else is built here.
`

// makeGate makes the gate's fixture project crew-gate, once: a Go module on the machine, hidden,
// running with In progress, Review, and Test, 30 turns a run and no budget (since 0.53), no setup,
// go test as its test, Model only, and no credentials beyond what every run gets.
func (b *Board) makeGate(ctx context.Context, who string) error {
	var exists bool
	if err := b.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projects WHERE name = $1)`, GateProject).Scan(&exists); err != nil || exists {
		return err
	}
	if b.Identity == nil {
		return errf(Refused, "no git identity: set it up in Setup first")
	}
	id, err := b.Identity(ctx)
	if err != nil {
		return errf(Refused, "no git identity: set it up in Setup first (%v)", err)
	}
	n := NewProject{Name: GateProject, Key: GateKey, Goal: "The fixture of Crew's gate.", Plan: gatePlan, Repo: Repo{Kind: "local"}, own: true, hidden: true}
	doc, err := settings.ProjectDefaults(n.Name, n.Key)
	if err != nil {
		return err
	}
	limits := doc["limits"].(settings.Doc)
	limits["run_max_turns"] = 30
	// A Go module with no dependencies, which needs no setup, tests with go test, and reaches only the
	// model: its runs may start from a candidate's runtime image with the agent CLI's login (16.2 step
	// 5; security review of M16 P4c).
	sb := doc["sandbox"].(settings.Doc)
	sb["setup"], sb["test"], sb["network"] = "", "go test ./...", "model_only"
	files := map[string]string{
		"README.md":    "# crew-gate\n\nThe fixture of Crew's gate: each release's real ticket adds a small Go function and its test.\n",
		"AGENTS.md":    "# crew-gate\n\nA Go module with no dependencies. Run `go test ./...`. Each ticket adds one package of its own.\n",
		"CHANGELOG.md": "# Changelog\n\nOne entry per ticket that reaches Done.\n",
		"go.mod":       "module crewgate\n\ngo 1.22\n",
	}
	if err := b.Git.CreateLocal(ctx, n.Name, files, GateKey+": the gate's first commit", id); err != nil {
		return b.gitError(err)
	}
	if _, err := b.insertProject(ctx, n, doc, "local", "", 0, who); err != nil {
		b.Git.Remove(n.Name)
		return err
	}
	_, err = b.Pool.Exec(ctx, `UPDATE projects SET state = 'archived', updated_at = now() WHERE id = $1 AND state <> 'archived'`, fixtureProject)
	return err
}

// GateTicket is the gate's real ticket for its run n (16.2): a small Go function and its test, in a
// package of its own, so that each run's checks fail on main before its work.
func GateTicket(n int) NewTicket {
	pkg := fmt.Sprintf("g%d", n)
	none := "The fixture has no pages."
	return NewTicket{
		Title: fmt.Sprintf("Add Reverse in package %s, with its test", pkg),
		Kind:  "feature",
		Why:   "Crew's gate runs one real ticket through In progress, Review, and Test on each release before it is installed.",
		What: fmt.Sprintf("Add the package %[1]s in the folder %[1]s/ with a function Reverse(s string) string that returns s with its runes in the "+
			"opposite order, and a table test TestReverse in %[1]s/reverse_test.go with an empty string, a word, and a word with a rune of more "+
			"than one byte.", pkg),
		Goal: fmt.Sprintf("go test ./%s/ passes, and TestReverse runs in it.", pkg),
		Checks: []CheckSpec{
			{Kind: "cmd", Spec: fmt.Sprintf("go vet ./%[1]s/ && go test -count=1 -v -run '^TestReverse$' ./%[1]s/ | grep -q -- '--- PASS: TestReverse'", pkg)},
		},
		DocsReason: &none,
	}
}
