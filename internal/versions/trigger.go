package versions

import (
	"context"
	"errors"
	"fmt"

	"github.com/kesokaj/crew/internal/release"
	"github.com/kesokaj/crew/internal/settings"
)

// What starts a build, and what it holds (16.2 step 3, since 0.51): Build a release on Versions, or
// updates.release_after_tickets tickets merged on Crew's project since the last release. A build
// takes the newest commit of main, stopped before the merge of a second improvement, so that one
// release holds one improvement (10.5), and gets the next patch number unless a minor or a major is
// asked for.

// ErrNothingToBuild is the answer when main has not moved since the last release.
var ErrNothingToBuild = errors.New("main has not moved since the last release: there is nothing to build")

// ErrNoCrewProject is the answer when improvements.crew_project names no project (16.2).
var ErrNoCrewProject = errors.New("Crew's project is not made: make it on the Crew level first")

// The numbers a build may take.
const (
	BumpPatch = "patch"
	BumpMinor = "minor"
	BumpMajor = "major"
)

// Next is what a build would make now.
type Next struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	// Since is the last release's commit, "" when no release holds one of Crew's project.
	Since string `json:"since,omitempty"`
	// Tickets is how many tickets of Crew's project merged into Commit since, which
	// release_after_tickets counts; the operator's own pushes are not among them.
	Tickets int `json:"tickets"`
	// StopsBefore is the merge of the second improvement, which waits for the next release.
	StopsBefore string `json:"stopsBefore,omitempty"`
}

// Plan is what a build of the project would make now, with bump the number it takes.
func (b *Builder) Plan(ctx context.Context, project, bump string) (*Next, error) {
	g := b.Git
	head := g.Main(ctx, project)
	if head == "" {
		return nil, fmt.Errorf("the project %s has no main", project)
	}
	folders, _, err := release.List(b.Releases)
	if err != nil {
		return nil, err
	}
	next := &Next{Commit: head}
	// The last release whose commit this repository holds, as what a release holds reads it (Changes).
	before, err := b.releaseBefore(ctx, project, "", head)
	if err != nil {
		return nil, err
	}
	if before != nil {
		next.Since = before.Manifest.Commit
	}
	line, err := g.FirstParents(ctx, project, next.Since, head)
	if err != nil {
		return nil, err
	}
	if len(line) == 0 {
		return nil, ErrNothingToBuild
	}
	merged, improvements, err := b.merges(ctx, project, line)
	if err != nil {
		return nil, err
	}
	seen := 0
	for i, c := range line {
		if improvements[c] {
			if seen++; seen == 2 {
				next.StopsBefore = c
				if i == 0 {
					return nil, ErrNothingToBuild
				}
				next.Commit = line[i-1]
				line = line[:i]
				break
			}
		}
	}
	for _, c := range line {
		if merged[c] {
			next.Tickets++
		}
	}
	if next.Version, err = b.number(ctx, folders, bump); err != nil {
		return nil, err
	}
	return next, nil
}

// merges are the commits of line that are the merge of a ticket of the project, and of those the
// ones whose ticket is an improvement's (10.5): the merge commit of a ticket is the candidate whose
// check passed and which main then moved to (7.10). An improvement the operator took out is none: its
// revert follows it, and it is never measured (since 0.55).
func (b *Builder) merges(ctx context.Context, project string, line []string) (merged, improvements map[string]bool, err error) {
	rows, err := b.Pool.Query(ctx, `SELECT c.commit, EXISTS (SELECT 1 FROM improvements i WHERE i.ticket = c.ticket AND i.status <> 'removed')
		FROM check_runs c JOIN projects p ON p.id = c.project
		WHERE p.name = $1 AND c.purpose = 'merge' AND c.state = 'passed' AND c.commit = ANY($2)`, project, line)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	merged, improvements = map[string]bool{}, map[string]bool{}
	for rows.Next() {
		var c string
		var imp bool
		if err := rows.Scan(&c, &imp); err != nil {
			return nil, nil, err
		}
		merged[c] = true
		improvements[c] = improvements[c] || imp
	}
	return merged, improvements, rows.Err()
}

// number is the next version: the highest of the releases on the machine, of the builds that did not
// fail, and of the running version, with its patch, minor, or major number one more.
func (b *Builder) number(ctx context.Context, folders []*release.Folder, bump string) (string, error) {
	high := b.Running
	consider := func(v string) {
		if releaseNumber.MatchString(v) && (!releaseNumber.MatchString(high) || release.Less(high, v)) {
			high = v
		}
	}
	for _, f := range folders {
		consider(f.Manifest.Version)
	}
	rows, err := b.Pool.Query(ctx, `SELECT version FROM builds WHERE state <> 'failed'`)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var v string
		if rows.Scan(&v) == nil {
			consider(v)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}
	var x, y, z int
	if !releaseNumber.MatchString(high) {
		high = "0.0.0"
	}
	fmt.Sscanf(high, "%d.%d.%d", &x, &y, &z)
	switch bump {
	case "", BumpPatch:
		z++
	case BumpMinor:
		y, z = y+1, 0
	case BumpMajor:
		x, y, z = x+1, 0, 0
	default:
		return "", fmt.Errorf("%q is not patch, minor, or major", bump)
	}
	return fmt.Sprintf("%d.%d.%d", x, y, z), nil
}

// crewProject is the project improvements.crew_project names, "" when none.
func (s *Service) crewProject(ctx context.Context) (string, settings.Doc, error) {
	if s.Settings == nil {
		return "", nil, errors.New("the settings cannot be read")
	}
	d, err := s.Settings(ctx)
	if err != nil {
		return "", nil, err
	}
	imp, _ := d["improvements"].(settings.Doc)
	name, _ := imp["crew_project"].(string)
	return name, d, nil
}

// BuildNow is Build a release on Versions: the operator's own pushes count for it, so it builds
// whenever main moved.
func (s *Service) BuildNow(ctx context.Context, bump string) (*Build, *Next, error) {
	if s.Builder == nil {
		return nil, nil, errors.New("this server builds no release: it has no sandboxes")
	}
	project, _, err := s.crewProject(ctx)
	if err != nil {
		return nil, nil, err
	}
	if project == "" {
		return nil, nil, ErrNoCrewProject
	}
	next, err := s.Builder.Plan(ctx, project, bump)
	if err != nil {
		return nil, nil, err
	}
	bd, err := s.Builder.Start(ctx, project, next.Version, next.Commit, ByOperator)
	return bd, next, err
}

// AutoBuild starts a build when updates.release_after_tickets tickets of Crew's project merged since
// the last release (16.2 step 3). A commit is built by itself once: a build of it that failed waits
// for the operator's press.
func (s *Service) AutoBuild(ctx context.Context) error {
	if s.Builder == nil {
		return nil
	}
	project, d, err := s.crewProject(ctx)
	if err != nil || project == "" {
		return err
	}
	u, _ := d["updates"].(settings.Doc)
	after, _ := u["release_after_tickets"].(int)
	if after <= 0 {
		return nil
	}
	next, err := s.Builder.Plan(ctx, project, BumpPatch)
	if errors.Is(err, ErrNothingToBuild) {
		return nil
	}
	if err != nil {
		return err
	}
	if next.Tickets < after {
		return nil
	}
	var tried bool
	if err := s.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM builds WHERE commit = $1)`, next.Commit).Scan(&tried); err != nil || tried {
		return err
	}
	_, err = s.Builder.Start(ctx, project, next.Version, next.Commit, ByClock)
	if errors.Is(err, ErrBuildRuns) {
		return nil
	}
	if err == nil {
		s.Log.Info("a build of a release started by itself", "version", next.Version, "commit", next.Commit, "tickets", next.Tickets)
	}
	return err
}
