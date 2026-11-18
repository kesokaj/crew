package board

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/gitstore"
)

// What Crew writes into a ticket's merge commit itself (9.9): the changelog's entry (R107) and the
// pages of the ticket's decision records (R106).

// NewDecisionRecord is what record_decision gives: a design decision later work must follow.
type NewDecisionRecord struct {
	Title    string `json:"title"`
	Context  string `json:"context"`
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
	// Replaces names an earlier record this one replaces, such as 0003; a record is never edited.
	Replaces string `json:"replaces"`
}

var slugPart = regexp.MustCompile(`[^a-z0-9]+`)

// slug is a title as a part of a file name.
func slug(title string) string {
	s := strings.Trim(slugPart.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if len(s) > 60 {
		s = strings.TrimRight(s[:60], "-")
	}
	if s == "" {
		s = "decision"
	}
	return s
}

// RecordDecision saves a decision record of a run (9.9): a row at once, with the page it gets in its
// ticket's merge commit, docs/decisions/NNNN-title.md, numbered in its project.
func (b *Board) RecordDecision(ctx context.Context, p *Project, ticket int64, run, role string, n NewDecisionRecord, docsRoot string) (int, string, error) {
	n.Title, n.Context, n.Decision, n.Reason = strings.TrimSpace(n.Title), strings.TrimSpace(n.Context), strings.TrimSpace(n.Decision), strings.TrimSpace(n.Reason)
	switch {
	case n.Title == "" || utf8.RuneCountInString(n.Title) > 120 || strings.ContainsAny(n.Title, "\r\n"):
		return 0, "", errf(Invalid, "title: one line of at most 120 characters")
	case n.Context == "" || n.Decision == "" || n.Reason == "":
		return 0, "", errf(Invalid, "context, decision, and reason: each is needed")
	case len(n.Context)+len(n.Decision)+len(n.Reason) > maxText:
		return 0, "", errf(Invalid, "context, decision, and reason: at most %d characters together", maxText)
	case n.Replaces != "" && !regexp.MustCompile(`^\d{1,4}$`).MatchString(n.Replaces):
		return 0, "", errf(Invalid, "replaces: the number of an earlier record, such as 0003")
	}
	if ticket == 0 {
		return 0, "", errf(Refused, "a decision record is written into a ticket's merge commit: this run works on no ticket")
	}
	if docsRoot == "" {
		docsRoot = "docs"
	}
	var number int
	var page string
	err := b.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := projectByRef(ctx, tx, p.ID, true); err != nil {
			return err
		}
		var n2 int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM decisions WHERE project = $1 AND record`, p.ID).Scan(&n2); err != nil {
			return err
		}
		number = n2 + 1
		if number > 9999 {
			return errf(Refused, "the project has 9999 decision records")
		}
		page = fmt.Sprintf("%s/decisions/%04d-%s.md", strings.Trim(docsRoot, "/"), number, slug(n.Title))
		ctxText := n.Context
		if n.Replaces != "" {
			var r int
			fmt.Sscan(n.Replaces, &r)
			ctxText += fmt.Sprintf("\n\nReplaces decision %04d.", r)
		}
		_, err := tx.Exec(ctx, `INSERT INTO decisions (project, ticket, run, role, question, alternatives, choice, reason, record, context, page)
			VALUES ($1, $2, nullif($3, '')::uuid, $4, $5, '[]', $6, $7, true, $8, $9)`, p.ID, ticket, run, role, n.Title, n.Decision, n.Reason, ctxText, page)
		return err
	})
	return number, page, err
}

// decisionPage is a decision record's page, as it is written into the merge commit.
func decisionPage(number int, title, ticket, role, context, decision, reason string, at time.Time) []byte {
	return []byte(fmt.Sprintf("# %04d. %s\n\n- Date: %s\n- Ticket: %s\n- Recorded by: the %s\n\n## Context\n\n%s\n\n## Decision\n\n%s\n\n## Reason\n\n%s\n",
		number, title, at.UTC().Format("2006-01-02"), ticket, role, context, decision, reason))
}

// MergeOverlay is what Crew writes into a ticket's merge commit (9.9): the changelog's entry on top of
// main's file, and the pages of the ticket's decision records. note is the builder's last note.
func (b *Board) MergeOverlay(ctx context.Context, t *Ticket, changelog, note string, now time.Time) (gitstore.Overlay, error) {
	o := gitstore.Overlay{}
	if changelog != "" {
		entry := ChangelogEntry(t, note, now)
		o[changelog] = func(onMain []byte) []byte { return InsertChangelogEntry(onMain, entry) }
	}
	rows, err := b.Pool.Query(ctx, `SELECT d.page, d.question, d.role, coalesce(d.context, ''), d.choice, d.reason, d.created_at
		FROM decisions d WHERE d.ticket = $1 AND d.record AND d.page IS NOT NULL ORDER BY d.id`, t.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var page, title, role, ctxText, choice, reason string
		var at time.Time
		if err := rows.Scan(&page, &title, &role, &ctxText, &choice, &reason, &at); err != nil {
			return nil, err
		}
		var number int
		fmt.Sscanf(page[strings.LastIndex(page, "/")+1:], "%04d", &number)
		content := decisionPage(number, title, t.Key, role, ctxText, choice, reason, at)
		o[page] = func([]byte) []byte { return content }
	}
	// Crew writes what agents wrote into these files after the checks, outside may_change: never into
	// one that instructs agents or runs as code, whatever docs.changelog and docs.root say (13.8,
	// security review of M12).
	for p := range o {
		if cli.Matches(p, cli.InstructionFiles()) {
			b.log().Warn("a page Crew writes into a merge is a file that instructs agents, and is left out", "ticket", t.Key, "path", p)
			delete(o, p)
		}
	}
	return o, rows.Err()
}

// ChangelogEntry is a ticket's entry in the changelog (R107): its title, its what, and the builder's
// last note.
func ChangelogEntry(t *Ticket, note string, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s: %s\n\n%s · %s\n", t.Key, oneLine(t.Title), now.UTC().Format("2006-01-02"), t.Kind)
	if what := strings.TrimSpace(t.What); what != "" {
		fmt.Fprintf(&b, "\n%s\n", cut(what, 1500))
	}
	if note = strings.TrimSpace(note); note != "" {
		fmt.Fprintf(&b, "\nThe builder's last note: %s\n", cut(note, 1500))
	}
	return b.String()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// InsertChangelogEntry puts an entry on top of a changelog's entries: after its title and the text
// under it, before the first entry. A file with no title gets one.
func InsertChangelogEntry(old []byte, entry string) []byte {
	s := string(old)
	if strings.TrimSpace(s) == "" {
		return []byte("# Changelog\n\n" + entry)
	}
	if i := strings.Index(s, "\n## "); i >= 0 {
		return []byte(s[:i+1] + entry + "\n" + s[i+1:])
	}
	if strings.HasPrefix(s, "## ") {
		return []byte(entry + "\n" + s)
	}
	return []byte(strings.TrimRight(s, "\n") + "\n\n" + entry)
}
