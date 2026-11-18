package gitendpoint

import (
	"context"
	"net/http"
	"net/http/cgi"
	"slices"
	"sync"
	"time"
)

// MaxWriteTime bounds how long the answer to a request may take once it has its slots (6.1, since
// 1.08): past it the answer's write fails, the CGI handler kills git, and the slots are free, so a
// client that stops reading holds none. A push's hook ends within about four minutes.
const MaxWriteTime = 10 * time.Minute

// work serves a request Auth allowed: it takes the slots of its service, every request's, an
// advertisement of refs too (since 1.08), bounds its body and its answer in size and in time, starts
// git, and for a push tells OnPush what it changed once git has ended and the slots are free.
func (e *Endpoint) work(w http.ResponseWriter, r *http.Request, acc Access, repo, service string) {
	var max int64
	if r.Method == http.MethodPost {
		max = e.maxPush()
		if service == "git-upload-pack" {
			max = 64 << 20
		}
		// A body that says it is too large is refused before git starts: one cut short in the middle
		// leaves git and the client waiting on each other.
		if r.ContentLength > max {
			http.Error(w, "the request is too large", http.StatusRequestEntityTooLarge)
			return
		}
	}
	// An advertisement of refs takes no body: one that says it has one would hold its slot while git
	// waits for the body (since 1.08).
	if r.Method == http.MethodGet && (r.ContentLength != 0 || len(r.TransferEncoding) > 0) {
		w.Header().Set("Connection", "close")
		http.Error(w, "a request for refs takes no body", http.StatusBadRequest)
		return
	}
	// A slot is taken before the body is read, into a file or by git, and held until git has ended
	// (since 1.08).
	release, ok := e.slot(r.Context(), service, repo)
	if !ok {
		if r.Context().Err() == nil && e.Log != nil {
			e.Log.Warn("git endpoint busy", "who", acc.Who, "repo", repo, "service", service)
		}
		// Closed after the answer, so the server does not wait for a body it will not read first.
		w.Header().Set("Connection", "close")
		http.Error(w, busyText(service), http.StatusServiceUnavailable)
		return
	}
	// What the request holds goes back once, in the reverse order it was taken: when git has ended for
	// a push that is heard of, else when the request ends.
	var once sync.Once
	ends := []func(){release}
	finish := func() {
		once.Do(func() {
			for i := len(ends) - 1; i >= 0; i-- {
				ends[i]()
			}
		})
	}
	defer finish()
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Now().Add(e.writeTime())); err != nil {
		http.Error(w, "the request could not be answered", http.StatusInternalServerError)
		return
	}
	if err := rc.SetReadDeadline(time.Now().Add(e.bodyTime())); err != nil {
		http.Error(w, "the request could not be read", http.StatusInternalServerError)
		return
	}
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, max)
		// A push's body, and one of unknown length, which git sends past its http.postBuffer, is read
		// whole into a file first: a CGI program takes no body of unknown length, and git http-backend
		// spins without end on a push's body that ends before its length (since 1.08).
		if r.ContentLength < 0 || service == "git-receive-pack" {
			done, ok := spool(w, r)
			if !ok {
				return
			}
			ends = append(ends, done)
		}
	}
	h := &cgi.Handler{Root: "/git", Env: e.env(acc)}
	h.Path, h.Args = e.backend()
	if e.Log != nil {
		e.Log.Debug("git endpoint", "who", acc.Who, "repo", repo, "service", service)
	}
	if service != "git-receive-pack" || r.Method != http.MethodPost || e.OnPush == nil {
		h.ServeHTTP(w, r)
		return
	}
	// The refs are read whatever becomes of the request's connection, a client that closes its side
	// once its body is sent ending the request's context, each read within a time of its own. A read
	// that fails records the push for no ref, never for every ref as new, and OnPush hears of main with
	// no new value when the request may push it, so a main that is not read is not taken (since 1.08).
	read := func(refs []string) (map[string]string, error) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), e.refReadTime())
		defer cancel()
		return e.refValues(ctx, repo, refs)
	}
	before, berr := read(acc.Push)
	h.ServeHTTP(w, r)
	finish()
	after, aerr := read(acc.Push)
	if berr != nil || aerr != nil {
		if e.Log != nil {
			e.Log.Error("the refs of a push could not be read: it is recorded for no ref", "who", acc.Who, "repo", repo,
				"before", errText(berr), "after", errText(aerr))
		}
		if slices.Contains(acc.Push, "refs/heads/main") {
			e.OnPush(acc, "refs/heads/main", "", "")
		}
		return
	}
	// A prefix in acc.Push names the refs under it, so the refs heard of are the ones read.
	refs := make([]string, 0, len(after))
	for ref := range after {
		refs = append(refs, ref)
	}
	slices.Sort(refs)
	for _, ref := range refs {
		if after[ref] == before[ref] || after[ref] == "" {
			continue
		}
		old := before[ref]
		if old == "" {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), e.refReadTime())
			old = e.mergeBase(ctx, repo, after[ref], "refs/heads/main")
			cancel()
		}
		e.OnPush(acc, ref, old, after[ref])
	}
}

func (e *Endpoint) writeTime() time.Duration {
	if e.WriteTime > 0 {
		return e.WriteTime
	}
	return MaxWriteTime
}

// refReadTime bounds each read of a push's refs; tests make it short.
func (e *Endpoint) refReadTime() time.Duration {
	if e.refTime > 0 {
		return e.refTime
	}
	return time.Minute
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
