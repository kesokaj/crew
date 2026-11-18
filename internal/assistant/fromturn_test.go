package assistant

import (
	"errors"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/planning"
)

// answered is a conversation of the shop's whose assistant answered once, with the turn and its run.
func (k *kit) answered(project, text string) (View, int, string) {
	k.t.Helper()
	k.db.Server.Exec(k.ctx, `UPDATE projects SET state = 'running'`)
	v, err := k.s.Start(k.ctx, Start{Project: project, Text: text})
	if err != nil {
		k.t.Fatal(err)
	}
	k.s.Reconcile(k.ctx)
	v = k.until(v.ID, idle)
	e := v.Entries[len(v.Entries)-1]
	if e.Kind != "agent" || e.Turn == 0 || e.Run == "" {
		k.t.Fatalf("no answer: %+v", v.Entries)
	}
	return v, e.Turn, e.Run
}

// E2E 4: the conversation's agent proposed a ticket; Make a ticket of this under its answer adopts it,
// and with no proposal adds the operator's own; Tell the agent and Remember this send the answer on as
// the assistant's (11.8; plan M20 D3, D11, D19, D26).
func TestAConversationsButtonsSendItsAnswerOn(t *testing.T) {
	k := newKit(t)
	v, turn, run := k.answered("shop", "How should accounts lock?")
	n := board.NewTicket{Title: "Lock accounts", Why: "w", What: "w", Goal: "g", Conversation: v.ID, ConversationRun: run}
	proposed, err := k.b.AddTicket(k.ctx, "shop", n, board.OriginConversation, "assistant · "+run)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := k.b.AddTicket(k.ctx, "shop", board.NewTicket{Title: "An agent's", Why: "w", What: "w", Goal: "g"}, board.OriginAgent, "builder · r")
	if got, _ := k.s.Get(k.ctx, v.ID); len(got.Entries[1].Tickets) != 1 || got.Entries[1].Tickets[0] != proposed.Key || got.CameOf.Tickets[0].Adopted {
		t.Fatalf("the turn's ticket: %+v %+v", got.Entries, got.CameOf)
	}
	form := TicketForm{Turn: turn, Adopt: proposed.Key, Title: "Lock after five tries", Kind: "fix", Why: "Asked for.", What: "The lock.", Goal: "A sixth try fails."}
	if _, err := k.s.MakeTicket(k.ctx, v.ID, TicketForm{Turn: turn + 5, Adopt: proposed.Key, Title: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a turn that is no answer: %v", err)
	}
	bad := form
	bad.Adopt = other.Key
	if _, err := k.s.MakeTicket(k.ctx, v.ID, bad); !errors.Is(err, ErrRefused) {
		t.Fatalf("a ticket the turn did not propose: %v", err)
	}
	adopted, err := k.s.MakeTicket(k.ctx, v.ID, form)
	if err != nil || adopted.Key != proposed.Key || adopted.Title != form.Title || adopted.AdoptedAt == nil || !adopted.Analysis.Approved {
		t.Fatalf("adopted: %+v %v", adopted, err)
	}
	made, err := k.s.MakeTicket(k.ctx, v.ID, TicketForm{Turn: turn, Title: "Show the lock", Why: "w", What: "w", Goal: "g"})
	if err != nil || made.Origin != board.OriginConversation || made.AdoptedAt == nil || !made.Analysis.Approved || made.Seen == nil ||
		!strings.Contains(*made.Seen, `the conversation "How should accounts lock?", confirmed by the operator`) || !board.MustReview(made.Origin) {
		t.Fatalf("made: %+v %v", made, err)
	}
	if got, _ := k.s.Get(k.ctx, v.ID); len(got.CameOf.Tickets) != 2 || !got.CameOf.Tickets[0].Adopted || !got.CameOf.Tickets[1].Adopted {
		t.Fatalf("what came of it: %+v", got.CameOf)
	}

	if _, err := k.s.Tell(k.ctx, v.ID, TellForm{Turn: turn, Text: "Lock after five."}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a conversation's Tell with no ticket: %v", err)
	}
	r, err := k.s.Tell(k.ctx, v.ID, TellForm{Turn: turn, Ticket: other.Key, Text: "Lock after five."})
	if err != nil || r.Author != board.ChatAuthor || r.Body != "Lock after five." || r.Ticket == nil || *r.Ticket != other.Key {
		t.Fatalf("told: %+v %v", r, err)
	}
	e, err := k.s.Remember(k.ctx, v.ID, RememberForm{Turn: turn, NewEntry: board.NewEntry{Kind: "fact", Title: "Accounts lock after five tries"}})
	if err != nil || e.Author != board.ChatAuthor || e.SourceRun == nil || *e.SourceRun != run {
		t.Fatalf("remembered: %+v %v", e, err)
	}
	if _, err := k.s.Plan(k.ctx, v.ID); !errors.Is(err, ErrRefused) {
		t.Fatalf("a plan from a conversation about a project: %v", err)
	}
}

// E2E 6's comment half, and D28: a question in a ticket's chat is a signal and a line of the ticket's
// history, with nothing of what was asked; Make a ticket of this links the new ticket as created from
// the chat's, also once that is Done, where Tell the agent is refused (11.9; plan M20 D10).
func TestAChatsQuestionIsRecordedAndItsButtonsStayOnItsTicket(t *testing.T) {
	k := newKit(t)
	k.db.Server.Exec(k.ctx, `UPDATE projects SET state = 'running'`)
	tk, err := k.b.AddTicket(k.ctx, "shop", board.NewTicket{Title: "One", Why: "w", What: "w", Goal: "g"}, board.OriginOperator, "op")
	if err != nil {
		t.Fatal(err)
	}
	chat, err := k.s.ChatOf(k.ctx, k.p, tk.Key, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.s.Send(k.ctx, chat.ID, Message{Text: "Why did the SECRET-WORD check fail?"}); err != nil {
		t.Fatal(err)
	}
	if _, err := k.s.Send(k.ctx, chat.ID, Message{Text: "And this?", Quote: "exit 1", Source: &Source{Part: "log", Label: "SF-1.1"}}); err != nil {
		t.Fatal(err)
	}
	var signals, history, leaked int
	k.db.Server.QueryRow(k.ctx, `SELECT count(*) FILTER (WHERE kind = 'asked_about' AND ticket = $1), count(*) FILTER (WHERE detail::text LIKE '%SECRET-WORD%')
		FROM signals`, tk.ID).Scan(&signals, &leaked)
	k.db.Server.QueryRow(k.ctx, `SELECT count(*) FROM changes WHERE tbl = 'tickets' AND row_key = $1 AND after->>'chat' = 'asked'`, tk.Key).Scan(&history)
	if signals != 2 || history != 2 || leaked != 0 {
		t.Fatalf("signals %d, history %d, the question in a signal %d", signals, history, leaked)
	}
	var marked string
	k.db.Server.QueryRow(k.ctx, `SELECT detail->>'text' FROM signals WHERE kind = 'asked_about' AND (detail->>'marked')::boolean`).Scan(&marked)
	if marked != "You commented on marked text of SF-1 in its chat" {
		t.Fatalf("the marked one: %q", marked)
	}
	k.s.Reconcile(k.ctx)
	v := k.until(chat.ID, idle)
	turn := v.Entries[len(v.Entries)-1].Turn
	if _, err := k.s.Tell(k.ctx, chat.ID, TellForm{Turn: turn, Ticket: "SF-9", Text: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a chat's Tell to another ticket: %v", err)
	}
	if r, err := k.s.Tell(k.ctx, chat.ID, TellForm{Turn: turn, Text: "Look at the log."}); err != nil || *r.Ticket != tk.Key || r.Author != board.ChatAuthor {
		t.Fatalf("a chat's Tell: %+v %v", r, err)
	}
	k.db.Server.Exec(k.ctx, `UPDATE tickets SET stage = 'done' WHERE id = $1`, tk.ID)
	if _, err := k.s.Tell(k.ctx, chat.ID, TellForm{Turn: turn, Text: "Too late."}); err == nil {
		t.Fatal("Tell the agent on a Done ticket")
	}
	made, err := k.s.MakeTicket(k.ctx, chat.ID, TicketForm{Turn: turn, Title: "Change it after all", Why: "w", What: "w", Goal: "g"})
	if err != nil {
		t.Fatal(err)
	}
	linked := false
	for _, l := range made.Links {
		linked = linked || (l.Kind == "created_from" && l.To == tk.Key)
	}
	if made.Origin != board.OriginChat || !linked || !strings.Contains(*made.Seen, "the chat of SF-1") || !board.MustReview(made.Origin) {
		t.Fatalf("made in the chat: %+v", made)
	}
	if _, err := k.s.MakeTicket(k.ctx, chat.ID, TicketForm{Turn: turn, Adopt: made.Key, Title: "x"}); !errors.Is(err, ErrRefused) {
		t.Fatalf("a chat adopts: %v", err)
	}
}

// E2E 5: Start a plan from this, about no project, makes a draft whose source is the conversation and
// whose first message gives the operator's words as theirs and the assistant's answers as data (D9).
func TestAPlanStartsFromAConversationAboutNoProject(t *testing.T) {
	k := newKit(t)
	k.s.Drafts = &planning.Drafts{Pool: k.db.Server}
	v, turn, _ := k.answered("", "A shop for old maps?")
	if _, err := k.s.MakeTicket(k.ctx, v.ID, TicketForm{Title: "x"}); !errors.Is(err, ErrRefused) {
		t.Fatalf("a ticket about no project without naming one: %v", err)
	}
	// Naming a project by name or key moves the normal conversation's finding into that project's
	// backlog; a hidden project is refused.
	moved, err := k.s.MakeTicket(k.ctx, v.ID, TicketForm{Project: "SF", Turn: turn, Title: "Add old maps", Why: "w", What: "w", Goal: "g"})
	if err != nil || moved.Origin != board.OriginConversation || !moved.Analysis.Approved || !board.MustReview(moved.Origin) {
		t.Fatalf("moved to project backlog: %+v %v", moved, err)
	}
	if _, err := k.db.Server.Exec(k.ctx, `INSERT INTO projects (id, name, key, repo_source, state, hidden) VALUES ('00000000-0000-4000-8000-000000009999', 'crew-gate', 'CG', 'local', 'running', true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := k.s.MakeTicket(k.ctx, v.ID, TicketForm{Project: "crew-gate", Turn: turn, Title: "Hidden", Why: "w", What: "w", Goal: "g"}); !errors.Is(err, ErrRefused) {
		t.Fatalf("hidden project target: %v", err)
	}
	draft, err := k.s.Plan(k.ctx, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	var source, text string
	k.db.Server.QueryRow(k.ctx, `SELECT d.source_conversation::text, m.text FROM plan_drafts d JOIN draft_messages m ON m.draft = d.id WHERE d.id = $1`, draft).Scan(&source, &text)
	if source != v.ID || !strings.Contains(text, "I wrote:\nA shop for old maps?") || !strings.Contains(text, "BEGIN The assistant's answer, turn") {
		t.Fatalf("the draft: %q %q", source, text)
	}
	if got, _ := k.s.Get(k.ctx, v.ID); got.CameOf.Draft != draft || len(got.CameOf.Tickets) != 1 || got.CameOf.Tickets[0].Key != moved.Key || got.CameOf.Tickets[0].Project != "SF" {
		t.Fatalf("what came of it: %+v", got.CameOf)
	}
}
