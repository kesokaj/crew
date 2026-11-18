package gitendpoint

import (
	"errors"
	"io"
	"net/http"
	"os"
)

// spool reads r's body whole into a file, which becomes the body git reads, at its exact length. A
// body past its bound is refused as too large, and one that ends before its length or does not arrive
// within its time as not read (since 1.08); ok is false then, with the answer written. done removes
// the file.
func spool(w http.ResponseWriter, r *http.Request) (done func(), ok bool) {
	f, err := os.CreateTemp("", "crew-git-*")
	if err != nil {
		http.Error(w, "the request could not be read", http.StatusInternalServerError)
		return nil, false
	}
	done = func() {
		f.Close()
		os.Remove(f.Name())
	}
	n, err := io.Copy(f, r.Body)
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		done()
		http.Error(w, "the request is too large", http.StatusRequestEntityTooLarge)
		return nil, false
	case err != nil:
		done()
		http.Error(w, "the request's body did not arrive whole in time", http.StatusRequestTimeout)
		return nil, false
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		done()
		http.Error(w, "the request could not be read", http.StatusInternalServerError)
		return nil, false
	}
	r.Body, r.ContentLength, r.TransferEncoding = f, n, nil
	return done, true
}
