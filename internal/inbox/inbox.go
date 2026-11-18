// Package inbox is the one list of what needs the operator (design 7.8). Every item is read from
// the state that causes it, at the time it is asked for: a project that waits for its OK and a
// project paused at a limit come from the board, and a CLI that is signed out from the login check.
// An item goes away when its cause does, so the list cannot say something that is no longer true.
// An agent none of whose models a CLI offers, and a ticket whose check waits on a model of a
// family Crew does not know, are of what Crew could not do by itself (12.3). The
// other kinds of 7.8 come with the milestones that make their causes (overrides M15, the rest of
// what Crew could not do M11). A release that waits, and one the updater did not install, are here
// since M14 (16.3); an override that waits for the operator since M15 (9.6).
package inbox

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/hold"
	"github.com/kesokaj/crew/internal/release"
	"github.com/kesokaj/crew/internal/settings"
)

// The kinds of item there are so far.
const (
	KindProjectOK     = "project_ok"
	KindProjectPaused = "project_paused"
	// KindDiskFull is the disk guard's stop (16.10): no new run starts until there is room.
	KindDiskFull = "disk_full"
	// KindMachineBoot is two microVMs in a row that did not boot (21): no new run starts until one
	// boots or the operator resumes.
	KindMachineBoot = "machine_boot"
	// KindDBWritable is the database written again after a time it could not be (21), until read.
	KindDBWritable   = "db_writable"
	KindCLISignedOut = "cli_signed_out"
	// KindCrewCannot is what Crew could not do by itself (7.8): an agent with no model, a ticket
	// held after repeated failures, or a ticket whose check waits on a model of a family Crew does not know (12.3).
	KindCrewCannot = "crew_cannot"
	// KindTerminalMain is a main the project terminal pushed, which waits for the operator (13.11).
	KindTerminalMain = "terminal_main"
	// KindNewFeature is a ticket Analysis gave notice of as a new feature (7.6, 7.3): it goes ahead by
	// itself when its notice has passed, unless the operator discards it or holds it.
	KindNewFeature = "new_feature"
	// KindCreatedTask is a ticket the operator did not make, analysed, whose decision waits for the
	// operator: approvals.created_task is operator (7.3, since 0.47).
	KindCreatedTask = "created_task"
	// KindFeatureSpec is a Tier-2 feature spec whose decision alters 000-foundation.md, touches
	// 13.8 protected files, or runs under approvals.feature_specs: manual (7.14, R209).
	KindFeatureSpec = "feature_spec"
	// KindPullDiverged is a ticket whose branch went another way on GitHub than Crew's: Crew keeps
	// its own and does not take it (21, since 0.47).
	KindPullDiverged = "pull_diverged"
	// KindMayChange is a ticket an agent created whose analysis says it may change files that instruct
	// agents: it waits for the operator to agree (13.8).
	KindMayChange = "may_change"
	// KindCodeChange is a ticket whose branch changes files that are code for the agent CLI, or
	// GitHub workflows: it never merges by itself, and waits for the operator with the diff (13.8).
	KindCodeChange = "code_change"
	// KindRemoteMain is a main that came in on the remote from outside and changes files that are
	// code: Crew does not take it as the base by itself (7.12, 13.8).
	KindRemoteMain = "remote_main"
	// KindRelease is a release of Crew that passed the gate: one of text installs by itself when its
	// notice has passed unless it is held, every other waits for Install (16.3).
	KindRelease = "release"
	// KindReleaseFailed is a release the updater did not install: its check stopped it, or it was
	// switched back. Nothing was replaced (16.4, 21).
	KindReleaseFailed = "release_failed"
	// KindOverride is a project's override of the baseline that waits for the operator: a checking
	// role's file, the prompt writer's, a project that decides every change itself, or one whose evals
	// could not tell: too few cases (9.6, since 0.49), or runs of the change that measured nothing
	// (since 1.10).
	KindOverride = "override"
	// KindDreamProposal is a Scoped Dreaming candidate awaiting fresh-session approval (10.8, R216).
	KindDreamProposal = "dream_proposal"
)

// Item is one thing that needs the operator.
type Item struct {
	// ID is stable while the cause lasts, so a page and, later, a mail can name it.
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Project is the project's name, empty for an item of the Crew level.
	Project string    `json:"project,omitempty"`
	Title   string    `json:"title"`
	Body    string    `json:"body"`
	Since   time.Time `json:"since"`
	// Alternatives are what the operator can do about it, in the order shown.
	Alternatives []Alternative `json:"alternatives"`
}

// Alternative is one thing the operator can do: an action the page sends, or a page it opens.
type Alternative struct {
	Label string `json:"label"`
	// Action is a POST the page makes, with Body as its JSON, or empty for a link.
	Action string         `json:"action,omitempty"`
	Body   map[string]any `json:"body,omitempty"`
	Link   string         `json:"link,omitempty"`
	// Main marks the alternative the item is about.
	Main bool `json:"main,omitempty"`
}

// Operator is how the board names the operator in the history.
const Operator = "operator"

// List returns every item, the oldest first, which is the one that has waited longest. machine is
// the machine's settings as they apply.
func List(ctx context.Context, q db.Querier, machine settings.Doc) ([]Item, error) {
	var out []Item
	noModel := func(ctx context.Context, q db.Querier) ([]Item, error) { return agentsWithNoModel(ctx, q, machine) }
	noBracket := func(ctx context.Context, q db.Querier) ([]Item, error) { return unbracketedModels(ctx, q, machine) }
	for _, f := range []func(context.Context, db.Querier) ([]Item, error){projectsOK, projectsPaused, machineBoot, dbWritable, diskFull, clisSignedOut, noModel, noBracket, unknownFamilies, waitingWhileBracketHasRoom, checkRuleNotMet, failedTickets, heldMains, remoteMains, newFeatures, createdTasks, featureSpecs, pullsDiverged, mayChange, codeChanges, releases, overrides, dreamProposals, mailTrouble} {
		items, err := f(ctx, q)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	// A hidden project is Crew's own, the gate's crew-gate (16.2): what waits there is the gate's to
	// report, and no item of the inbox names it.
	rows, err := q.Query(ctx, `SELECT name FROM projects WHERE hidden`)
	if err != nil {
		return nil, err
	}
	hidden, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	out = slices.DeleteFunc(out, func(it Item) bool { return it.Project != "" && slices.Contains(hidden, it.Project) })
	sortBySince(out)
	if out == nil {
		out = []Item{}
	}
	return out, nil
}

func sortBySince(items []Item) {
	sort.SliceStable(items, func(i, j int) bool { return items[i].Since.Before(items[j].Since) })
}

// projectsOK are the projects whose breakdown is written and waits for the OK (7.2, step 5): in
// planning, with tickets in Planning, and the lead's last breakdown newer than the operator's last
// sending back.
func projectsOK(ctx context.Context, q db.Querier) ([]Item, error) {
	// An amendment's tickets wait the same way while the project runs on (7.13), once the lead broke
	// the amendment down after it was made.
	rows, err := q.Query(ctx, `SELECT p.name, b.created_at, t.n, a.number
		FROM projects p
		JOIN LATERAL (SELECT count(*) AS n FROM tickets WHERE project = p.id AND stage = 'planning') t ON t.n > 0
		JOIN LATERAL (SELECT kind, created_at FROM plans WHERE project = p.id AND kind IN ('breakdown', 'sent_back')
			ORDER BY id DESC LIMIT 1) b ON b.kind = 'breakdown'
		LEFT JOIN LATERAL (`+board.OpenAmendmentSQL("p.id")+`) a ON true
		WHERE (p.state = 'planning' OR (p.state IN ('running', 'done') AND a.id IS NOT NULL AND b.created_at > a.created_at))
		  AND NOT EXISTS (SELECT 1 FROM subtasks s JOIN runs r ON r.subtask = s.id
			WHERE s.project = p.id AND s.role IN ('planner', 'curator', 'lead') AND s.ticket IS NULL AND r.state IN ('starting', 'working'))`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Item, error) {
		var name string
		var since time.Time
		var n int
		var amendment *int
		if err := r.Scan(&name, &since, &n, &amendment); err != nil {
			return Item{}, err
		}
		body := plural(n, "ticket waits", "tickets wait") + " in Planning. Nothing is built until you say OK."
		title := "OK for " + name
		if amendment != nil {
			title = fmt.Sprintf("OK for amendment %d of %s", *amendment, name)
			body = plural(n, "ticket of the amendment waits", "tickets of the amendment wait") + " in Planning. The project runs on meanwhile."
		}
		return Item{
			ID: "ok/" + name, Kind: KindProjectOK, Project: name, Title: title, Body: body, Since: since,
			Alternatives: []Alternative{
				{Label: "Read the plan", Link: "/p/" + name + "/plan"},
				{Label: "See the tickets", Link: "/p/" + name + "/board"},
				{Label: "Give the OK", Action: "/api/projects/" + name + "/ok", Main: true},
			},
		}, nil
	})
}

// diskFull is the disk guard's stop (16.10), read from the row every start asks; it goes when the
// disk has room again.
func diskFull(ctx context.Context, q db.Querier) ([]Item, error) {
	st, err := hold.Read(ctx, q)
	if err != nil || st.DiskFull == "" {
		return nil, err
	}
	since := time.Now()
	if st.DiskAt != nil {
		since = *st.DiskAt
	}
	return []Item{{ID: "disk", Kind: KindDiskFull, Title: "The disk is too full: no new run starts", Since: since,
		Body:         st.DiskFull + ". Crew removed what it may; it never removes a trajectory. Free room on the machine, and work goes on by itself.",
		Alternatives: []Alternative{{Label: "See the database and the disk", Link: "/database", Main: true}}}}, nil
}

// machineBoot is the hold of two microVMs in a row that did not boot (21), read from the row every
// start asks; it goes when a microVM boots, the probe's or any other, or with Resume.
func machineBoot(ctx context.Context, q db.Querier) ([]Item, error) {
	st, err := hold.Read(ctx, q)
	if err != nil || st.BootFailed == "" {
		return nil, err
	}
	since := time.Now()
	if st.BootFailedAt != nil {
		since = *st.BootFailedAt
	}
	return []Item{{ID: "boot", Kind: KindMachineBoot, Title: "MicroVMs do not start: new runs are held", Since: since,
		Body: strings.TrimSuffix(st.BootFailed, ".") + ". Two microVMs in a row did not boot, so Kata or KVM on the machine needs you. " +
			"A probe microVM tries every 5 minutes, unless Crew is paused or stopped or the disk is full, and the hold goes by itself when one boots; Resume lets runs start now.",
		Alternatives: []Alternative{
			{Label: "Run the check", Action: "/api/machine/boot/probe"},
			{Label: "Resume", Action: "/api/machine/boot/resume", Main: true},
		}}}, nil
}

// dbWritable is the time the database could not be written, once it can be again (21), until the
// operator has read it.
func dbWritable(ctx context.Context, q db.Querier) ([]Item, error) {
	u, err := hold.ReadUnwritable(ctx, q)
	if err != nil || u == nil {
		return nil, err
	}
	// No clock time, so the text needs no time zone (16.10): how long, and how long ago.
	ago := minutes(time.Since(u.To))
	body := "Crew could not write its database until " + ago + " ago, from a time not known: Crew restarted while it could not write."
	if u.From != nil {
		body = fmt.Sprintf("Crew could not write its database for %s, until %s ago.", minutes(u.To.Sub(*u.From)), ago)
	}
	body += " No new run started meanwhile, and a run whose events could not be stored was stopped."
	return []Item{{ID: "db-writable", Kind: KindDBWritable, Title: "The database can be written again", Since: u.To, Body: body,
		Alternatives: []Alternative{{Label: "Read it", Action: "/api/machine/db-writable/read", Main: true}}}}, nil
}

func minutes(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
	return fmt.Sprintf("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
}

// projectsPaused are the projects that Crew paused at one of their limits (7.7). A project the
// operator paused needs nobody: it is not in the inbox.
func projectsPaused(ctx context.Context, q db.Querier) ([]Item, error) {
	rows, err := q.Query(ctx, `SELECT p.name, coalesce(p.pause_reason, ''), c.at FROM projects p
		JOIN LATERAL (SELECT who, at FROM changes WHERE tbl = 'projects' AND row_key = p.id::text
			AND after->>'state' = 'paused' ORDER BY seq DESC LIMIT 1) c ON c.who <> $1
		WHERE p.state = 'paused'`, Operator)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Item, error) {
		var it Item
		var reason string
		if err := r.Scan(&it.Project, &reason, &it.Since); err != nil {
			return it, err
		}
		it.ID, it.Kind = "paused/"+it.Project, KindProjectPaused
		it.Title = it.Project + " is paused at a limit"
		it.Body = reason + ". It starts nothing new; what runs finishes. Raise the limit, and it resumes by itself."
		it.Alternatives = []Alternative{
			{Label: "Open the board", Link: "/p/" + it.Project + "/board"},
			{Label: "Raise the limit", Link: "/p/" + it.Project + "/settings", Main: true},
		}
		return it, nil
	})
}

// clisSignedOut are the CLIs that are set up and that the login check found signed out (12.6). Runs
// on such a CLI wait, and continue after the sign-in.
func clisSignedOut(ctx context.Context, q db.Querier) ([]Item, error) {
	rows, err := q.Query(ctx, `SELECT cli, result, updated_at FROM cli_logins WHERE state = 'signed_out' ORDER BY cli`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Item, error) {
		var it Item
		var cli, result string
		if err := r.Scan(&cli, &result, &it.Since); err != nil {
			return it, err
		}
		it.ID, it.Kind = "signin/"+cli, KindCLISignedOut
		it.Title = cli + " is signed out"
		it.Body = "The login check says: " + result + ". Runs on " + cli + " wait, and continue after you sign in again."
		it.Alternatives = []Alternative{
			{Label: "Check now", Action: "/api/clis/" + cli + "/check"},
			{Label: "Sign in again", Link: "/setup", Main: true},
		}
		return it, nil
	})
}

// heldMains are the mains the project terminal pushed in edit mode, which wait for the operator's
// confirmation before they become the base: anything that ran in the terminal could have pushed one.
func heldMains(ctx context.Context, q db.Querier) ([]Item, error) {
	rows, err := q.Query(ctx, `SELECT m.id, p.name, m.old, m.new, m.files, m.code, m.pushed_at FROM terminal_mains m JOIN projects p ON p.id = m.project
		WHERE m.state = 'waiting' OR (m.state = 'deciding' AND m.decided_at < now() - interval '10 minutes') ORDER BY m.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Item, error) {
		var it Item
		var id int64
		var old, new string
		var files, code []string
		if err := r.Scan(&id, &it.Project, &old, &new, &files, &code, &it.Since); err != nil {
			return it, err
		}
		n := strconv.FormatInt(id, 10)
		it.ID, it.Kind = "main/"+it.Project+"/"+n, KindTerminalMain
		it.Title = "main from the terminal of " + it.Project + " waits for you"
		changed := strings.Join(files, ", ")
		if len(files) > 8 {
			changed = strings.Join(files[:8], ", ") + ", and " + strconv.Itoa(len(files)-8) + " more"
		}
		it.Body = "Pushed from the project terminal in edit mode: " + old[:min(7, len(old))] + " to " + new[:min(7, len(new))] +
			", changing " + plural(len(files), "file", "files") + " (" + changed + "). It becomes main when you take it. Crew's main stays as it was until then."
		if len(code) > 0 {
			// Whatever ran in the terminal could have pushed these (13.8): they are named first.
			it.Body = "It changes files that are code for the agent CLI, or GitHub workflows: " + strings.Join(code, ", ") +
				". Take it only if you made these changes. " + it.Body
		}
		base := "/api/projects/" + it.Project + "/terminal/mains/" + n
		it.Alternatives = []Alternative{
			{Label: "See the diff", Link: "/p/" + it.Project + "/git?from=" + old + "&to=" + new},
			{Label: "Refuse", Action: base + "/refuse"},
			{Label: "Take it as main", Action: base + "/take", Main: true},
		}
		return it, nil
	})
}

// remoteMains are mains that came in on a project's remote from outside and change files that are
// code: they wait for the operator before Crew takes them as the base (7.12, 13.8).
func remoteMains(ctx context.Context, q db.Querier) ([]Item, error) {
	rows, err := q.Query(ctx, `SELECT m.id, p.name, coalesce(p.remote, ''), m.base, m.remote, m.files, m.created_at FROM remote_mains m
		JOIN projects p ON p.id = m.project WHERE m.state = 'held' ORDER BY m.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Item, error) {
		var it Item
		var id int64
		var remote, base, head string
		var code []string
		if err := r.Scan(&id, &it.Project, &remote, &base, &head, &code, &it.Since); err != nil {
			return it, err
		}
		n := strconv.FormatInt(id, 10)
		it.ID, it.Kind = "remote/"+it.Project+"/"+n, KindRemoteMain
		it.Title = "main on the remote of " + it.Project + " changes files that are code"
		it.Body = "Commits came in on " + remote + " from outside Crew: " + base[:min(7, len(base))] + " to " + head[:min(7, len(head))] +
			". They change files that are code for the agent CLI, or GitHub workflows: " + strings.Join(code, ", ") +
			". Crew takes them as the base only when you say so, and then runs the project's tests on them. Left, Crew keeps its main, and its pushes of main wait until main on the remote follows it again."
		base2 := "/api/projects/" + it.Project + "/remote/mains/" + n
		it.Alternatives = []Alternative{
			{Label: "Leave it", Action: base2 + "/leave"},
			{Label: "Take it as main", Action: base2 + "/take", Main: true},
		}
		return it, nil
	})
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// releases are the release that waits and a release the updater did not install (16.3, 16.4).
func releases(ctx context.Context, q db.Querier) ([]Item, error) {
	rows, err := q.Query(ctx, `SELECT version, state, kind, protected, coalesce(notice_at, found_at), install_after, held,
			coalesce(held_why, ''), coalesce(failure, ''), changed
		FROM releases WHERE state IN ('waiting', 'failed') AND gate IS NOT NULL AND replaced_by IS NULL`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Item, error) {
		var v, state, kind, heldWhy, failure string
		var protected []string
		var since time.Time
		var after *time.Time
		var held bool
		var changed int
		if err := r.Scan(&v, &state, &kind, &protected, &since, &after, &held, &heldWhy, &failure, &changed); err != nil {
			return Item{}, err
		}
		// A hold a server before 0.53 stored is shown in today's words (since 0.54).
		heldWhy = release.Unpriced(heldWhy)
		install := Alternative{Label: "Install now", Action: "/api/crew/versions/" + v + "/install", Main: true}
		see := Alternative{Label: "See the release", Link: "/versions"}
		if state == "failed" {
			body := "Nothing was replaced: Crew runs on as it was. " + failure
			if held {
				body += " It is held, and installs only when you press Install."
			}
			return Item{ID: "release-failed/" + v, Kind: KindReleaseFailed, Title: "Crew " + v + " was not installed", Body: body, Since: since,
				Alternatives: []Alternative{see, {Label: "Try again", Action: install.Action, Main: true}}}, nil
		}
		it := Item{ID: "release/" + v, Kind: KindRelease, Title: "Crew " + v + " is ready", Since: since}
		switch {
		case kind == "setup":
			it.Body = "It needs a newer machine setup. Run crew-ctl install --upgrade-runtime on the machine, then Install."
			install.Label = "Install"
		case held:
			it.Body = "It is held: it installs when you press Install. " + heldWhy
			install.Label = "Install"
		case after != nil:
			it.Body = fmt.Sprintf("It changes only prompts and harness text (%s), and installs by itself at %s UTC unless you hold it.",
				plural(changed, "file", "files"), after.UTC().Format("2006-01-02 15:04"))
		default:
			it.Body = "It changes code, so it waits until you press Install: the server is one program with the rights of all its parts."
			install.Label = "Install"
		}
		if len(protected) > 0 {
			it.Body += " It touches protected parts: " + strings.Join(protected, ", ") + "."
		}
		it.Alternatives = []Alternative{see}
		if kind == "text" && !held {
			it.Alternatives = append(it.Alternatives, Alternative{Label: "Hold", Action: "/api/crew/versions/" + v + "/hold"})
		}
		it.Alternatives = append(it.Alternatives, install)
		return it, nil
	})
}

// overrides are the overrides that wait for the operator (9.6): in waiting, and in testing with evals
// that could not tell: too few cases, or runs of the change that measured nothing (since 1.10). Apply
// makes it the project's; Refuse ends it.
func overrides(ctx context.Context, q db.Querier) ([]Item, error) {
	rows, err := q.Query(ctx, `SELECT p.name, o.number, o.path, o.version, o.status, o.why, o.proposed_by, o.created_at, coalesce(o.eval->>'why', '')
		FROM overrides o JOIN projects p ON p.id = o.project
		WHERE p.state <> 'archived' AND (o.status = 'waiting' OR (o.status = 'testing' AND o.eval->>'verdict' = 'too_few'))
		ORDER BY o.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Item, error) {
		var it Item
		var number, version int
		var path, status, why, by, evalWhy string
		if err := r.Scan(&it.Project, &number, &path, &version, &status, &why, &by, &it.Since, &evalWhy); err != nil {
			return it, err
		}
		role, _, _ := strings.Cut(by, " · ")
		it.ID, it.Kind = fmt.Sprintf("override/%s/%d", it.Project, number), KindOverride
		it.Title = fmt.Sprintf("o_%d: %s's own %s waits for you", number, it.Project, path)
		if status == "testing" {
			// The evals' why is Crew's own sentence with its numbers: too few cases, or runs of the
			// change that measured nothing (since 1.10).
			it.Body = fmt.Sprintf("The %s proposed version %d of %s for this project. Its evals could not tell whether it is worse (%s), so it waits: "+
				"Apply makes it the project's, and Crew measures it afterwards.", role, version, path, evalWhy)
		} else {
			it.Body = fmt.Sprintf("The %s proposed version %d of %s for this project. It changes how work is checked, or the prompt writer's rules, "+
				"or a file no eval runs, or the project decides every change itself, so it waits for you.", role, version, path)
		}
		if strings.TrimSpace(why) != "" {
			it.Body += " Why, as the " + role + " wrote it: " + why
		}
		base := fmt.Sprintf("/api/projects/%s/overrides/%d", it.Project, number)
		// One that waits is read before it applies: Open is the main way, where its text is (security
		// review of M15).
		it.Alternatives = []Alternative{
			{Label: "Open", Link: "/p/" + it.Project + "/settings", Main: true},
			{Label: "Refuse", Action: base, Body: map[string]any{"choice": "refuse"}},
			{Label: "Apply", Action: base, Body: map[string]any{"choice": "apply"}},
		}
		return it, nil
	})
}

func dreamProposals(ctx context.Context, q db.Querier) ([]Item, error) {
	rows, err := q.Query(ctx, `SELECT p.name, d.key, d.kind, d.target_path, d.title, d.created_at
		FROM dream_proposals d JOIN projects p ON p.id = d.project_id
		WHERE d.state = 'pending_approval'
		ORDER BY d.created_at ASC, d.id ASC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Item, error) {
		var projName, key, kind, targetPath, title string
		var since time.Time
		if err := r.Scan(&projName, &key, &kind, &targetPath, &title, &since); err != nil {
			return Item{}, err
		}
		return Item{
			ID:      "dream/" + projName + "/" + key,
			Kind:    KindDreamProposal,
			Project: projName,
			Title:   fmt.Sprintf("Dream proposal %s in %s: %s", key, projName, title),
			Body:    fmt.Sprintf("Target %s (%s) passed static vetting and waits for your fresh-session approval before touching main.", targetPath, kind),
			Since:   since,
			Alternatives: []Alternative{
				{Label: "Inspect Knowledge & Dreamstore", Link: "/p/" + projName + "/knowledge", Main: true},
			},
		}, nil
	})
}
