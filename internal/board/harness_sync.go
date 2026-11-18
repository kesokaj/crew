package board

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/harnessbuild"
	"github.com/kesokaj/crew/internal/homepath"
)

// SyncProjectHomeHarness live-syncs the global custom harness skeleton and the project's `main`
// skills (`.agents/skills/<name>/SKILL.md`), rules (`.agents/rules/<name>.md`), and `AGENTS.md`
// into a project's persistent `/home/agent` directory (`<CREW_DATA_DIR>/projects/<id>/home`) so
// the running `agy acp` server picks them up on the next session without rebuilding the image (12.9).
func (b *Board) SyncProjectHomeHarness(ctx context.Context, p *Project, homeDir string, deletedPath string) error {
	if homeDir == "" {
		return nil
	}
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		return err
	}
	root, err := os.OpenRoot(homeDir)
	if err != nil {
		return err
	}
	defer root.Close()

	// 1. Sync operator-customized global harness files into .gemini/config/...
	if skel, err := CustomHarnessSkeleton(ctx, b.Pool); err == nil {
		for rel, data := range skel {
			_ = writeSafeHomeFile(root, rel, data)
		}
	}

	// 2. If a project skill or rule was deleted, remove its synced copy from .gemini/config/ and .agents/.
	if deletedPath != "" {
		if kind, name, ok := validProjectLayerPath(deletedPath); ok {
			switch kind {
			case "skill":
				_ = root.RemoveAll(path.Join(harnessbuild.AgyRoot, "skills", name))
				_ = root.RemoveAll(path.Join(".agents", "skills", name))
			case "rule":
				_ = root.RemoveAll(path.Join(harnessbuild.AgyRoot, "rules", name+".md"))
				_ = root.RemoveAll(path.Join(".agents", "rules", name+".md"))
			}
		}
	}

	// 3. Sync project's main branch skills, rules, and AGENTS.md into the persistent home.
	if b.Git == nil || p == nil {
		return nil
	}
	main := b.Git.Main(ctx, p.Name)
	if main == "" {
		return nil
	}
	files, err := b.Git.Files(ctx, p.Name, main, func(f string) bool {
		return f == "AGENTS.md" || f == "WORKFLOW.md" || skillFile.MatchString(f) || (strings.HasPrefix(f, ".agents/rules/") && strings.HasSuffix(f, ".md"))
	}, cli.MaxInstructionEntries)
	if err != nil {
		return err
	}
	var totalBytes int64
	for _, f := range files {
		if strings.Count(f.Path, "/")+1 > cli.MaxInstructionDepth {
			return fmt.Errorf("instruction path %s exceeds maximum depth %d", f.Path, cli.MaxInstructionDepth)
		}
		body, berr := b.Git.Blob(ctx, p.Name, f.Blob, 256<<10)
		if berr != nil {
			continue
		}
		totalBytes += int64(len(body))
		if totalBytes > cli.MaxInstructionBytes {
			return fmt.Errorf("project instruction files exceed %d MiB", cli.MaxInstructionBytes>>20)
		}
		_ = writeSafeHomeFile(root, f.Path, body)
		if kind, name, ok := validProjectLayerPath(f.Path); ok {
			switch kind {
			case "skill":
				_ = writeSafeHomeFile(root, path.Join(harnessbuild.AgyRoot, "skills", name, "SKILL.md"), body)
			case "rule":
				_ = writeSafeHomeFile(root, path.Join(harnessbuild.AgyRoot, "rules", name+".md"), body)
			}
		}
	}
	return nil
}

// SyncAllProjectHomesGlobalHarness syncs operator-customized global harness files into every
// existing project's persistent home directory (`<CREW_DATA_DIR>/projects/<id>/home`).
func SyncAllProjectHomesGlobalHarness(ctx context.Context, q db.Querier, homeFor func(projectID string) string) error {
	if q == nil || homeFor == nil {
		return nil
	}
	skel, err := CustomHarnessSkeleton(ctx, q)
	if err != nil {
		return err
	}
	rows, err := q.Query(ctx, `SELECT id::text FROM projects WHERE state <> 'archived'`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		home := homeFor(id)
		if home == "" {
			continue
		}
		if fi, err := os.Stat(home); err != nil || !fi.IsDir() {
			continue
		}
		root, err := os.OpenRoot(home)
		if err != nil {
			continue
		}
		for rel, data := range skel {
			_ = writeSafeHomeFile(root, rel, data)
		}
		root.Close()
	}
	return nil
}

func writeSafeHomeFile(root *os.Root, rel string, data []byte) error {
	rel = filepath.ToSlash(path.Clean(rel))
	return homepath.WriteFileIn(root, rel, data, 0o644)
}
