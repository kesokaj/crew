package versions

import (
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/release"
)

// The gate's report on the release page names the live part, what was not run, and a hold, and each
// text is one plain line in which Markdown reads nothing: a step's name is what agents' code may have
// printed, and its detail stays on the machine (security review of M16 P4).
func TestTheGatesReportIsPlainAndNamesTheLivePart(t *testing.T) {
	r := gateReport(release.Gate{
		Steps:       []release.GateStep{{Name: "make check\n# Owned [x](https://evil.example)", OK: true, Detail: "admin@example.org"}},
		InstallTest: "skipped", Live: release.LiveSkipped,
		LiveSteps: []release.GateStep{{Name: "the real ticket on crew-gate", OK: false}},
		Skipped:   []string{"the real ticket: agy is signed out"}, Hold: "shop measured worse",
	})
	for _, want := range []string{"- make check\\# Owned \\[x\\]\\(https\\://evil\\.example\\): passed\n", "- The live steps: skipped\n",
		"- the real ticket on crew\\-gate: not run\n", "- Not run: the real ticket\\: agy is signed out\n", "- Held: shop measured worse\n"} {
		if !strings.Contains(r, want) {
			t.Errorf("no %q in\n%s", want, r)
		}
	}
	// Since 0.53 no cost is reported.
	if strings.Contains(r, "admin@example.org") || strings.Count(r, "\n#") != 0 || strings.Contains(r, "cost") || strings.Contains(r, "$") {
		t.Fatalf("the report:\n%s", r)
	}
	// No mention on GitHub, and no link.
	if r := gateReport(release.Gate{Hold: "ask @ops at https://evil.example"}); !strings.Contains(r, "ask @\u200bops at https\\://evil\\.example") {
		t.Fatalf("a mention or a link:\n%s", r)
	}
}

// A release's notes reach its GitHub page as text: each line of them, a ticket's title or a commit's
// subject, which agents and the operator wrote, renders no link, image, mention, reference, or HTML
// (security review of 95d116d). notes.md on the machine stays plain.
func TestTheNotesArePublishedAsText(t *testing.T) {
	notes := "# Crew 0.2.20: 1 ticket\n\nSince 0.2.19 (79f63a9c21aa):\n\nTickets:\n\n" +
		"- CREW-1: [Install steps](https://evil.example) ![](https://t.example/p.png) @ops org/repo#1 <details>x</details>\n"
	got := publishedNotes(notes)
	for _, want := range []string{"# Crew 0\\.2\\.20\\: 1 ticket\n", "- CREW\\-1\\: \\[Install steps\\]\\(https\\://evil\\.example\\) \\!\\[\\]\\(https\\://t\\.example/p\\.png\\) @​ops org/repo\\#1 \\<details\\>x\\</details\\>\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, got)
		}
	}
	if strings.Count(got, "\n- ") != 1 || !strings.HasPrefix(got, "# ") {
		t.Fatalf("the lines' own marks are kept:\n%s", got)
	}
	// No control, invisible, or direction-changing character reaches the page, which would make a line
	// read as another (P5 of M16).
	if got := publishedNotes("- CREW-2: Fix\u202e the\u0007 bell\u200b\n"); got != "- CREW\\-2\\: Fix the bell\n" {
		t.Fatalf("control characters: %q", got)
	}
	// An entity is text too, and a long line keeps its end, the commit's hash (round 2 of the reviews
	// of 95d116d).
	long := "- " + strings.Repeat("y", 150) + " &commat;ops org/repo&num;1 (abcdef1)\n"
	got = publishedNotes(long)
	if !strings.Contains(got, "\\&commat;ops") || !strings.Contains(got, "\\&num;1") || !strings.HasSuffix(got, "\\(abcdef1\\)\n") {
		t.Fatalf("a long line with entities:\n%s", got)
	}
}

// A server build's title counts what it holds in Crew's words; a list that was cut says so (round 2
// of the reviews of 95d116d).
func TestABuildsTitleCountsWhatItHolds(t *testing.T) {
	two := []ChangeTicket{{Key: "A-1"}, {Key: "A-2"}}
	one := []ChangeCommit{{Commit: "c"}}
	for _, tc := range []struct {
		ch   Changes
		want string
	}{
		{Changes{Tickets: two, Outside: one}, "2 tickets and 1 commit from outside"},
		{Changes{Tickets: two[:1]}, "1 ticket"},
		{Changes{Outside: one, Cut: ChangesCut{Outside: true}}, "more than 1 commit from outside"},
		{Changes{Tickets: two, Cut: ChangesCut{Tickets: true}}, "more than 2 tickets"},
		{Changes{}, ""},
	} {
		if got := heldTitle(&tc.ch); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.ch, got, tc.want)
		}
	}
}
