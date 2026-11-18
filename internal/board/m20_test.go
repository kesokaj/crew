package board_test

import (
	"os"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
)

const (
	m20Conv  = "00000000-0000-4000-8000-0000000020d1"
	m20Other = "00000000-0000-4000-8000-0000000020d2"
	m20Turn  = "00000000-0000-4000-8000-0000000020e1"
)

// proposed is a ticket the assistant of the conversation conv proposed with create_ticket.
func (e *env) proposed(p *board.Project, title, conv string) *board.Ticket {
	e.t.Helper()
	n := ready(title)
	n.Conversation, n.ConversationRun = conv, m20Turn
	t, err := e.b.AddTicket(e.ctx, p.Name, n, board.OriginConversation, "assistant · "+m20Turn)
	if err != nil {
		e.t.Fatal(err)
	}
	return t
}

// Make a ticket of this adopts a ticket the conversation's assistant proposed (11.8; plan M20 D19):
// while no Analysis run took it up, the form's text replaces its own and it is approved by adding it;
// later only its adoption is recorded and its text, status, and stage stay; an ended or adopted one,
// another conversation's, and an agent's are refused. Its origin stays, so Review cannot be skipped.
func TestAdoptMakesAProposedTicketTheOperators(t *testing.T) {
	e := newEnv(t)
	p := e.running("shop", "SF")
	for _, id := range []string{m20Conv, m20Other} {
		if _, err := e.db.Server.Exec(e.ctx, `INSERT INTO conversations (id, project, purpose, cli) VALUES ($1, $2, 'operator', 'agy')`, id, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	tk := e.proposed(p, "Lock after five", m20Conv)
	if tk.Analysis.Status != board.StatusProposed || tk.AdoptedAt != nil || tk.ConversationRun == nil || *tk.ConversationRun != m20Turn {
		t.Fatalf("proposed: %+v", tk)
	}
	form := board.NewTicket{Title: "Lock an account after five failed sign ins", Kind: "fix", Why: "The operator wants it.", What: "The lockout.", Goal: "A sixth try is refused."}
	before := e.changes(p)
	got, err := e.b.Adopt(e.ctx, p, tk.Key, m20Conv, form, board.Operator)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != form.Title || got.Kind != "fix" || got.Goal != form.Goal || !got.Analysis.Approved || got.AdoptedAt == nil ||
		got.Origin != board.OriginConversation || !board.MustReview(got.Origin) || got.Analysis.Status != board.StatusProposed || len(got.Checks) != 1 {
		t.Fatalf("adopted while proposed: %+v", got)
	}
	if e.changes(p) != before+1 {
		t.Fatal("the adoption has no row in the history")
	}
	if _, err := e.b.Adopt(e.ctx, p, tk.Key, m20Conv, form, board.Operator); kindOf(err) != board.Refused {
		t.Fatalf("adopted twice: %v", err)
	}
	// Approved by adding it: Analysis makes it ready or skips it, and cannot discard it.
	e.db.Server.Exec(e.ctx, `UPDATE tickets SET stage = 'analysis' WHERE id = $1`, tk.ID)
	if _, err := e.b.Decide(e.ctx, p, tk.Key, board.Decision{Choice: "discard", Reason: "no"}, "lead · r"); kindOf(err) != board.Refused {
		t.Fatalf("an adopted ticket discarded: %v", err)
	}

	// Taken up by Analysis: only the adoption is recorded.
	later := e.proposed(p, "Later", m20Conv)
	e.db.Server.Exec(e.ctx, `UPDATE tickets SET stage = 'analysis', analysis = analysis || '{"status": "ready"}' WHERE id = $1`, later.ID)
	got, err = e.b.Adopt(e.ctx, p, later.Key, m20Conv, form, board.Operator)
	if err != nil || got.Title != "Later" || got.Analysis.Status != board.StatusReady || got.Analysis.Approved || got.Stage != "analysis" || got.AdoptedAt == nil {
		t.Fatalf("adopted once analysed: %+v %v", got, err)
	}

	ended := e.proposed(p, "Ended", m20Conv)
	if _, err := e.db.Server.Exec(e.ctx, `UPDATE tickets SET stage = 'closed', closed_reason = 'discarded' WHERE id = $1`, ended.ID); err != nil {
		t.Fatal(err)
	}
	other := e.proposed(p, "Other", m20Other)
	agents := e.add(p, ready("An agent's"), board.OriginAgent)
	for what, key := range map[string]string{"ended": ended.Key, "another conversation's": other.Key, "an agent's": agents.Key} {
		if _, err := e.b.Adopt(e.ctx, p, key, m20Conv, form, board.Operator); kindOf(err) != board.Refused {
			t.Errorf("%s: %v", what, err)
		}
	}
	if _, err := e.b.Adopt(e.ctx, p, other.Key, m20Conv, board.NewTicket{Title: ""}, board.Operator); kindOf(err) != board.Invalid {
		t.Fatalf("a form with no title: %v", err)
	}

	// A ticket the operator confirmed in the app is theirs from the start.
	n := ready("Made of a turn")
	n.Conversation, n.Approved = m20Conv, true
	made, err := e.b.AddTicket(e.ctx, p.Name, n, board.OriginConversation, board.Operator)
	if err != nil || made.AdoptedAt == nil || made.ConversationRun != nil || made.Analysis.Status != board.StatusWaiting || !made.Analysis.Approved {
		t.Fatalf("made in the app: %+v %v", made, err)
	}
}

// The operator's comment on marked text keeps the quote and where it was marked; a comment sent on
// from the chat has its own author and reaches the working run as the operator's do (11.6, 11.9; plan
// M20 D11, D27).
func TestACommentKeepsItsQuoteAndAChatsCommentIsDelivered(t *testing.T) {
	e := newEnv(t)
	p := e.running("shop", "SF")
	tk := e.ticket(p, "SF-1")
	src := &board.QuoteSource{Part: "log", Label: "SF-1.1, turn 2", Run: "00000000-0000-4000-8000-000000000001"}
	c, err := e.b.CommentWith(e.ctx, p, tk.Key, board.NewComment{Body: "Do not retry this.", Author: board.Operator, Quote: "retrying in 5s", QuoteSource: src}, board.Operator)
	if err != nil {
		t.Fatal(err)
	}
	if c.Quote == nil || *c.Quote != "retrying in 5s" || c.QuoteSource == nil || *c.QuoteSource != *src || c.Body != "Do not retry this." {
		t.Fatalf("the quoted comment: %+v", c)
	}
	for what, n := range map[string]struct {
		ref string
		c   board.NewComment
	}{
		"a quote with no words":   {tk.Key, board.NewComment{Author: board.Operator, Quote: "q"}},
		"a quote on the plan":     {"", board.NewComment{Body: "b", Author: board.Operator, Quote: "q"}},
		"a quote from the chat":   {tk.Key, board.NewComment{Body: "b", Author: board.ChatAuthor, Quote: "q"}},
		"a quote of a token":      {tk.Key, board.NewComment{Body: "b", Author: board.TokenAuthor("laptop"), Quote: "q"}},
		"a part not in the set":   {tk.Key, board.NewComment{Body: "b", Author: board.Operator, Quote: "q", QuoteSource: &board.QuoteSource{Part: "elsewhere"}}},
		"a label of two lines":    {tk.Key, board.NewComment{Body: "b", Author: board.Operator, Quote: "q", QuoteSource: &board.QuoteSource{Part: "log", Label: "a\nb"}}},
		"a label too long":        {tk.Key, board.NewComment{Body: "b", Author: board.Operator, Quote: "q", QuoteSource: &board.QuoteSource{Part: "log", Label: strings.Repeat("x", 81)}}},
		"a run that is no id":     {tk.Key, board.NewComment{Body: "b", Author: board.Operator, Quote: "q", QuoteSource: &board.QuoteSource{Part: "log", Run: "x"}}},
		"a source with no quote":  {tk.Key, board.NewComment{Body: "b", Author: board.Operator, QuoteSource: src}},
		"an author of no comment": {tk.Key, board.NewComment{Body: "b", Author: "builder"}},
		"a quote too long":        {tk.Key, board.NewComment{Body: "b", Author: board.Operator, Quote: strings.Repeat("q", board.MaxQuote+1)}},
	} {
		if _, err := e.b.CommentWith(e.ctx, p, n.ref, n.c, board.Operator); kindOf(err) != board.Invalid {
			t.Errorf("%s: %v", what, err)
		}
	}
	chat, err := e.b.CommentBy(e.ctx, p, tk.Key, "The assistant says the retry loops.", board.ChatAuthor, board.Operator)
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := e.b.Waiting(e.ctx, p.ID, tk.ID)
	if err != nil || len(waiting) != 2 || waiting[1].ID != chat.ID || waiting[1].Author != board.ChatAuthor {
		t.Fatalf("waiting: %+v %v", waiting, err)
	}
	open, err := e.b.Unresolved(e.ctx, tk)
	if err != nil || len(open) != 2 || open[0].Quote == nil {
		t.Fatalf("unresolved: %+v %v", open, err)
	}
	e.db.Server.Exec(e.ctx, `UPDATE tickets SET stage = 'done' WHERE id = $1`, tk.ID)
	if _, err := e.b.CommentBy(e.ctx, p, tk.Key, "Too late.", board.ChatAuthor, board.Operator); kindOf(err) != board.Refused {
		t.Fatalf("a Done ticket's comment from the chat: %v", err)
	}
}

// What the operator keeps with Remember this from an assistant's message is theirs to keep: the
// index's cap, which holds an agent's entries back, does not refuse it (9.3, 11.9).
func TestAnEntryFromTheChatIsKeptPastTheIndexCap(t *testing.T) {
	e := newEnv(t)
	p := e.running("shop", "SF")
	if _, err := e.b.Remember(e.ctx, p, board.NewEntry{Kind: "lesson", Title: "A first lesson that makes the index long"}, board.MemorySource{}, "builder"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Server.Exec(e.ctx, `INSERT INTO settings (project, doc, updated_by) VALUES ($1, '{"knowledge": {"memory": {"index_max_chars": 20}}}', 'test')
		ON CONFLICT ((coalesce(project::text, 'machine'))) DO UPDATE SET doc = settings.doc || jsonb_build_object('knowledge',
		coalesce(settings.doc->'knowledge', '{}') || '{"memory": {"index_max_chars": 20}}')`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.b.Remember(e.ctx, p, board.NewEntry{Kind: "lesson", Title: "An agent's"}, board.MemorySource{}, "builder"); kindOf(err) != board.Refused {
		t.Fatalf("an agent's past the cap: %v", err)
	}
	got, err := e.b.Remember(e.ctx, p, board.NewEntry{Kind: "fact", Title: "Retries loop when the mailer is down"}, board.MemorySource{}, board.ChatAuthor)
	if err != nil || got.Author != board.ChatAuthor {
		t.Fatalf("from the chat past the cap: %+v %v", got, err)
	}
}

// The screens name a chat-authored comment and memory entry by ChatAuthor: the page's copy of it is the
// same string, or the memory screen and the records would call it "the assistant (from the chat)".
func TestThePageKnowsTheChatsAuthor(t *testing.T) {
	src, err := os.ReadFile("../../web/src/lib/api.ts")
	if err != nil {
		t.Fatal(err)
	}
	if want := "export const chatAuthor = '" + board.ChatAuthor + "'"; !strings.Contains(string(src), want) {
		t.Fatalf("web/src/lib/api.ts has no %q", want)
	}
}

// A project can be protected against Archive and Delete; Crew's own project is always protected and
// cannot be switched off (7.13, 16.2).
func TestAProtectedProjectRefusesArchiveAndDeleteAndCrewsProjectIsAlwaysProtected(t *testing.T) {
	e := newEnv(t)
	p := e.running("shop", "SF")
	if p.Protected {
		t.Fatal("a new project is protected by default")
	}
	p, err := e.b.SetProtected(e.ctx, p.Name, true, board.Operator)
	if err != nil || !p.Protected {
		t.Fatalf("SetProtected(true): %+v %v", p, err)
	}
	// Repeating SetProtected(true) is a no-op.
	if p2, err := e.b.SetProtected(e.ctx, p.ID, true, board.Operator); err != nil || !p2.Protected {
		t.Fatalf("SetProtected(true) again: %+v %v", p2, err)
	}
	if _, err := e.b.SetState(e.ctx, p.Name, "archive", "", board.Operator); kindOf(err) != board.Refused || !strings.Contains(err.Error(), "is protected") {
		t.Fatalf("archiving a protected project: %v", err)
	}
	if err := e.b.MayDelete(e.ctx, p, p.Name); kindOf(err) != board.Refused || !strings.Contains(err.Error(), "is protected") {
		t.Fatalf("MayDelete on a protected project: %v", err)
	}
	// A missing project returns NotFound, and the fixture project refuses SetProtected.
	if _, err := e.b.SetProtected(e.ctx, "no-such-project", true, board.Operator); kindOf(err) != board.NotFound {
		t.Fatalf("SetProtected on missing project: %v", err)
	}
	if _, err := e.b.SetProtected(e.ctx, "f1c50000-0000-4000-8000-000000000001", true, board.Operator); kindOf(err) != board.Refused {
		t.Fatalf("SetProtected on fixture: %v", err)
	}
	// Switching Protected off allows archiving and deleting again.
	if p, err = e.b.SetProtected(e.ctx, p.Name, false, board.Operator); err != nil || p.Protected {
		t.Fatalf("SetProtected(false): %+v %v", p, err)
	}
	if p, err = e.b.SetState(e.ctx, p.Name, "archive", "", board.Operator); err != nil || p.State != "archived" {
		t.Fatalf("archiving after unprotecting: %+v %v", p, err)
	}
	// Crew's project cannot be unprotected or deleted.
	if _, err := e.db.Server.Exec(e.ctx, `INSERT INTO settings (project, doc, updated_by) VALUES (NULL, '{"improvements": {"crew_project": "shop"}}', 'test')
		ON CONFLICT ((coalesce(project::text, 'machine'))) DO UPDATE SET doc = excluded.doc`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.b.SetProtected(e.ctx, p.Name, false, board.Operator); kindOf(err) != board.Refused || !strings.Contains(err.Error(), "always protected") {
		t.Fatalf("turning Protected off on Crew's project: %v", err)
	}
	if err := e.b.MayDelete(e.ctx, p, p.Name); kindOf(err) != board.Refused || !strings.Contains(err.Error(), "Crew's project") {
		t.Fatalf("MayDelete on Crew's project: %v", err)
	}
}
