package board_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
)

// request_skill makes a skill ticket in the Backlog, created by the agent, and a request for the same
// thing joins the open one (12.9).
func TestARequestForASkillIsOneTicket(t *testing.T) {
	e := newEnv(t)
	p := e.running("shop", "SF")
	r := board.SkillRequest{Missing: "I could not test a whole purchase.", SeenIn: "SF-1, the checkout test", Wanted: "How to test a whole purchase end to end"}
	tk, joined, err := e.b.RequestSkill(e.ctx, p, r, "", "builder", "builder · r_1")
	if err != nil || joined || !tk.Skill || tk.Kind != "chore" || tk.Origin != board.OriginAgent || tk.Stage != "backlog" ||
		!strings.Contains(*tk.Seen, "the checkout test") {
		t.Fatalf("%+v %v %v", tk, joined, err)
	}
	again, joined, err := e.b.RequestSkill(e.ctx, p, board.SkillRequest{Missing: "x", SeenIn: "SF-3", Wanted: "Testing a purchase end to end"}, "", "tester", "tester · r_2")
	if err != nil || !joined || again.Key != tk.Key {
		t.Fatalf("the same request: %s %v %v", again.Key, joined, err)
	}
	other, joined, err := e.b.RequestSkill(e.ctx, p, board.SkillRequest{Missing: "x", SeenIn: "y", Wanted: "Writing database migrations"}, "", "builder", "builder · r_3")
	if err != nil || joined || other.Key == tk.Key {
		t.Fatalf("another request: %s %v %v", other.Key, joined, err)
	}
	rs, _ := e.b.SkillRequests(e.ctx, tk)
	if len(rs) != 2 {
		t.Fatalf("requests %+v", rs)
	}
	if _, _, err := e.b.RequestSkill(e.ctx, p, board.SkillRequest{Wanted: "x"}, "", "builder", "builder · r_1"); kindOf(err) != board.Invalid {
		t.Fatalf("a request with nothing seen: %v", err)
	}
}

// A skill from outside carries instructions that send code or data away (21, row 9): its provenance is
// recorded, row by row, or the check says what is missing.
func TestVendoredRowsNameTheirSourcePinAndLicence(t *testing.T) {
	md := "# Vendored\n\n| Skill | Source | Pin | License |\n|---|---|---|---|\n" +
		"| flows | github.com/x/skills (`skills/flows`) | 6654f6b | MIT |\n" +
		"| sender | github.com/y/skills | main | |\n| bare | | | |\n"
	got := board.VendoredRows(md)
	if len(got) != 2 || got[0] != "sender has no a pin of a commit, no a licence" || !strings.HasPrefix(got[1], "bare has no a source") {
		t.Fatalf("%q", got)
	}
	if got := board.VendoredRows(""); len(got) != 0 {
		t.Fatalf("no file: %q", got)
	}
}

// The In progress exit of a ticket that changes the project's skills checks VENDORED.md (row 9), and
// the project's layer on main lists the skill once it merged (12.9).
func TestAChangeOfSkillsNamesItsProvenance(t *testing.T) {
	e := newEnv(t)
	p := e.running("shop", "SF")
	e.reconcile(p)
	e.approve(p, "SF-1", board.Decision{DocsReason: ptr("none"), MayChange: []string{".agents/skills/**"}})
	e.reconcile(p)
	e.b.Checks = scripted()
	branch := *e.ticket(p, "SF-1").Branch
	e.push(p, branch, map[string]string{".agents/skills/flows/SKILL.md": "---\nname: flows\ndescription: Test a purchase.\n---\n# Flows\n",
		".agents/skills/VENDORED.md": "| Skill | Source | Pin | License |\n|---|---|---|---|\n| flows | github.com/x/skills | | MIT |\n"})
	e.reconcile(p)
	if w := *e.ticket(p, "SF-1").Waiting; !strings.Contains(w, "VENDORED.md: flows has no a pin of a commit") {
		t.Fatalf("waiting: %s", w)
	}
	e.push(p, branch, map[string]string{".agents/skills/VENDORED.md": "| Skill | Source | Pin | License |\n|---|---|---|---|\n| flows | github.com/x/skills | 6654f6b | MIT |\n"})
	e.reconcile(p)
	if e.stage(p, "SF-1") != "done" {
		t.Fatalf("SF-1 is in %s, waiting %v", e.stage(p, "SF-1"), e.ticket(p, "SF-1").Waiting)
	}
	// The scripted checks stand in for the merge here: main gets the skill as the merge gives it.
	e.commit(p, ".agents/skills/flows/SKILL.md", "---\nname: flows\ndescription: Test a purchase.\n---\n# Flows\n", "SF-1")
	layer, err := e.b.ProjectLayer(e.ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, it := range layer {
		names[it.Kind+":"+it.Name] = it.Description
	}
	if _, ok := names["context:AGENTS.md"]; !ok || names["skill:flows"] != "Test a purchase." || deref(layer[0].Ticket) != "SF-1" {
		t.Fatalf("layer %+v", layer)
	}
}

// A short request that shares only common words with an open skill ticket is a ticket of its own; the
// same request in other words joins (review of M12).
func TestASkillRequestJoinsOnlyTheSameRequest(t *testing.T) {
	e := newEnv(t)
	p := e.running("shop", "SF")
	first, _, err := e.b.RequestSkill(e.ctx, p, board.SkillRequest{Missing: "m", SeenIn: "s", Wanted: "Write database migrations"}, "", "builder", "builder · r_1")
	if err != nil {
		t.Fatal(err)
	}
	short, joined, err := e.b.RequestSkill(e.ctx, p, board.SkillRequest{Missing: "m", SeenIn: "s", Wanted: "a skill"}, "", "tester", "tester · r_2")
	if err != nil || joined || short.Key == first.Key {
		t.Fatalf("a short request joined %s: %v", short.Key, err)
	}
	same, joined, _ := e.b.RequestSkill(e.ctx, p, board.SkillRequest{Missing: "m", SeenIn: "s", Wanted: "Writing database migrations"}, "", "lead", "lead · r_3")
	if !joined || same.Key != first.Key {
		t.Fatalf("the same request: %s %v", same.Key, joined)
	}
}

// A branch cannot take away the record of where the project's skills came from (security review of
// M12).
func TestABranchThatRemovesVendoredMDDoesNotPass(t *testing.T) {
	e := newEnv(t)
	p := e.running("shop", "SF")
	e.commit(p, ".agents/skills/VENDORED.md", "| Skill | Source | Pin | License |\n|---|---|---|---|\n| flows | github.com/x/skills | 6654f6b | MIT |\n", "")
	e.reconcile(p)
	e.approve(p, "SF-1", board.Decision{DocsReason: ptr("none"), MayChange: []string{".agents/skills/**"}})
	e.reconcile(p)
	e.b.Checks = scripted()
	branch := *e.ticket(p, "SF-1").Branch
	work := e.t.TempDir()
	git(t, work, "clone", "-q", e.git.Quarantine.Path(p.Name), ".")
	git(t, work, "checkout", "-q", "-B", branch)
	git(t, work, "rm", "-q", ".agents/skills/VENDORED.md")
	os.MkdirAll(filepath.Join(work, ".agents/skills/x"), 0o755)
	os.WriteFile(filepath.Join(work, ".agents/skills/x/SKILL.md"), []byte("# x\n"), 0o644)
	git(t, work, "add", "-A")
	git(t, work, "commit", "-q", "-m", "work")
	git(t, work, "push", "-q", "origin", branch)
	e.reconcile(p)
	if w := deref(e.ticket(p, "SF-1").Waiting); !strings.Contains(w, "takes rows out of .agents/skills/VENDORED.md: flows") {
		t.Fatalf("waiting: %q", w)
	}
	// Emptied, not deleted: the same.
	e.push(p, branch, map[string]string{".agents/skills/VENDORED.md": "# Vendored\n"})
	e.reconcile(p)
	if w := deref(e.ticket(p, "SF-1").Waiting); !strings.Contains(w, "takes rows out") {
		t.Fatalf("emptied: %q", w)
	}
	// The rows put back, with one more: it passes.
	e.push(p, branch, map[string]string{".agents/skills/VENDORED.md": "| Skill | Source | Pin | License |\n|---|---|---|---|\n" +
		"| flows | github.com/x/skills | 6654f6b | MIT |\n| x | github.com/y/skills | 435076e | MIT |\n"})
	e.reconcile(p)
	if e.stage(p, "SF-1") != "done" {
		t.Fatalf("SF-1 is in %s, waiting %q", e.stage(p, "SF-1"), deref(e.ticket(p, "SF-1").Waiting))
	}
}

// A branch cut before main had VENDORED.md takes nothing out of it: the rows are measured from where the
// branch left main (review of M12, round 2).
func TestAnOlderBranchDoesNotRemoveRowsItNeverHad(t *testing.T) {
	if got := board.RemovedRows("", "| Skill | Source |\n|---|---|\n| a | b |\n"); len(got) != 0 {
		t.Fatalf("%q", got)
	}
	if got := board.RemovedRows("| Skill | Source |\n|---|---|\n| a | b |\n| c | d |\n", "| Skill | Source |\n|---|---|\n| c | d |\n"); len(got) != 1 || got[0] != "a" {
		t.Fatalf("%q", got)
	}
	e := newEnv(t)
	p := e.running("shop", "SF")
	e.reconcile(p)
	e.approve(p, "SF-1", board.Decision{DocsReason: ptr("none"), MayChange: []string{".agents/skills/**"}})
	e.reconcile(p)
	e.b.Checks = scripted()
	branch := *e.ticket(p, "SF-1").Branch
	// main gets the file after the branch was cut; the branch adds a skill of its own.
	e.commit(p, ".agents/skills/VENDORED.md", "| Skill | Source | Pin | License |\n|---|---|---|---|\n| flows | github.com/x/skills | 6654f6b | MIT |\n", "")
	e.push(p, branch, map[string]string{".agents/skills/mine/SKILL.md": "# mine\n"})
	e.reconcile(p)
	if w := deref(e.ticket(p, "SF-1").Waiting); strings.Contains(w, "takes rows out") {
		t.Fatalf("an older branch: %q", w)
	}
}
