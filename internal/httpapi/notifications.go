package httpapi

import (
	"errors"
	"net/http"

	"github.com/kesokaj/crew/internal/notify"
)

// The mail (design 18): Setup's mail account, the test message, and what was sent.
func (s *Server) notificationRoutes(api *routes) {
	api.HandleFunc("GET /api/setup/mail", s.mailAccount)
	api.HandleFunc("PUT /api/setup/mail", s.setMailAccount)
	api.HandleFunc("POST /api/notifications/test", s.testMail)
	api.HandleFunc("GET /api/notifications", s.notifications)
}

// notifier returns the notifier, or answers 503 while the database is not there yet.
func (s *Server) notifier(w http.ResponseWriter) *notify.Notifier {
	n := s.Notify.Load()
	if n == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the mail is not available yet: the database does not answer")
	}
	return n
}

func (s *Server) mailAccount(w http.ResponseWriter, r *http.Request) {
	n := s.notifier(w)
	if n == nil {
		return
	}
	v, err := n.Account(r.Context())
	if err != nil {
		s.Log.Error("reading the mail account", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "the mail account cannot be read")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// setMailAccount stores the account and its app password, a secret: a fresh route (20.3). The
// password is in no answer and no log line.
func (s *Server) setMailAccount(w http.ResponseWriter, r *http.Request) {
	n := s.notifier(w)
	if n == nil {
		return
	}
	var in struct {
		Account  string `json:"account"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	v, err := n.SetAccount(r.Context(), in.Account, in.Password, r.Header.Get(HeaderEmail))
	var ie *notify.InputError
	switch {
	case errors.As(err, &ie):
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", ie.Msg)
		return
	case err != nil:
		s.Log.Error("storing the mail account", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "the mail account cannot be stored")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) testMail(w http.ResponseWriter, r *http.Request) {
	n := s.notifier(w)
	if n == nil {
		return
	}
	row, err := n.SendTest(r.Context())
	var nm *notify.ErrNoMail
	switch {
	case errors.As(err, &nm):
		writeError(w, http.StatusConflict, "FAILED_PRECONDITION", "no test message can be sent: "+nm.Why)
		return
	case err != nil:
		s.Log.Error("sending a test message", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "the test message cannot be sent")
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (s *Server) notifications(w http.ResponseWriter, r *http.Request) {
	n := s.notifier(w)
	if n == nil {
		return
	}
	v, rows, err := n.Sent(r.Context())
	if err != nil {
		s.Log.Error("reading the notifications", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "the notifications cannot be read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mail": v, "sent": rows})
}
