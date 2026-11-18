package capacity

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type usedQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Used is what the sandboxes Crew started take now, and the runs and checks recorded to start.
// When q supports BeginTx, the three queries run in one RepeatableRead read-only transaction so a
// run going idle while a check starts is never counted in both (BUG-038).
func Used(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}, sizes map[string]Size) (Use, error) {
	if b, ok := q.(interface {
		BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
	}); ok {
		tx, err := b.BeginTx(ctx, pgx.TxOptions{
			IsoLevel:   pgx.RepeatableRead,
			AccessMode: pgx.ReadOnly,
		})
		if err != nil {
			return Use{}, err
		}
		defer func() { _ = tx.Rollback(ctx) }() // read-only snapshot; nothing to commit
		q = tx
	}
	var u Use
	byProject, activeSandboxIDs, err := loadProjectVMs(ctx, q)
	if err != nil {
		return u, err
	}
	addProjectVMs(&u, byProject)
	if err := addActiveRuns(ctx, q, &u, sizes, byProject, activeSandboxIDs); err != nil {
		return u, err
	}
	if err := addRunningChecksAndSandboxes(ctx, q, &u, sizes); err != nil {
		return u, err
	}
	return u, nil
}

func addProjectVMs(u *Use, byProject map[string]projectVM) {
	for _, vm := range byProject {
		switch vm.state {
		case "running":
			u.CPUs += vm.cpus
			u.MemMB += vm.memMB
			u.ProjectVMs++
			u.Large++
		case "paused":
			u.MemMB += vm.memMB
			u.PausedVMs++
		}
	}
}

// A run idle between its turns is not counted: its microVM waits, such as for the checks of its
// work, and counting it would make those checks wait for its room (7.9, since 0.47). A
// conversation's run is counted idle too: its microVM waits for the operator's next message up to
// conversations.idle_minutes, with nothing of Crew's own waiting for its room (plan M20 D32).
// Runs executing inside a running or paused project_sandboxes microVM increment u.Runs without
// adding CPU/RAM a second time (13.10, R71).
func addActiveRuns(ctx context.Context, q usedQuerier, u *Use, sizes map[string]Size, byProject map[string]projectVM, activeSandboxIDs map[string]bool) error {
	runRows, err := q.Query(ctx, `SELECT coalesce(r.size, 'small'), coalesce(r.sandbox, ''), coalesce(s.project::text, ''), coalesce(s.kind, ''), coalesce(s.role, '')
		FROM runs r JOIN subtasks s ON s.id = r.subtask
		WHERE r.state IN ('starting', 'working') OR (r.state = 'idle' AND s.scope = 'conversation')`)
	if err != nil {
		return err
	}
	defer runRows.Close()
	for runRows.Next() {
		var size, sandboxID, projectID, kind, role string
		if err := runRows.Scan(&size, &sandboxID, &projectID, &kind, &role); err != nil {
			return err
		}
		u.Runs++
		vm, hasVM := byProject[projectID]
		vmActive := hasVM && (vm.state == "running" || vm.state == "paused")
		inProjectVM := (sandboxID != "" && activeSandboxIDs[sandboxID]) ||
			(sandboxID == "" && vmActive && kind != "judge" && role != "judge" && role != "improver")
		if !inProjectVM {
			if size == Large || size == Build {
				u.Large++
			} else {
				u.Small++
			}
			u.add(sizes[size], 1)
		}
	}
	return runRows.Err()
}

func addRunningChecksAndSandboxes(ctx context.Context, q usedQuerier, u *Use, sizes map[string]Size) error {
	rows, err := q.Query(ctx, `SELECT 'check', 'large', count(*) FROM check_runs WHERE state = 'running' AND purpose NOT IN ('judge', 'stage_judge')
		UNION ALL SELECT 'check', 'large', count(*) FROM remote_mains WHERE state = 'checking'
		UNION ALL SELECT 'check', 'large', count(*) FROM setup_checks WHERE state = 'checking'
		UNION ALL SELECT kind, coalesce(size, ''), count(*) FROM sandboxes WHERE removed_at IS NULL AND kind = ANY($1) GROUP BY kind, size`, kindNames())
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var what, size string
		var n int
		if err := rows.Scan(&what, &size, &n); err != nil {
			return err
		}
		switch what {
		case "check":
			u.Checks += n
		case "terminal":
			u.Terminals += n
			size = sandboxKinds[what]
		case "hosted":
			u.Hosted += n
			if size != Small && size != Large {
				size = sandboxKinds[what]
			}
		case "build", "host-build":
			u.Checks += n
			size = sandboxKinds[what]
		default:
			size = sandboxKinds[what]
		}
		// A build has Docker inside, as a large sandbox has: it is counted among them, at its own size.
		if size == Large || size == Build {
			u.Large += n
		} else {
			u.Small += n
		}
		u.add(sizes[size], n)
	}
	return rows.Err()
}

func kindNames() []string {
	var out []string
	for k := range sandboxKinds {
		out = append(out, k)
	}
	return out
}
