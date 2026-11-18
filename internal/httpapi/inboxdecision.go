package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/kesokaj/crew/internal/inbox"
	"github.com/kesokaj/crew/internal/notify"
	"github.com/kesokaj/crew/internal/settings"
)

// A link in a mail chooses; Confirm in Crew decides (18.3). The item is named in the query and the
// body, not the path: its id holds slashes (plan D10).
func (s *Server) inboxDecisionRoutes(api *routes) {
	api.HandleFunc("GET /api/inbox/item", s.inboxItem)
	api.HandleFunc("POST /api/inbox/decision", s.inboxDecision)
}

// InboxItem is an inbox item with when its open episode was mailed.
type InboxItem struct {
	inbox.Item
	Notified []notify.Sent `json:"notified"`
}

// Decided is one press of an alternative through the inbox.
type Decided struct {
	Label string    `json:"label"`
	By    string    `json:"by"`
	At    time.Time `json:"at"`
}

// listInbox is the inbox as it is now.
func (s *Server) listInbox(w http.ResponseWriter, r *http.Request) ([]inbox.Item, bool) {
	pool := s.pool(w)
	if pool == nil {
		return nil, false
	}
	doc, err := settings.Effective(r.Context(), pool, s.Machine)
	if err != nil {
		s.boardError(w, "reading the settings", err)
		return nil, false
	}
	items, err := inbox.List(r.Context(), pool, doc)
	if err != nil {
		s.boardError(w, "reading the inbox", err)
		return nil, false
	}
	return items, true
}

// withNotified adds to each item when its open episode was mailed.
func (s *Server) withNotified(w http.ResponseWriter, r *http.Request, items []inbox.Item) ([]InboxItem, bool) {
	sent, err := notify.Notified(r.Context(), s.Pool.Load())
	if err != nil {
		s.boardError(w, "reading what was mailed", err)
		return nil, false
	}
	out := make([]InboxItem, 0, len(items))
	for _, it := range items {
		n := sent[it.ID]
		if n == nil {
			n = []notify.Sent{}
		}
		out = append(out, InboxItem{Item: it, Notified: n})
	}
	return out, true
}

// inboxItem is the item a mail's link names: the item when it waits, and the presses recorded for
// its id, so an old link shows what was decided and when.
func (s *Server) inboxItem(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "name the item: ?id=")
		return
	}
	items, ok := s.listInbox(w, r)
	if !ok {
		return
	}
	var found []inbox.Item
	for _, it := range items {
		if it.ID == id {
			found = append(found, it)
		}
	}
	withSent, ok := s.withNotified(w, r, found)
	if !ok {
		return
	}
	rows, err := s.Pool.Load().Query(r.Context(), `SELECT label, by_email, at FROM inbox_decisions WHERE item = $1 ORDER BY id DESC LIMIT 10`, id)
	if err != nil {
		s.boardError(w, "reading the decisions", err)
		return
	}
	defer rows.Close()
	decisions := []Decided{}
	for rows.Next() {
		var d Decided
		if err := rows.Scan(&d.Label, &d.By, &d.At); err != nil {
			s.boardError(w, "reading the decisions", err)
			return
		}
		decisions = append(decisions, d)
	}
	out := map[string]any{"item": nil, "decisions": decisions, "notified": []notify.Sent{}}
	if len(withSent) > 0 {
		out["item"], out["notified"] = withSent[0], withSent[0].Notified
	}
	writeJSON(w, http.StatusOK, out)
}

// inboxDecision is Confirm (D11): the item is read again, the alternative found by its label, and its
// action served through the API's own routes with this request's session, so the action's own checks,
// the fresh sign-in among them, hold as if the page had sent it. A press the action took is recorded.
func (s *Server) inboxDecision(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Item   string `json:"item"`
		Choose string `json:"choose"`
	}
	if !decode(w, r, &in) {
		return
	}
	items, ok := s.listInbox(w, r)
	if !ok {
		return
	}
	var alt *inbox.Alternative
	waits := false
	for _, it := range items {
		if it.ID != in.Item {
			continue
		}
		waits = true
		for i := range it.Alternatives {
			if a := it.Alternatives[i]; a.Label == in.Choose && a.Action != "" {
				alt = &it.Alternatives[i]
			}
		}
	}
	if !waits {
		writeError(w, http.StatusConflict, "ABORTED", "This no longer waits for you")
		return
	}
	if alt == nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", strconv.Quote(in.Choose)+" is not something this item can do")
		return
	}
	body := []byte("{}")
	if alt.Body != nil {
		body, _ = json.Marshal(alt.Body)
	}
	inner := r.Clone(r.Context())
	inner.Method = http.MethodPost
	inner.URL = &url.URL{Path: alt.Action}
	inner.RequestURI = ""
	inner.Body = io.NopCloser(bytes.NewReader(body))
	inner.ContentLength = int64(len(body))
	inner.Header.Set("Content-Type", "application/json")
	rec := &recorder{header: http.Header{}, code: http.StatusOK}
	s.api.ServeHTTP(rec, inner)
	if rec.code >= 200 && rec.code < 300 {
		if _, err := s.Pool.Load().Exec(r.Context(), `INSERT INTO inbox_decisions (item, label, by_email) VALUES ($1, $2, $3)`,
			in.Item, alt.Label, r.Header.Get(HeaderEmail)); err != nil {
			s.Log.Error("recording a decision of the inbox", "item", in.Item, "error", err)
		}
	}
	for _, k := range []string{"Content-Type", "Cache-Control"} {
		if v := rec.header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	w.WriteHeader(rec.code)
	w.Write(rec.body.Bytes())
}

// recorder keeps the answer of an action served inside a decision.
type recorder struct {
	header http.Header
	code   int
	body   bytes.Buffer
	wrote  bool
}

func (r *recorder) Header() http.Header { return r.header }

func (r *recorder) WriteHeader(code int) {
	if !r.wrote {
		r.code, r.wrote = code, true
	}
}

func (r *recorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.body.Write(b)
}
