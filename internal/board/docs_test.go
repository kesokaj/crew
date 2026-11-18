package board_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kesokaj/crew/internal/board"
)

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// commit writes one file on main, as a merge of a ticket would, with its trailer.
func (e *env) commit(p *board.Project, path, content, ticket string) {
	e.t.Helper()
	msg := "Change " + path
	if ticket != "" {
		msg += "\n\nTicket: " + ticket + "\n"
	}
	if _, err := e.git.CommitPage(e.ctx, p.Name, e.git.Main(e.ctx, p.Name), path, []byte(content), msg, me); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) doc(p *board.Project, path string) *board.DocPage {
	e.t.Helper()
	d, err := e.b.Doc(e.ctx, p, path)
	if err != nil {
		e.t.Fatal(err)
	}
	return d
}

// The pages on main are indexed (9.9) with their kind, title, and what they cover. Documentation falls
// behind the code (21, row 24): a change of covered code that leaves the page as it is marks the page,
// with the ticket that did it; a change of the page makes it current again, and names its ticket.
func TestThePagesAreIndexedAndMarkedWhenTheirCodeMoves(t *testing.T) {
	e := newEnv(t)
	p := e.running("shop", "SF")
	e.commit(p, "docs/architecture.md", "---\ncovers: [internal/mail/**, cmd/]\n---\n# How it fits\n\nThe mailer sends receipts.\n", "SF-1")
	e.commit(p, "docs/decisions/0001-cents.md", "# 0001. Cents\n", "")
	e.commit(p, "notes.md", "# Not a page\n", "")
	if err := e.b.IndexDocs(e.ctx, p); err != nil {
		t.Fatal(err)
	}
	pages, err := e.b.Docs(e.ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, d := range pages {
		kinds[d.Path] = d.Kind + ":" + d.Title
	}
	if len(pages) != 4 || kinds["README.md"] != "overview:shop" || kinds["CHANGELOG.md"] != "changelog:Changelog" ||
		kinds["docs/architecture.md"] != "architecture:How it fits" || kinds["docs/decisions/0001-cents.md"] != "decision:0001. Cents" {
		t.Fatalf("pages %v", kinds)
	}
	arch := e.doc(p, "docs/architecture.md")
	if !arch.Current() || len(arch.Covers) != 2 || deref(arch.LastTicket) != "SF-1" || len(arch.Tickets) != 1 {
		t.Fatalf("architecture %+v", arch)
	}

	// Code it covers changes, and the page does not: it is marked, by the ticket that changed it.
	e.commit(p, "internal/mail/send.go", "package mail\n", "SF-1")
	e.commit(p, "internal/cart/cart.go", "package cart\n", "")
	if err := e.b.IndexDocs(e.ctx, p); err != nil {
		t.Fatal(err)
	}
	arch = e.doc(p, "docs/architecture.md")
	if arch.Current() || deref(arch.OutdatedBy) != "SF-1" {
		t.Fatalf("after its code changed: %+v", arch)
	}
	if !e.doc(p, "README.md").Current() {
		t.Fatal("the overview covers no code, and was marked")
	}
	// The page changes: current again.
	e.clock.Add(time.Minute)
	e.commit(p, "docs/architecture.md", "---\ncovers: [internal/mail/**]\n---\n# How it fits\n\nThe mailer sends receipts and resets.\n", "SF-1")
	e.b.IndexDocs(e.ctx, p)
	if arch = e.doc(p, "docs/architecture.md"); !arch.Current() || !strings.Contains(arch.Body, "resets") {
		t.Fatalf("after the page changed: %+v", arch)
	}
	hits, err := e.b.SearchDocs(e.ctx, p, "receipts", 10)
	if err != nil || len(hits) != 1 || hits[0].Path != "docs/architecture.md" || !strings.Contains(hits[0].Snippet, "**receipts**") {
		t.Fatalf("search %+v %v", hits, err)
	}
}

// The operator and an agent edit the same page (21, row 25): the operator's edit is a commit on main
// with the operator as author; an edit from a page that changed since is refused; the changelog is
// Crew's, and not a page the operator edits.
func TestTheOperatorsEditOfAPage(t *testing.T) {
	e := newEnv(t)
	p := e.running("shop", "SF")
	e.b.IndexDocs(e.ctx, p)
	base := e.git.Main(e.ctx, p.Name)
	commit, err := e.b.EditPage(e.ctx, p, "docs/setup.md", base, "# Setup\n\nmake run\n", me)
	if err != nil {
		t.Fatal(err)
	}
	if d := e.doc(p, "docs/setup.md"); d.LastCommit != commit || d.Kind != "setup" {
		t.Fatalf("the edited page %+v", d)
	}
	// An agent's ticket changes the page on main meanwhile.
	e.commit(p, "docs/setup.md", "# Setup\n\nmake build run\n", "SF-1")
	if _, err := e.b.EditPage(e.ctx, p, "docs/setup.md", commit, "# Setup\n\nmine\n", me); kindOf(err) != board.Conflict {
		t.Fatalf("an edit over a newer page: %v", err)
	}
	if _, err := e.b.EditPage(e.ctx, p, "CHANGELOG.md", e.git.Main(e.ctx, p.Name), "x", me); kindOf(err) != board.Invalid {
		t.Fatalf("an edit of the changelog: %v", err)
	}
	if _, err := e.b.EditPage(e.ctx, p, "internal/x.go", e.git.Main(e.ctx, p.Name), "x", me); kindOf(err) != board.Invalid {
		t.Fatalf("an edit of code: %v", err)
	}
}

// A decision record is a row at once, with its page; what Crew writes into the ticket's merge holds the
// changelog's entry on top of the entries, and the page (9.9).
func TestADecisionRecordAndTheChangelogGoIntoTheMerge(t *testing.T) {
	e := newEnv(t)
	p := e.running("shop", "SF")
	tk := e.ticket(p, "SF-1")
	if _, _, err := e.b.RecordDecision(e.ctx, p, 0, "", "builder", board.NewDecisionRecord{Title: "x", Context: "c", Decision: "d", Reason: "r"}, "docs"); kindOf(err) != board.Refused {
		t.Fatalf("a record on no ticket: %v", err)
	}
	n, page, err := e.b.RecordDecision(e.ctx, p, tk.ID, "", "builder", board.NewDecisionRecord{Title: "Money is stored as integer cents",
		Context: "Floats lose cents.", Decision: "Store cents as integers.", Reason: "Exact sums.", Replaces: "3"}, "docs")
	if err != nil || n != 1 || page != "docs/decisions/0001-money-is-stored-as-integer-cents.md" {
		t.Fatalf("%d %s %v", n, page, err)
	}
	o, err := e.b.MergeOverlay(e.ctx, tk, "CHANGELOG.md", "Ran make test.", time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC))
	if err != nil || len(o) != 2 {
		t.Fatalf("%v %v", o, err)
	}
	log := string(o["CHANGELOG.md"]([]byte("# Changelog\n\nOne entry per ticket.\n\n## SF-0: Old\n")))
	if !strings.HasPrefix(log, "# Changelog\n\nOne entry per ticket.\n\n## SF-1: First\n\n2026-10-17 · feature\n") ||
		!strings.Contains(log, "The builder's last note: Ran make test.\n\n## SF-0: Old") {
		t.Fatalf("the changelog:\n%s", log)
	}
	doc := string(o[page](nil))
	for _, want := range []string{"# 0001. Money is stored as integer cents", "- Ticket: SF-1", "## Context\n\nFloats lose cents.\n\nReplaces decision 0003.", "## Reason\n\nExact sums."} {
		if !strings.Contains(doc, want) {
			t.Fatalf("no %q in:\n%s", want, doc)
		}
	}
}

// Crew writes agents' text into a merge only where it is documentation: a changelog or a decisions
// folder that settings put on a file that instructs agents is left out (security review of M12). A
// page path that leaves its folder is refused as one.
func TestCrewWritesNoAgentTextIntoInstructions(t *testing.T) {
	e := newEnv(t)
	p := e.running("shop", "SF")
	tk := e.ticket(p, "SF-1")
	if _, _, err := e.b.RecordDecision(e.ctx, p, tk.ID, "", "builder", board.NewDecisionRecord{Title: "x", Context: "c", Decision: "d", Reason: "r"}, ".agents/rules"); err != nil {
		t.Fatal(err)
	}
	o, err := e.b.MergeOverlay(e.ctx, tk, "AGENTS.md", "note", time.Now())
	if err != nil || len(o) != 0 {
		t.Fatalf("the overlay writes %d files: %v", len(o), err)
	}
	if _, err := e.b.EditPage(e.ctx, p, "docs/../AGENTS.md", e.git.Main(e.ctx, p.Name), "x", me); kindOf(err) != board.Invalid {
		t.Fatalf("a path out of docs/: %v", err)
	}
}

// Reading the pages and the skills costs git work only for what changed: a page indexed already is not
// read again when main moves, and the skills listing is read once for a main (security review of M12).
func TestThePagesAndTheSkillsAreReadOnceForWhatChanged(t *testing.T) {
	e := newEnv(t)
	p := e.running("shop", "SF")
	log := filepath.Join(t.TempDir(), "git.log")
	script := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$*\" >> "+log+"\nexec git \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.git.Git = script
	calls := func(what string) int {
		b, _ := os.ReadFile(log)
		os.Remove(log)
		return strings.Count(string(b), what)
	}
	if err := e.b.IndexDocs(e.ctx, p); err != nil {
		t.Fatal(err)
	}
	if n := calls("cat-file blob"); n != 2 {
		t.Fatalf("the first index read %d pages", n) // README.md and CHANGELOG.md
	}
	e.commit(p, "lib/cart.go", "package lib\n", "")
	calls("")
	if err := e.b.IndexDocs(e.ctx, p); err != nil {
		t.Fatal(err)
	}
	if n := calls("cat-file blob"); n != 0 {
		t.Fatalf("an index after main moved read %d unchanged pages again", n)
	}
	if _, err := e.b.ProjectLayer(e.ctx, p); err != nil {
		t.Fatal(err)
	}
	calls("")
	if _, err := e.b.ProjectLayer(e.ctx, p); err != nil {
		t.Fatal(err)
	}
	if n := calls(" log "); n != 0 {
		t.Fatalf("a second listing on the same main ran git log %d times", n)
	}
}

// A skills listing with a read that failed is not kept: the next one reads again, and has what the
// first could not read (review of M12, round 2).
func TestASkillsListingWithAFailedReadIsReadAgain(t *testing.T) {
	e := newEnv(t)
	p := e.running("shop", "SF")
	e.commit(p, ".agents/skills/flows/SKILL.md", "---\nname: flows\ndescription: Test a purchase.\n---\n", "SF-1")
	script := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncase \"$*\" in *cat-file\\ blob*) exit 1;; esac\nexec git \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.git.Git = script
	first, err := e.b.ProjectLayer(e.ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	e.git.Git = ""
	second, err := e.b.ProjectLayer(e.ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	desc := func(l []board.LayerItem) string {
		for _, it := range l {
			if it.Kind == "skill" {
				return it.Description
			}
		}
		return "none"
	}
	if desc(first) != "" || desc(second) != "Test a purchase." {
		t.Fatalf("first %q, then %q", desc(first), desc(second))
	}
}
