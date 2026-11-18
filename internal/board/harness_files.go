package board

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/baseline"
	harnesstree "github.com/kesokaj/crew/harness"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/gitstore"
	"github.com/kesokaj/crew/internal/harnessbuild"
)

var (
	ruleFile         = regexp.MustCompile(`^\.agents/rules/([A-Za-z0-9][A-Za-z0-9._-]*)\.md$`)
	harnessSkillPath = regexp.MustCompile(`^harness/skills/([A-Za-z0-9][A-Za-z0-9._-]*)/SKILL\.md$`)
	harnessRulePath  = regexp.MustCompile(`^harness/rules/([A-Za-z0-9][A-Za-z0-9._-]*)\.md$`)
	harnessAgentPath = regexp.MustCompile(`^harness/agents/([A-Za-z0-9][A-Za-z0-9._-]*)\.md$`)
	identName        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// GlobalHarnessEntry is one file of the global harness or baseline, with whether the operator
// customized it in crew_harness_files.
type GlobalHarnessEntry struct {
	Path        string     `json:"path"`
	Kind        string     `json:"kind"` // context, skill, rule, agent, or baseline
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Customized  bool       `json:"customized"`
	BuiltIn     bool       `json:"builtIn"`
	UpdatedAt   *time.Time `json:"updatedAt,omitempty"`
}

// GlobalFileDetail is the full content and embedded default of a global baseline/ or harness/ file.
type GlobalFileDetail struct {
	Path           string     `json:"path"`
	Kind           string     `json:"kind"`
	Content        string     `json:"content"`
	DefaultContent string     `json:"defaultContent"`
	Customized     bool       `json:"customized"`
	BuiltIn        bool       `json:"builtIn"`
	UpdatedAt      *time.Time `json:"updatedAt,omitempty"`
}

func classifyGlobalPath(pth string) (kind, name, rel string, ok bool) {
	switch {
	case strings.HasPrefix(pth, "baseline/"):
		rel = strings.TrimPrefix(pth, "baseline/")
		for _, bp := range baseline.Paths() {
			if bp == rel && strings.HasSuffix(rel, ".md") {
				return "baseline", rel, rel, true
			}
		}
		return "", "", "", false
	case pth == "harness/context/AGENTS.md":
		return "context", "AGENTS.md", "context/AGENTS.md", true
	case harnessSkillPath.MatchString(pth):
		name = harnessSkillPath.FindStringSubmatch(pth)[1]
		return "skill", name, strings.TrimPrefix(pth, "harness/"), true
	case harnessRulePath.MatchString(pth):
		name = harnessRulePath.FindStringSubmatch(pth)[1]
		if strings.EqualFold(name, "AGENTS") {
			return "", "", "", false
		}
		return "rule", name, strings.TrimPrefix(pth, "harness/"), true
	case harnessAgentPath.MatchString(pth):
		name = harnessAgentPath.FindStringSubmatch(pth)[1]
		return "agent", name, strings.TrimPrefix(pth, "harness/"), true
	default:
		return "", "", "", false
	}
}

// CustomHarnessMap returns all rows in crew_harness_files whose path starts with prefix.
func CustomHarnessMap(ctx context.Context, q db.Querier, prefix string) (map[string]string, map[string]time.Time, error) {
	out := map[string]string{}
	times := map[string]time.Time{}
	if q == nil {
		return out, times, nil
	}
	rows, err := q.Query(ctx, `SELECT path, content, updated_at FROM crew_harness_files WHERE path LIKE $1`, prefix+"%")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p, c string
		var at time.Time
		if err := rows.Scan(&p, &c, &at); err != nil {
			return nil, nil, err
		}
		out[p] = c
		times[p] = at
	}
	return out, times, rows.Err()
}

// ListGlobalHarnessFiles lists all global harness/ files (embedded + operator-added) and marks which
// ones have operator customizations in crew_harness_files.
func ListGlobalHarnessFiles(ctx context.Context, q db.Querier) ([]GlobalHarnessEntry, map[string]bool, error) {
	custom, times, err := CustomHarnessMap(ctx, q, "")
	if err != nil {
		return nil, nil, err
	}
	customSet := map[string]bool{}
	harnessCustom := map[string]string{}
	for p, c := range custom {
		customSet[p] = true
		if after, ok := strings.CutPrefix(p, "harness/"); ok {
			harnessCustom[after] = c
		}
	}
	items := harnesstree.BaseWithOverrides(harnessCustom)
	descByKindName := map[string]string{}
	for _, it := range items {
		descByKindName[it.Kind+":"+it.Name] = it.Description
	}
	builtIn := harnesstree.FilesMap()
	allPaths := map[string]bool{}
	for p := range builtIn {
		allPaths["harness/"+p] = true
	}
	for p := range harnessCustom {
		allPaths["harness/"+p] = true
	}
	paths := make([]string, 0, len(allPaths))
	for p := range allPaths {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]GlobalHarnessEntry, 0, len(paths))
	for _, p := range paths {
		kind, name, rel, ok := classifyGlobalPath(p)
		if !ok {
			continue
		}
		_, isBuiltIn := builtIn[rel]
		e := GlobalHarnessEntry{
			Path:        p,
			Kind:        kind,
			Name:        name,
			Description: descByKindName[kind+":"+name],
			Customized:  customSet[p],
			BuiltIn:     isBuiltIn,
		}
		if e.Kind == "context" {
			e.Description = "The global context every agent CLI reads at session start."
		}
		if t, ok := times[p]; ok {
			tCopy := t
			e.UpdatedAt = &tCopy
		}
		out = append(out, e)
	}
	return out, customSet, nil
}

// ReadGlobalHarnessFile reads one global baseline/... or harness/... file with its embedded default.
func ReadGlobalHarnessFile(ctx context.Context, q db.Querier, pth string) (*GlobalFileDetail, error) {
	kind, _, rel, ok := classifyGlobalPath(pth)
	if !ok {
		return nil, errf(Invalid, "path: %q is not an editable global baseline/ or harness/ file", pth)
	}
	var def string
	var builtIn bool
	if kind == "baseline" {
		t, err := baseline.Text(rel)
		if err != nil {
			return nil, errf(NotFound, "no baseline file %s", pth)
		}
		def, builtIn = t, true
	} else {
		if t, err := harnesstree.Text(rel); err == nil {
			def, builtIn = t, true
		}
	}
	cur := def
	var customized bool
	var updatedAt *time.Time
	if q != nil {
		var c string
		var at time.Time
		if err := q.QueryRow(ctx, `SELECT content, updated_at FROM crew_harness_files WHERE path = $1`, pth).Scan(&c, &at); err == nil {
			cur, customized, updatedAt = c, true, &at
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	if !builtIn && !customized {
		return nil, errf(NotFound, "no global file %s", pth)
	}
	return &GlobalFileDetail{
		Path:           pth,
		Kind:           kind,
		Content:        cur,
		DefaultContent: def,
		Customized:     customized,
		BuiltIn:        builtIn,
		UpdatedAt:      updatedAt,
	}, nil
}

// SaveGlobalHarnessFile validates and saves a global baseline/... or harness/... file in crew_harness_files.
func SaveGlobalHarnessFile(ctx context.Context, q db.Querier, pth, content, by string) (*GlobalFileDetail, error) {
	kind, _, rel, ok := classifyGlobalPath(pth)
	if !ok {
		return nil, errf(Invalid, "path: %q is not an editable global baseline/ or harness/ markdown path", pth)
	}
	if strings.TrimSpace(content) == "" || len(content) > 256<<10 {
		return nil, errf(Invalid, "content: must be non-empty and at most 256 kB")
	}
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if kind != "baseline" {
		custom, _, err := CustomHarnessMap(ctx, q, "harness/")
		if err != nil {
			return nil, err
		}
		custom[pth] = content
		if _, err := planCustomHarness(custom); err != nil {
			return nil, errf(Invalid, "%v", err)
		}
		_ = rel
	}
	if _, err := q.Exec(ctx, `INSERT INTO crew_harness_files (path, content, updated_by, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (path) DO UPDATE SET content = EXCLUDED.content, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		pth, content, by); err != nil {
		return nil, err
	}
	return ReadGlobalHarnessFile(ctx, q, pth)
}

// DeleteGlobalHarnessFile removes a customization from crew_harness_files, resetting to the embedded default.
func DeleteGlobalHarnessFile(ctx context.Context, q db.Querier, pth string) error {
	if _, _, _, ok := classifyGlobalPath(pth); !ok {
		return errf(Invalid, "path: %q is not a global baseline/ or harness/ file", pth)
	}
	_, err := q.Exec(ctx, `DELETE FROM crew_harness_files WHERE path = $1`, pth)
	return err
}

func planCustomHarness(custom map[string]string) (harnessbuild.Skeleton, error) {
	mfs := fstest.MapFS{}
	for p, c := range harnesstree.FilesMap() {
		mfs[p] = &fstest.MapFile{Data: []byte(c), Mode: 0o644}
	}
	for p, c := range custom {
		rel := strings.TrimPrefix(p, "harness/")
		mfs[rel] = &fstest.MapFile{Data: []byte(c), Mode: 0o644}
	}
	return harnessbuild.Plan(harnessbuild.Source{FS: mfs})
}

// CustomHarnessSkeleton compiles any operator-customized global harness/... files in crew_harness_files
// into their home-relative .gemini/config/... files for a sandbox. When crew_harness_files holds no
// harness/... rows, it returns nil, nil so sandboxes use the runtime image's /crew/home-skel as is.
func CustomHarnessSkeleton(ctx context.Context, q db.Querier) (map[string][]byte, error) {
	custom, _, err := CustomHarnessMap(ctx, q, "harness/")
	if err != nil || len(custom) == 0 {
		return nil, err
	}
	sk, err := planCustomHarness(custom)
	if err != nil {
		return nil, err
	}
	wantRel := map[string]bool{
		filepath.ToSlash(filepath.Join(harnessbuild.AgyRoot, "rules", "AGENTS.md")): true,
	}
	for p := range custom {
		_, name, _, ok := classifyGlobalPath(p)
		if !ok {
			continue
		}
		switch {
		case strings.HasPrefix(p, "harness/skills/"):
			wantRel[filepath.ToSlash(filepath.Join(harnessbuild.AgyRoot, "skills", name, "SKILL.md"))] = true
		case strings.HasPrefix(p, "harness/rules/"):
			wantRel[filepath.ToSlash(filepath.Join(harnessbuild.AgyRoot, "rules", name+".md"))] = true
		case strings.HasPrefix(p, "harness/agents/"):
			wantRel[filepath.ToSlash(filepath.Join(harnessbuild.AgyRoot, "agents", name, "agent.md"))] = true
		}
	}
	out := map[string][]byte{}
	for _, f := range sk.Files {
		slash := filepath.ToSlash(f.Rel)
		if wantRel[slash] {
			out[slash] = f.Data
		}
	}
	return out, nil
}

func validProjectLayerPath(pth string) (kind, name string, ok bool) {
	switch {
	case pth == "AGENTS.md":
		return "context", "AGENTS.md", true
	case skillFile.MatchString(pth):
		name = skillFile.FindStringSubmatch(pth)[1]
		if !identName.MatchString(name) {
			return "", "", false
		}
		return "skill", name, true
	case ruleFile.MatchString(pth):
		name = ruleFile.FindStringSubmatch(pth)[1]
		if strings.EqualFold(name, "AGENTS") {
			return "", "", false
		}
		return "rule", name, true
	default:
		return "", "", false
	}
}

// ReadSkillFile reads a project's AGENTS.md, .agents/skills/<name>/SKILL.md, or .agents/rules/<name>.md
// from main, or reads a base layer item when base is true.
func (b *Board) ReadSkillFile(ctx context.Context, p *Project, pth string, base bool) (string, error) {
	if base || strings.HasPrefix(pth, "baseline/") {
		clean := pth
		if !strings.HasPrefix(clean, "baseline/") && !strings.HasPrefix(clean, "harness/") {
			clean = "harness/" + clean
		}
		det, err := ReadGlobalHarnessFile(ctx, b.Pool, clean)
		if err != nil {
			return "", err
		}
		return det.Content, nil
	}
	if _, _, ok := validProjectLayerPath(pth); !ok {
		return "", errf(Invalid, "path: must be AGENTS.md, .agents/skills/<name>/SKILL.md, or .agents/rules/<name>.md")
	}
	main := b.Git.Main(ctx, p.Name)
	if main == "" {
		return "", errf(NotFound, "%s has no main branch yet", p.Name)
	}
	raw, err := b.Git.FileAt(ctx, p.Name, main, pth)
	if raw == nil || errors.Is(err, fs.ErrNotExist) || errors.Is(err, gitstore.ErrNoRev) {
		return "", errf(NotFound, "no file %s on main", pth)
	}
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// SaveSkillFile validates and writes AGENTS.md, .agents/skills/<name>/SKILL.md, or .agents/rules/<name>.md
// as a commit on the project's main branch, so running sandboxes pick it up on their next turn (12.9).
func (b *Board) SaveSkillFile(ctx context.Context, p *Project, pth, content string, who gitstore.Identity) (string, error) {
	if p.State == "archived" {
		return "", errf(Refused, "%s is archived", p.Name)
	}
	kind, name, ok := validProjectLayerPath(pth)
	if !ok {
		return "", errf(Invalid, "path: must be AGENTS.md, .agents/skills/<name>/SKILL.md, or .agents/rules/<name>.md")
	}
	if strings.TrimSpace(content) == "" || len(content) > 256<<10 {
		return "", errf(Invalid, "content: must be non-empty and at most 256 kB")
	}
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	switch kind {
	case "skill":
		for _, it := range harnesstree.Base() {
			if it.Kind == "skill" && it.Name == name {
				return "", errf(Invalid, "skill %q already exists in the global harness base; choose a project-specific name or edit the global skill on /crew", name)
			}
		}
		front, _, err := harnessbuild.Split(content)
		if err != nil {
			return "", errf(Invalid, "SKILL.md frontmatter: %v", err)
		}
		fm, err := harnessbuild.ParseStrict(front)
		if err != nil {
			return "", errf(Invalid, "SKILL.md frontmatter: %v", err)
		}
		if strings.TrimSpace(fm["description"]) == "" {
			return "", errf(Invalid, "SKILL.md frontmatter must include a non-empty description")
		}
	case "rule":
		if len(content) > harnessbuild.AgyRuleFileLimit {
			return "", errf(Invalid, "rule %s is %d bytes (limit %d bytes)", name, len(content), harnessbuild.AgyRuleFileLimit)
		}
		if strings.HasPrefix(strings.TrimSpace(content), "---") {
			front, _, err := harnessbuild.Split(content)
			if err != nil {
				return "", errf(Invalid, "rule frontmatter: %v", err)
			}
			if _, err := harnessbuild.ParseStrict(front); err != nil {
				return "", errf(Invalid, "rule frontmatter: %v", err)
			}
		}
	}
	commit, err := b.Git.CommitOverlay(ctx, p.Name, gitstore.Overlay{
		pth: func([]byte) []byte { return []byte(content) },
	}, fmt.Sprintf("Update %s\n\nUpdated from Crew's project settings.\n", pth), who)
	if err != nil {
		return "", err
	}
	layers.mu.Lock()
	delete(layers.byID, p.ID)
	layers.mu.Unlock()
	if p.Remote != nil {
		if _, err := b.Pool.Exec(ctx, `INSERT INTO pushes (project, remote, refs, who) VALUES ($1, $2, '{refs/heads/main}', $3)`,
			p.ID, *p.Remote, Operator); err != nil {
			return "", err
		}
	}
	if err := b.inTx(ctx, func(tx pgx.Tx) error {
		return db.RecordProjectChange(ctx, tx, p.ID, Operator, "project_layer", pth, nil, map[string]any{"main": commit})
	}); err != nil {
		return "", err
	}
	_ = b.IndexDocs(ctx, p)
	return commit, nil
}

// DeleteSkillFile removes a project skill or rule from main (AGENTS.md cannot be removed).
func (b *Board) DeleteSkillFile(ctx context.Context, p *Project, pth string, who gitstore.Identity) (string, error) {
	if p.State == "archived" {
		return "", errf(Refused, "%s is archived", p.Name)
	}
	kind, _, ok := validProjectLayerPath(pth)
	if !ok || kind == "context" {
		return "", errf(Invalid, "path: only .agents/skills/<name>/SKILL.md or .agents/rules/<name>.md can be removed")
	}
	commit, err := b.Git.DeletePage(ctx, p.Name, path.Clean(pth), fmt.Sprintf("Remove %s\n\nRemoved from Crew's project settings.\n", pth), who)
	if err != nil {
		return "", err
	}
	layers.mu.Lock()
	delete(layers.byID, p.ID)
	layers.mu.Unlock()
	if p.Remote != nil {
		if _, err := b.Pool.Exec(ctx, `INSERT INTO pushes (project, remote, refs, who) VALUES ($1, $2, '{refs/heads/main}', $3)`,
			p.ID, *p.Remote, Operator); err != nil {
			return "", err
		}
	}
	if err := b.inTx(ctx, func(tx pgx.Tx) error {
		return db.RecordProjectChange(ctx, tx, p.ID, Operator, "project_layer", pth, map[string]any{"deleted": true}, map[string]any{"main": commit})
	}); err != nil {
		return "", err
	}
	return commit, nil
}
