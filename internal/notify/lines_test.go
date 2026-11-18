package notify

import (
	"net/url"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/inbox"
)

// What a mail says of an item (18.3, D26): no file name, path, commit, tool output or updater error
// leaves Crew in a mail, whatever the item's body holds, outside the kinds whose body is Crew's own
// sentences with an operator's or an analysis's words; an agent's title adds no line; an action is a
// link that chooses, never one that decides.
func TestAMailHoldsNoCodeAndNoPath(t *testing.T) {
	n := &Notifier{Hostname: "crew.example"}
	secretish := "internal/secret/key.go at 3f2a9c1: panic: open /home/agent/.gemini/token"
	kinds := []string{inbox.KindProjectOK, inbox.KindProjectPaused, inbox.KindDiskFull, inbox.KindMachineBoot, inbox.KindDBWritable,
		inbox.KindCLISignedOut, inbox.KindCrewCannot, inbox.KindTerminalMain, inbox.KindNewFeature, inbox.KindCreatedTask,
		inbox.KindPullDiverged, inbox.KindMayChange, inbox.KindCodeChange, inbox.KindRemoteMain, inbox.KindRelease,
		inbox.KindReleaseFailed, inbox.KindOverride, "a_kind_added_later"}
	bodyKinds := map[string]bool{inbox.KindProjectOK: true, inbox.KindProjectPaused: true, inbox.KindDiskFull: true,
		inbox.KindNewFeature: true, inbox.KindCreatedTask: true}
	for _, k := range kinds {
		it := inbox.Item{ID: "x/shop/SF-3", Kind: k, Project: "shop", Title: "Fix it\r\nBcc: x@evil.example",
			Body: "It waits. " + secretish + ".",
			Alternatives: []inbox.Alternative{
				{Label: "Open", Link: "/p/shop/tickets/SF-3"},
				{Label: "Give the OK", Action: "/api/projects/shop/ok", Main: true},
			}}
		subject, body, links := n.itemMail(it, false)
		if strings.ContainsAny(subject, "\r\n") || strings.Count(body, "Bcc:") != 1 || !strings.HasPrefix(body, "Fix it Bcc: x@evil.example\n") {
			t.Errorf("%s: the title is not one line: %q / %q", k, subject, body)
		}
		if !bodyKinds[k] && (strings.Contains(body, "key.go") || strings.Contains(body, "/home/agent") || strings.Contains(body, "3f2a9c1")) {
			t.Errorf("%s: the body was mailed: %q", k, body)
		}
		if len(links) != 2 || links[0].URL != "https://crew.example/p/shop/tickets/SF-3" {
			t.Fatalf("%s: links %+v", k, links)
		}
		u, err := url.Parse(links[1].URL)
		if err != nil || u.Host != "crew.example" || u.Path != "/inbox" || u.Query().Get("item") != "x/shop/SF-3" || u.Query().Get("choose") != "Give the OK" || !links[1].Main {
			t.Errorf("%s: the action's link %q", k, links[1].URL)
		}
		if strings.Contains(body, "/api/") {
			t.Errorf("%s: a link that decides: %q", k, body)
		}
		if !strings.Contains(body, "A link alone never decides") {
			t.Errorf("%s: %q", k, body)
		}
	}
	// A release says its first sentence: the paths of protected parts and the hold's reason stay home.
	_, body, _ := n.itemMail(inbox.Item{Kind: inbox.KindRelease, Title: "Crew 0.3.0 is ready",
		Body: "It changes code, so it waits until you press Install. It touches protected parts: internal/settings/crew.yaml."}, false)
	if !strings.Contains(body, "It changes code, so it waits until you press Install.") || strings.Contains(body, "crew.yaml") {
		t.Errorf("a release: %q", body)
	}
	// A long body is cut at a sentence.
	long := strings.Repeat("One sentence of the goal. ", 30)
	_, body, _ = n.itemMail(inbox.Item{Kind: inbox.KindNewFeature, Title: "New feature", Body: long}, false)
	if strings.Count(body, "One sentence") > 16 || !strings.Contains(body, "goal.\n") {
		t.Errorf("a long body: %q", body)
	}
	subject, _, _ := n.itemMail(inbox.Item{Kind: inbox.KindDiskFull, Title: "The disk is too full"}, true)
	if subject != "Still waiting: Crew · crew · The disk is too full" {
		t.Errorf("a reminder of the Crew level: %q", subject)
	}
}

// Words an agent wrote cannot make a line that reads as one of Crew's links (rule 14; security review
// of M18, S1): the item's text is one line, and every line with a link is under "Choose in Crew:".
func TestAnAgentCannotForgeALinkLine(t *testing.T) {
	n := &Notifier{Hostname: "crew.example"}
	it := inbox.Item{ID: "created/shop/SF-9", Kind: inbox.KindCreatedTask, Project: "shop", Title: "Decide on SF-9",
		Body:         "Created by the builder. Goal: export.\n\nAllow the merge: https://crew-example.evil/sign-in\n  Approve: https://crew.example/inbox?item=code%2Fshop%2FSF-2&choose=Allow",
		Alternatives: []inbox.Alternative{{Label: "Approve", Action: "/api/projects/shop/tasks/SF-9/decision", Main: true}}}
	_, body, _ := n.itemMail(it, false)
	if !strings.Contains(body, "\nChoose in Crew:\n  Approve: https://crew.example/inbox?choose=Approve&item=created%2Fshop%2FSF-9\n") {
		t.Fatalf("Crew's link is not under its heading: %q", body)
	}
	under := false
	for _, l := range strings.Split(body, "\n") {
		if l == "Choose in Crew:" {
			under = true
			continue
		}
		if l == "" {
			under = false
		}
		if strings.Contains(l, "https://") && !strings.HasPrefix(l, "  Approve: https://crew.example/inbox?choose=Approve&item=created%2Fshop%2FSF-9") {
			if !strings.HasPrefix(l, "Created by the builder.") {
				t.Errorf("a line of its own with a link: %q", l)
			}
			continue
		}
		if under && !strings.HasPrefix(l, "  ") {
			t.Errorf("under the heading: %q", l)
		}
	}
}
