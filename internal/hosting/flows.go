package hosting

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
)

// FollowEvery is how often Follow main and a project that waits for its Dockerfile are looked at
// (19.5, plan M19 D16).
var FollowEvery = 30 * time.Second

// Snapshot is a project's hosting settings as they apply now, for Changed to compare with after a
// change of the settings.
func (s *Service) Snapshot(ctx context.Context, projectID string) (Settings, error) {
	p, err := s.project(ctx, projectID)
	if err != nil {
		return Settings{}, err
	}
	return settingsOf(ctx, s.Pool, p.id, p.name, p.key)
}

// redeployed are the settings a change of which starts the live image again (plan M19 D21).
func redeployed(a, b Settings) bool {
	return a.Internet != b.Internet || a.Size != b.Size || a.Port != b.Port || a.Health != b.Health || a.Data != b.Data
}

// Changed is told after a project's settings were stored: a change of the internet, the size, the
// port, the health address or the data folder on a live project starts a settings deploy of the
// live image at once (plan M19 D21). A deploy that runs keeps the settings it began with.
func (s *Service) Changed(ctx context.Context, projectID string, before Settings) error {
	after, err := s.Snapshot(ctx, projectID)
	if err != nil || !redeployed(before, after) {
		return err
	}
	return s.redeployLive(ctx, projectID)
}

// redeployLive starts a settings deploy of what serves, when something serves.
func (s *Service) redeployLive(ctx context.Context, projectID string) error {
	cur, err := s.liveOf(ctx, projectID)
	if err != nil || !cur.ok {
		return err
	}
	_, err = s.Deploy(ctx, projectID, Ask{Why: "settings", By: "settings", From: cur.deploy.Number})
	return err
}

// SetValuesAndApply stores a project's values and, on a live project, starts the live image again
// with them (plan M19 D21): the running container keeps the values it started with until then.
func (s *Service) SetValuesAndApply(ctx context.Context, projectID string, values map[string]string, who string) error {
	if err := s.SetValues(ctx, projectID, values, who); err != nil {
		return err
	}
	return s.redeployLive(ctx, projectID)
}

// Hostable answers a press of Host locally on a project whose main has no Dockerfile (19.2 step 2,
// plan M19 D14): the ticket that makes it hostable, made once; hosting goes on by itself when main
// has the Dockerfile. A second press makes none.
func (s *Service) Hostable(ctx context.Context, projectID string) (string, error) {
	p, err := s.project(ctx, projectID)
	if err != nil {
		return "", err
	}
	var ticket *int64
	var state string
	err = s.Pool.QueryRow(ctx, `SELECT state, ticket FROM hosting WHERE project = $1`, p.id).Scan(&state, &ticket)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if state == "waiting_dockerfile" && ticket != nil {
		return s.ticketKey(ctx, *ticket)
	}
	st, err := settingsOf(ctx, s.Pool, p.id, p.name, p.key)
	if err != nil {
		return "", err
	}
	if s.Tickets == nil {
		return "", errors.New("no board to make the ticket on")
	}
	t, err := s.Tickets(ctx, p.id, hostableTicket(p.name, st))
	if err != nil {
		return "", err
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO hosting (project, state, ticket) VALUES ($1, 'waiting_dockerfile', $2)
		ON CONFLICT (project) DO UPDATE SET state = 'waiting_dockerfile', ticket = $2, updated_at = now()`, p.id, t.ID); err != nil {
		return "", err
	}
	s.logLine(p.id, "waiting for %s on main: ticket %s", st.Dockerfile, t.Key)
	return t.Key, nil
}

func (s *Service) ticketKey(ctx context.Context, id int64) (string, error) {
	var key string
	err := s.Pool.QueryRow(ctx, `SELECT p.key || '-' || t.number FROM tickets t JOIN projects p ON p.id = t.project WHERE t.id = $1`, id).Scan(&key)
	return key, err
}

// hostableTicket is the ticket that makes a project hostable (plan M19 D14): a Dockerfile where
// hosting.dockerfile says, whose image answers the health address on the port as a user that is not
// root. The Dockerfile's name is bounded to plain characters (D35), so it is one word of the checks.
func hostableTicket(name string, st Settings) board.NewTicket {
	df := st.Dockerfile
	return board.NewTicket{
		Title: "Make " + name + " hostable",
		Kind:  "chore",
		Why:   "The operator pressed Host locally, and main has no " + df + ": Crew builds a hosted project from one Dockerfile (19.2).",
		What:  fmt.Sprintf("Add %s at the root of the repository's tree as hosting.dockerfile names it. Its image runs the project as a user that is not root, listens on the port of the environment value PORT (%d by default), answers GET %s with 200 once it serves, and keeps what it must keep under %s, which is mounted there. It declares no VOLUME but %s.", df, st.Port, st.Health, st.Data, st.Data),
		Goal:  fmt.Sprintf("main has %s, and the image it builds answers %s on port %d as a user that is not root.", df, st.Health, st.Port),
		Checks: []board.CheckSpec{
			{Kind: "cmd", Spec: "test -f '" + df + "'"},
			{Kind: "cmd", Spec: "docker build -f '" + df + "' ."},
			{Kind: "judge", Spec: fmt.Sprintf("The image built from %s runs as a user that is not root, listens on PORT (%d by default), answers GET %s with 200, and keeps its data under %s with no other VOLUME.", df, st.Port, st.Health, st.Data)},
		},
	}
}

// RunFollow looks after Follow main and the projects that wait for their Dockerfile every
// FollowEvery, until ctx ends.
func (s *Service) RunFollow(ctx context.Context) {
	t := time.NewTicker(FollowEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := s.FollowRound(ctx); err != nil && ctx.Err() == nil {
			s.Log.Warn("following main for the hosted projects", "error", err)
		}
	}
}

// FollowRound deploys, once each: a project that waits for its Dockerfile, once main has it (why
// hostable); and a live project with hosting.follow_main on, when a ticket reached Done since the live
// deploy began and main's head is neither the live one's commit nor one that failed (19.5, plan M19
// D16): one deploy of the head, listing the tickets Done since.
func (s *Service) FollowRound(ctx context.Context) error {
	rows, err := s.Pool.Query(ctx, `SELECT h.project, h.state, coalesce(h.failed_head, ''), coalesce(d.commit, ''), coalesce(d.started_at, 'epoch')
		FROM hosting h JOIN projects p ON p.id = h.project LEFT JOIN deploys d ON d.id = h.live_deploy
		WHERE p.state <> 'archived' AND (h.state = 'waiting_dockerfile' OR (h.state IN ('live', 'failing') AND h.live_deploy IS NOT NULL))`)
	if err != nil {
		return err
	}
	type row struct {
		project, state, failed, commit string
		since                          time.Time
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.project, &r.state, &r.failed, &r.commit, &r.since); err != nil {
			rows.Close()
			return err
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var errs []error
	for _, r := range all {
		if err := s.follow(ctx, r.project, r.state, r.failed, r.commit, r.since); err != nil {
			var pe *PreconditionError
			var nd *NoDockerfileError
			if !errors.As(err, &pe) && !errors.As(err, &nd) {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (s *Service) follow(ctx context.Context, projectID, state, failed, commit string, since time.Time) error {
	p, err := s.project(ctx, projectID)
	if err != nil {
		return err
	}
	st, err := settingsOf(ctx, s.Pool, p.id, p.name, p.key)
	if err != nil {
		return err
	}
	if state != "waiting_dockerfile" && !st.FollowMain {
		return nil
	}
	head := s.Git.Main(ctx, p.name)
	if head == "" || head == failed || head == commit {
		return nil
	}
	if state == "waiting_dockerfile" {
		body, err := s.Git.FileAt(ctx, p.name, head, st.Dockerfile)
		if err != nil || body == nil {
			return err
		}
		_, err = s.Deploy(ctx, p.id, Ask{Why: "hostable", By: "hostable", Commit: head})
		return err
	}
	keys, err := s.doneSince(ctx, p.id, since)
	if err != nil || len(keys) == 0 {
		return err
	}
	_, err = s.Deploy(ctx, p.id, Ask{Why: "follow_main", By: "follow_main", Commit: head, Tickets: keys})
	return err
}

// doneSince are the keys of a project's tickets that reached Done after a time, in their order.
func (s *Service) doneSince(ctx context.Context, projectID string, since time.Time) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `SELECT p.key || '-' || t.number FROM tickets t JOIN projects p ON p.id = t.project
		WHERE t.project = $1 AND EXISTS (SELECT 1 FROM stage_moves m WHERE m.ticket = t.id AND m.to_stage = 'done' AND m.at > $2)
		ORDER BY t.number`, projectID, since)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// Forget is a project's Delete (plan M19 Stop): hosting stops, and its data, its copies, its logs,
// its images and its values go. The rows go with the project's.
func (s *Service) Forget(ctx context.Context, projectID string) error {
	if err := s.Stop(ctx, projectID); err != nil {
		var pe *PreconditionError
		if !errors.As(err, &pe) {
			return err
		}
	}
	rows, err := s.Pool.Query(ctx, `SELECT number FROM deploys WHERE project = $1 AND image IS NOT NULL`, projectID)
	if err != nil {
		return err
	}
	numbers, err := pgx.CollectRows(rows, pgx.RowTo[int])
	if err != nil {
		return err
	}
	var errs []error
	for _, n := range numbers {
		errs = append(errs, s.Runtime.RemoveImage(ctx, ImageRef(projectID, n)))
	}
	if s.Vault != nil {
		errs = append(errs, s.Vault.Delete(ctx, valuesID(projectID), "delete"))
	}
	if s.Dir != "" {
		errs = append(errs, os.RemoveAll(s.projectDir(projectID)))
	}
	return errors.Join(errs...)
}
