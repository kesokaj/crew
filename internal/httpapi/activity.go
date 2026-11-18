package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/models"
)

// The activity feed (design 20.1, Activity): what agents and Crew did, one line each, newest first,
// each read from a row: a move, a run that started or ended, a check run, a ticket an agent made, a
// decision, a push.
func (s *Server) activityRoutes(api *routes) {
	api.HandleFunc("GET /api/activity", s.activity)
	api.HandleFunc("GET /api/projects/{p}/activity", s.projectActivity)
}

// ActivityLine is one line of the feed.
type ActivityLine struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"` // move | agent | check | ticket | decision | git | news
	Project string    `json:"project"`
	Ticket  *string   `json:"ticket"`
	Text    string    `json:"text"`
}

// The most lines one answer has.
const activityLimit = 200

// stageWords are the stages as the screens name them.
var stageWords = map[string]string{
	"planning": "Planning", "backlog": "Backlog", "analysis": "Analysis", "in_progress": "In progress",
	"review": "Review", "test": "Test", "e2e": "E2E", "verification": "Verification", "done": "Done", "closed": "Closed",
}

func stageWord(s string) string {
	if w, ok := stageWords[s]; ok {
		return w
	}
	return s
}

func roleWord(r string) string {
	if r == "" {
		return r
	}
	return strings.ToUpper(r[:1]) + strings.ReplaceAll(r[1:], "_", " ")
}

var purposeWords = map[string]string{
	"base": "The base check", "exit": "The exit checks", "test": "The checks of Test", "merge": "The checks on top of main",
	"part": "The check of a subtask", "join": "The check of a join", "judge": "The judge checks", "e2e": "The end-to-end checks",
	"stage": "The stage checks", "stage_judge": "The stage judge checks", "outside": "The checks on main",
}

func purposeWord(p string) string {
	if w, ok := purposeWords[p]; ok {
		return w
	}
	return "The checks"
}

// choiceWords say why Crew chose. Better is the builder's passed share, or for an agent that checks
// fewer defects escaped (12.3); cheaper is only on a choice made before 0.53.
var choiceWords = map[string]string{
	"better": "it passed Crew's checks more often", "cheaper": "it did as well for less (before 0.53)", "worse": "the watched tickets did worse",
	"operator": "you went back", "newer": "a newer model in its bracket was offered",
}

// whyChosen is choiceWords for the role: an agent that checks is better when fewer defects escaped it.
func whyChosen(role, reason string) string {
	if reason == "better" && role != "builder" {
		return "fewer defects escaped it, and it asked for fixes about as often"
	}
	return choiceWords[reason]
}

var stateWords = map[string]string{"passed": "passed", "failed": "failed", "error": "could not run", "running": "started"}

// readActivity reads the newest lines, of one project when project is an id, else of every project.
func readActivity(ctx context.Context, pool *pgxpool.Pool, project string) ([]ActivityLine, error) {
	out, err := readLines(ctx, pool, project, activityLimit)
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(out, func(a, b ActivityLine) int { return b.At.Compare(a.At) })
	if len(out) > activityLimit {
		out = out[:activityLimit]
	}
	if out == nil {
		out = []ActivityLine{}
	}
	return out, nil
}

// newsLimit bounds each source of the daily mail's news (D27): far more than a day has, so no line of
// news is crowded out by a busy day of runs, as one cut of all the lines would.
const newsLimit = 2000

// ReadNews is Activity's news lines of every project and the Crew level after a time, oldest first:
// what the daily mail holds (18.1).
func ReadNews(ctx context.Context, pool *pgxpool.Pool, after time.Time) ([]ActivityLine, error) {
	lines, err := readLines(ctx, pool, "", newsLimit)
	if err != nil {
		return nil, err
	}
	out := slices.DeleteFunc(lines, func(l ActivityLine) bool { return l.Kind != "news" || !l.At.After(after) })
	slices.SortStableFunc(out, func(a, b ActivityLine) int { return a.At.Compare(b.At) })
	return out, nil
}

// readLines reads at most limit lines of each source.
func readLines(ctx context.Context, pool *pgxpool.Pool, project string, limit int) ([]ActivityLine, error) {
	type source struct {
		sql  string
		read func(pgx.Rows) ([]ActivityLine, error)
	}
	// perRow reads one line from each row.
	perRow := func(line func(pgx.CollectableRow) (ActivityLine, error)) func(pgx.Rows) ([]ActivityLine, error) {
		return func(rows pgx.Rows) ([]ActivityLine, error) { return pgx.CollectRows(rows, line) }
	}
	// Every query names the project as p and filters on $1, the project's id or empty.
	const where = `($1 = '' OR p.id::text = $1)`
	key := `p.key || '-' || t.number`
	sources := []source{
		{`SELECT m.at, p.name, ` + key + `, m.from_stage, m.to_stage, coalesce(m.reason, '') FROM stage_moves m
			JOIN tickets t ON t.id = m.ticket JOIN projects p ON p.id = t.project WHERE ` + where + ` ORDER BY m.id DESC LIMIT $2`,
			perRow(func(r pgx.CollectableRow) (ActivityLine, error) {
				var l ActivityLine
				var k, from, to, why string
				err := r.Scan(&l.At, &l.Project, &k, &from, &to, &why)
				l.Kind, l.Ticket, l.Text = "move", &k, fmt.Sprintf("%s moved from %s to %s", k, stageWord(from), stageWord(to))
				if why != "" {
					l.Text += " (" + why + ")"
				}
				return l, err
			})},
		// A run that started, and the same run again when it ended.
		{`SELECT r.started_at, r.ended_at, p.name, CASE WHEN t.id IS NULL THEN NULL ELSE ` + key + ` END, s.number, s.role, s.title,
				r.cli, coalesce(r.model, ''), coalesce(r.effort, ''), coalesce(r.stop_reason, '')
			FROM runs r JOIN subtasks s ON s.id = r.subtask JOIN projects p ON p.id = s.project LEFT JOIN tickets t ON t.id = s.ticket
			WHERE ` + where + ` ORDER BY r.started_at DESC LIMIT $2`,
			runLines},
		{`SELECT coalesce(c.ended_at, c.started_at), p.name, ` + key + `, c.purpose, c.state FROM check_runs c
			JOIN tickets t ON t.id = c.ticket JOIN projects p ON p.id = c.project WHERE ` + where + ` ORDER BY c.id DESC LIMIT $2`,
			perRow(func(r pgx.CollectableRow) (ActivityLine, error) {
				var l ActivityLine
				var k, purpose, state string
				err := r.Scan(&l.At, &l.Project, &k, &purpose, &state)
				l.Kind, l.Ticket = "check", &k
				l.Text = fmt.Sprintf("%s of %s %s", purposeWord(purpose), k, stateWords[state])
				return l, err
			})},
		{`SELECT t.created_at, p.name, ` + key + `, t.title, t.created_by, t.origin FROM tickets t JOIN projects p ON p.id = t.project
			WHERE ` + where + ` AND t.origin <> 'plan' ORDER BY t.id DESC LIMIT $2`,
			perRow(func(r pgx.CollectableRow) (ActivityLine, error) {
				var l ActivityLine
				var k, title, by, origin string
				err := r.Scan(&l.At, &l.Project, &k, &title, &by, &origin)
				who := "The " + by
				if origin == "operator" {
					who = "You"
				}
				l.Kind, l.Ticket, l.Text = "ticket", &k, fmt.Sprintf("%s created %s %s", who, k, title)
				return l, err
			})},
		{`SELECT d.created_at, p.name, CASE WHEN t.id IS NULL THEN NULL ELSE ` + key + ` END, d.role, d.question, d.choice
			FROM decisions d JOIN projects p ON p.id = d.project LEFT JOIN tickets t ON t.id = d.ticket
			WHERE ` + where + ` ORDER BY d.id DESC LIMIT $2`,
			perRow(func(r pgx.CollectableRow) (ActivityLine, error) {
				var l ActivityLine
				var role, q, choice string
				err := r.Scan(&l.At, &l.Project, &l.Ticket, &role, &q, &choice)
				l.Kind, l.Text = "decision", fmt.Sprintf("The %s decided: %s → %s", role, q, choice)
				return l, err
			})},
		{`SELECT x.done_at, p.name, x.refs, x.remote FROM pushes x JOIN projects p ON p.id = x.project
			WHERE ` + where + ` AND x.state = 'done' ORDER BY x.id DESC LIMIT $2`,
			perRow(func(r pgx.CollectableRow) (ActivityLine, error) {
				var l ActivityLine
				var refs []string
				var remote string
				err := r.Scan(&l.At, &l.Project, &refs, &remote)
				l.Kind, l.Text = "git", fmt.Sprintf("Crew pushed %s to %s", strings.Join(refs, ", "), remote)
				return l, err
			})},
		// A hosted project's deploy that went live (19.2 step 7, plan M19 News).
		{`SELECT d.live_at, p.name, d.number, d.commit, d.why FROM deploys d JOIN projects p ON p.id = d.project
			WHERE ` + where + ` AND d.live_at IS NOT NULL AND NOT p.hidden ORDER BY d.live_at DESC LIMIT $2`,
			perRow(func(r pgx.CollectableRow) (ActivityLine, error) {
				var l ActivityLine
				var n int
				var commit, why string
				err := r.Scan(&l.At, &l.Project, &n, &commit, &why)
				l.Kind, l.Text = "news", fmt.Sprintf("%s is live: deploy %d, commit %.7s, %s", l.Project, n, commit, deployWhy[why])
				return l, err
			})},
		// News of the new features (7.6): a notice given, and one that went ahead by itself when its
		// notice passed.
		{`SELECT c.at, p.name, c.row_key, coalesce(t.title, ''), coalesce(c.after->>'why', ''), coalesce(c.after->>'notice_until', '')
			FROM changes c JOIN projects p ON p.id = c.project LEFT JOIN tickets t ON t.project = p.id AND p.key || '-' || t.number = c.row_key
			WHERE ` + where + ` AND c.tbl = 'tickets' AND (c.after->>'why' = 'its notice passed'
				OR (c.after->'analysis'->>'status' = 'new_feature_notice' AND c.before->'analysis'->>'status' IS DISTINCT FROM 'new_feature_notice'))
			ORDER BY c.seq DESC LIMIT $2`,
			perRow(func(r pgx.CollectableRow) (ActivityLine, error) {
				var l ActivityLine
				var k, title, why, until string
				err := r.Scan(&l.At, &l.Project, &k, &title, &why, &until)
				l.Kind, l.Ticket = "news", &k
				switch {
				case why == "its notice passed":
					l.Text = fmt.Sprintf("New feature %s %s went ahead: nobody objected during its notice. It is ready, and is built when there is room", k, title)
				case until != "":
					l.Text = fmt.Sprintf("%s %s is a new feature. It goes ahead by itself after its notice unless you discard it or hold it", k, title)
				default:
					l.Text = fmt.Sprintf("%s %s is a new feature. It waits for your Approve now", k, title)
				}
				return l, err
			})},
		// News the backend writes as a signal with its text (7.7, 8.2, 21): a ticket past its budget
		// (only before 0.53, which stored ones keep), a ticket long in its stage, the lead's look again at a ticket that could not get through, and a
		// project's setup that fails on main, a pull request someone merged on GitHub, and an override
		// applied or taken back (9.6).
		{`SELECT g.at, p.name, CASE WHEN t.id IS NULL THEN NULL ELSE ` + key + ` END, g.detail->>'text'
			FROM signals g JOIN projects p ON p.id = g.project LEFT JOIN tickets t ON t.id = g.ticket
			WHERE ` + where + ` AND g.kind IN ('over_budget', 'slow_stage', 'new_approach', 'cancel_and_replace', 'setup_failed', 'merged_outside', 'override', 'improver_note') AND g.detail ? 'text' ORDER BY g.id DESC LIMIT $2`,
			perRow(func(r pgx.CollectableRow) (ActivityLine, error) {
				var l ActivityLine
				err := r.Scan(&l.At, &l.Project, &l.Ticket, &l.Text)
				l.Kind = "news"
				return l, err
			})},
		// News of the models (12.3, 12.7): a pin passed over in a project; on the Crew level, what
		// Crew chose, a comparison that ended, a model a CLI offers or no longer offers, a list that
		// could not be read, and a limit that was reached.
		{`SELECT g.at, p.name, CASE WHEN t.id IS NULL THEN NULL ELSE ` + key + ` END, g.detail->>'role', g.detail->'ignored'->>0, g.detail->>'ran'
			FROM signals g JOIN projects p ON p.id = g.project LEFT JOIN tickets t ON t.id = g.ticket
			WHERE ` + where + ` AND g.kind = 'model_gone' ORDER BY g.id DESC LIMIT $2`,
			perRow(func(r pgx.CollectableRow) (ActivityLine, error) {
				var l ActivityLine
				var role, gone, ran string
				err := r.Scan(&l.At, &l.Project, &l.Ticket, &role, &gone, &ran)
				l.Kind, l.Text = "news", fmt.Sprintf("%s is no longer offered: the %s ran on %s in its place", gone, role, ran)
				return l, err
			})},
		{`SELECT c.at, c.role, c.kind, c.reason, c.from_entry || ' · ' || c.from_effort, c.to_entry || ' · ' || c.to_effort, c.state
			FROM model_choices c WHERE $1 = '' ORDER BY c.id DESC LIMIT $2`,
			perRow(func(r pgx.CollectableRow) (ActivityLine, error) {
				var l ActivityLine
				var role, kind, reason, from, to, state string
				err := r.Scan(&l.At, &role, &kind, &reason, &from, &to, &state)
				l.Kind = "news"
				switch kind {
				case "change":
					l.Text = fmt.Sprintf("Crew changed the %s from %s to %s: %s", role, from, to, whyChosen(role, reason))
				case "back":
					l.Text = fmt.Sprintf("Crew went back on the %s from %s to %s: %s", role, from, to, whyChosen(role, reason))
				default:
					l.Text = fmt.Sprintf("Crew recommends %s for the %s you set, in place of %s: %s", to, role, from, whyChosen(role, reason))
				}
				return l, err
			})},
		{`SELECT c.ended_at, c.role, c.own_entry || ' · ' || c.own_effort, c.other_entry || ' · ' || c.other_effort FROM comparisons c
			WHERE $1 = '' AND c.state = 'ended' ORDER BY c.id DESC LIMIT $2`,
			perRow(func(r pgx.CollectableRow) (ActivityLine, error) {
				var l ActivityLine
				var role, own, other string
				err := r.Scan(&l.At, &role, &own, &other)
				l.Kind, l.Text = "news", fmt.Sprintf("The comparison of %s with %s for the %s ended", own, other, role)
				return l, err
			})},
		{`SELECT m.first_seen, m.cli, m.name, false FROM models m WHERE $1 = '' AND m.added_by = 'cli'
				AND m.first_seen > (SELECT min(x.first_seen) FROM models x WHERE x.cli = m.cli) + interval '1 minute'
			UNION ALL SELECT m.gone_at, m.cli, m.name, true FROM models m WHERE $1 = '' AND m.gone_at IS NOT NULL
			ORDER BY 1 DESC LIMIT $2`,
			perRow(func(r pgx.CollectableRow) (ActivityLine, error) {
				var l ActivityLine
				var cliName, name string
				var gone bool
				err := r.Scan(&l.At, &cliName, &name, &gone)
				l.Kind, l.Text = "news", fmt.Sprintf("%s offers a new model: %s", cliName, name)
				if gone {
					l.Text = fmt.Sprintf("%s no longer offers %s", cliName, name)
				}
				return l, err
			})},
		{`SELECT l.models_failed_at, l.cli, coalesce(l.models_error, '') FROM cli_logins l
			WHERE $1 = '' AND l.models_failures >= ` + fmt.Sprint(models.FailuresForNews) + ` LIMIT $2`,
			perRow(func(r pgx.CollectableRow) (ActivityLine, error) {
				var l ActivityLine
				var cliName, why string
				err := r.Scan(&l.At, &cliName, &why)
				l.Kind, l.Text = "news", fmt.Sprintf("Crew could not read %s's list of models: %s. It keeps the last list", cliName, why)
				return l, err
			})},
		{`SELECT q.reached_at, q.cli, q.grp, coalesce(q.reached_until, q.reached_at) FROM quota q
			WHERE $1 = '' AND q.reached_at IS NOT NULL ORDER BY q.reached_at DESC LIMIT $2`,
			perRow(func(r pgx.CollectableRow) (ActivityLine, error) {
				var l ActivityLine
				var cliName, group string
				var until time.Time
				err := r.Scan(&l.At, &cliName, &group, &until)
				name := group
				if a, ok := cli.Get(cliName); ok && !strings.HasPrefix(group, "model:") {
					name = a.GroupName(group)
				} else {
					name = strings.TrimPrefix(group, "model:") + " alone"
				}
				l.Kind, l.Text = "news", fmt.Sprintf("%s: %s reached a limit; it resets at %s", cliName, name, until.Local().Format("Jan 2 15:04"))
				return l, err
			})},
		// News of the releases (10.7, since 0.57): one that measured worse than the release it replaced,
		// read from its row, so it comes once; a release that stopped running before schema step 67 makes
		// none. Its text is Crew's own, the numbers of its measure.
		{`SELECT r.measured_at, r.version, coalesce(r.measure->>'before', ''), coalesce((r.measure->>'window')::int, 0), coalesce(r.measure->>'why', '')
			FROM releases r WHERE $1 = '' AND r.measure_news AND r.measure->>'verdict' = 'worse' ORDER BY r.measured_at DESC LIMIT $2`,
			perRow(func(r pgx.CollectableRow) (ActivityLine, error) {
				var l ActivityLine
				var v, before, why string
				var n int
				err := r.Scan(&l.At, &v, &before, &n, &why)
				l.Kind, l.Text = "news", fmt.Sprintf("Crew %s measured worse than %s over %d tickets each: %s. Crew takes out only an improvement by itself; this release's numbers are on Versions",
					v, before, n, why)
				return l, err
			})},
		// News on the Crew level that no other row says (16.10, since 0.67): a restart for security
		// updates, its wait of 7 days, and a certificate that runs out. Each line is Crew's own text,
		// written once by its key.
		{`SELECT n.at, n.text FROM machine_news n WHERE $1 = '' ORDER BY n.id DESC LIMIT $2`,
			perRow(func(r pgx.CollectableRow) (ActivityLine, error) {
				var l ActivityLine
				err := r.Scan(&l.At, &l.Text)
				l.Kind = "news"
				return l, err
			})},
	}
	var out []ActivityLine
	for _, src := range sources {
		rows, err := pool.Query(ctx, src.sql, project, limit)
		if err != nil {
			return nil, err
		}
		lines, err := src.read(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, lines...)
	}
	return out, nil
}

// runLines makes a line for each run that started, and one more for each that ended.
func runLines(rows pgx.Rows) ([]ActivityLine, error) {
	defer rows.Close()
	var out []ActivityLine
	for rows.Next() {
		var started time.Time
		var ended *time.Time
		var project, role, title, cli, model, effort, reason string
		var k *string
		var n int
		if err := rows.Scan(&started, &ended, &project, &k, &n, &role, &title, &cli, &model, &effort, &reason); err != nil {
			return nil, err
		}
		what := title
		if k != nil {
			what = fmt.Sprintf("%s.%d %s", *k, n, title)
		}
		on := cli
		if model != "" {
			on = model
			if effort != "" {
				on += " · " + effort
			}
		}
		out = append(out, ActivityLine{At: started, Kind: "agent", Project: project, Ticket: k,
			Text: fmt.Sprintf("%s started %s on %s", roleWord(role), what, on)})
		if ended != nil {
			end := ActivityLine{At: *ended, Kind: "agent", Project: project, Ticket: k}
			if k != nil {
				what = fmt.Sprintf("%s.%d", *k, n)
			}
			end.Text = fmt.Sprintf("%s finished %s", roleWord(role), what)
			if reason != "" && reason != "ok" {
				end.Text = fmt.Sprintf("%s's run on %s stopped: %s", roleWord(role), what, reason)
			}
			out = append(out, end)
		}
	}
	return out, rows.Err()
}

func (s *Server) activity(w http.ResponseWriter, r *http.Request) {
	pool := s.pool(w)
	if pool == nil {
		return
	}
	lines, err := readActivity(r.Context(), pool, "")
	if err != nil {
		s.boardError(w, "reading the activity", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines})
}

func (s *Server) projectActivity(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	lines, err := readActivity(r.Context(), b.Pool, p.ID)
	if err != nil {
		s.boardError(w, "reading the activity", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines})
}

// deployWhy says why a deploy was made, for its line of news.
var deployWhy = map[string]string{
	"asked": "as you asked", "follow_main": "by Follow main", "rollback": "rolled back",
	"hostable": "once main had its Dockerfile", "settings": "with its settings of now",
}
