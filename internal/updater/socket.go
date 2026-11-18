package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// The socket (16.11): /run/crew/updater.sock, which only root and the server's container can open.
// It takes install a version, say where it is, stop, and load a build, as HTTP with JSON.
//
//	POST /install  Ask        -> 202 Request, 409 busy, 422 refused
//	GET  /status              -> Status
//	POST /stop                -> 200, 409 when nothing can be stopped
//	POST /load     LoadAsk    -> 200 Loaded, 409 busy, 422 refused (since 0.51: a build Crew made)
//	POST /gate     Ask        -> 202 Request, 409 busy, 422 refused (since 0.52: the gate's steps)
//	POST /gate/live LiveAsk   -> 200 Gate, 409 no pending gate, 422 refused (since 0.52: the live result)

type peerKey struct{}

// Serve listens on the socket at path, with mode 0660 and the group gid, until ctx ends.
func (u *Updater) Serve(ctx context.Context, path string, gid int) error {
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chown(path, 0, gid); err != nil && os.Getuid() == 0 {
		ln.Close()
		return err
	}
	if err := os.Chmod(path, 0o660); err != nil {
		ln.Close()
		return err
	}
	srv := &http.Server{
		Handler:           u.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return context.WithValue(ctx, peerKey{}, peerUID(c))
		},
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	err = srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// peerUID is the user id of the process at the other end of a Unix socket, from the kernel, or -1.
func peerUID(c net.Conn) int {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return -1
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return -1
	}
	uid := -1
	_ = raw.Control(func(fd uintptr) {
		if cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED); err == nil {
			uid = int(cred.Uid)
		}
	})
	return uid
}

// Handler is the socket's HTTP handler. A request's caller is root when the kernel says its user id
// is 0; a test sets it with WithPeer.
func (u *Updater) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /install", func(w http.ResponseWriter, r *http.Request) {
		var a Ask
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&a); err != nil {
			writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "the request is not JSON: " + err.Error()})
			return
		}
		// Root only when the kernel said so: a caller whose id is unknown is not root.
		uid, known := r.Context().Value(peerKey{}).(int)
		req, err := u.Start(r.Context(), a, known && uid == 0)
		var refused *Refusal
		switch {
		case errors.Is(err, ErrBusy):
			writeJSONResponse(w, http.StatusConflict, map[string]string{"error": err.Error()})
		case errors.As(err, &refused):
			writeJSONResponse(w, http.StatusUnprocessableEntity, map[string]any{"error": refused.Reason, "request": req})
		case err != nil:
			writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		default:
			writeJSONResponse(w, http.StatusAccepted, req)
		}
	})
	mux.HandleFunc("POST /gate", func(w http.ResponseWriter, r *http.Request) {
		var a Ask
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&a); err != nil {
			writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "the request is not JSON: " + err.Error()})
			return
		}
		a.Action = ActionGate
		req, err := u.Start(r.Context(), a, false)
		var refused *Refusal
		switch {
		case errors.Is(err, ErrBusy):
			writeJSONResponse(w, http.StatusConflict, map[string]string{"error": err.Error()})
		case errors.As(err, &refused):
			writeJSONResponse(w, http.StatusUnprocessableEntity, map[string]any{"error": refused.Reason, "request": req})
		case err != nil:
			writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		default:
			writeJSONResponse(w, http.StatusAccepted, req)
		}
	})
	mux.HandleFunc("POST /gate/live", func(w http.ResponseWriter, r *http.Request) {
		var a LiveAsk
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&a); err != nil {
			writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "the request is not JSON: " + err.Error()})
			return
		}
		g, err := u.Live(a)
		var refused *Refusal
		switch {
		case errors.Is(err, ErrNoPendingGate):
			writeJSONResponse(w, http.StatusConflict, map[string]string{"error": err.Error()})
		case errors.As(err, &refused):
			writeJSONResponse(w, http.StatusUnprocessableEntity, map[string]string{"error": refused.Reason})
		case err != nil:
			writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		default:
			writeJSONResponse(w, http.StatusOK, g)
		}
	})
	mux.HandleFunc("POST /load", func(w http.ResponseWriter, r *http.Request) {
		var a LoadAsk
		// The facts list every file of the commit: Crew's are some thousands of lines.
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&a); err != nil {
			writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "the request is not JSON: " + err.Error()})
			return
		}
		got, err := u.Load(r.Context(), a)
		var refused *Refusal
		switch {
		case errors.Is(err, ErrBusy):
			writeJSONResponse(w, http.StatusConflict, map[string]string{"error": "the updater is busy: a request or another load runs"})
		case errors.As(err, &refused):
			writeJSONResponse(w, http.StatusUnprocessableEntity, map[string]string{"error": refused.Reason})
		case err != nil:
			writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		default:
			writeJSONResponse(w, http.StatusOK, got)
		}
	})
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		st, err := u.Status(20)
		if err != nil {
			writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSONResponse(w, http.StatusOK, st)
	})
	mux.HandleFunc("POST /stop", func(w http.ResponseWriter, r *http.Request) {
		if err := u.Stop(); err != nil {
			writeJSONResponse(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSONResponse(w, http.StatusOK, map[string]string{"stopped": "yes"})
	})
	return mux
}

// WithPeer is a context whose caller has the user id uid, for a test of the handler.
func WithPeer(ctx context.Context, uid int) context.Context {
	return context.WithValue(ctx, peerKey{}, uid)
}

func writeJSONResponse(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// asRoot says the updater runs as root, as it does in its container; a test sets it.
var asRoot = func() bool { return os.Getuid() == 0 }

// Exclusive takes the updater's lock in the folder dir, beside its socket, for as long as the process
// holds the file: a second crew-updater, also one started by docker exec in the updater's container,
// finds it taken and must stop before it reads a request or touches the socket (16.11, since 0.53). The
// kernel lets go of the lock when the process ends, so a stopped updater leaves none behind. The file is
// never a link, and an updater that runs as root takes only a lock that is root's own in a folder only
// root writes, so the user crew can neither hold it nor put another in its place (security review of
// 0.53).
func Exclusive(dir string) (*os.File, error) {
	if asRoot() {
		var st unix.Stat_t
		if err := unix.Lstat(dir, &st); err != nil {
			return nil, err
		}
		if st.Uid != 0 || st.Mode&0o022 != 0 || st.Mode&unix.S_IFMT != unix.S_IFDIR {
			return nil, fmt.Errorf("the updater's lock folder %s is not root's alone", dir)
		}
	}
	f, err := os.OpenFile(filepath.Join(dir, "updater.lock"), os.O_RDWR|os.O_CREATE|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	if asRoot() {
		var st unix.Stat_t
		if err := unix.Fstat(int(f.Fd()), &st); err != nil || st.Uid != 0 || st.Mode&unix.S_IFMT != unix.S_IFREG {
			f.Close()
			return nil, fmt.Errorf("the updater's lock in %s is not root's own file", dir)
		}
		// Only root opens it: a lock another user could open, it could hold (round 2 of the security
		// review of 0.53).
		if st.Mode&0o077 != 0 {
			if err := f.Chmod(0o600); err != nil {
				f.Close()
				return nil, err
			}
		}
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("another crew-updater runs: it holds the lock in %s", dir)
		}
		return nil, err
	}
	return f, nil
}
