package versions

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/harness"
)

// maxAgy bounds agy's tarball (62 MiB at 1.4.5), and agyTime its download; a var so a test can lower it.
var maxAgy int64 = 512 << 20

const agyTime = 10 * time.Minute

var errAgyRedirect = errors.New("agy's tarball: the vendor's address answered with a redirect, which a build does not follow")

// fetchAgy downloads agy's tarball as the candidate's versions.lock pins it into dir, a folder of its
// own beside the build's, and returns the file's path (12.8, 16.2 step 4, since 0.57). The address is
// the vendor's alone (the adapter's AgyDownload); the request follows no redirect and names nothing of
// the installation (AGENTS.md, Clean 7); the answer is read up to maxAgy and hashed as it is written,
// and only a file of the pin's sha512 is kept. Anything else leaves nothing in dir and says why in
// Crew's words, with no value of the lock. The disk keeps harness.DiskMargin free, as a build's files do.
func fetchAgy(ctx context.Context, client *http.Client, lock map[string]string, dir string) (string, error) {
	url, want, err := cli.AgyDownload(lock)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	free, err := harness.FreeSpace(dir)
	if err != nil {
		return "", err
	}
	if free < uint64(maxAgy)+harness.DiskMargin {
		return "", fmt.Errorf("agy's tarball: the disk has %d bytes free, and the download needs up to %d and a margin of %d", free, maxAgy, int64(harness.DiskMargin))
	}
	c := http.Client{}
	if client != nil {
		c = *client
	}
	if c.Timeout == 0 {
		c.Timeout = agyTime
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return errAgyRedirect }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	res, err := c.Do(req)
	if errors.Is(err, errAgyRedirect) {
		return "", errAgyRedirect
	}
	if err != nil {
		return "", errors.New("agy's tarball: the vendor's address did not answer")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("agy's tarball: the vendor's address answered %d", res.StatusCode)
	}
	f, err := os.CreateTemp(dir, ".agy-*")
	if err != nil {
		return "", err
	}
	fail := func(err error) (string, error) {
		os.Remove(f.Name())
		return "", err
	}
	h := sha512.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(res.Body, maxAgy+1))
	cerr := f.Close()
	switch {
	case err != nil:
		return fail(errors.New("agy's tarball: the answer broke off"))
	case cerr != nil:
		return fail(cerr)
	case n > maxAgy:
		return fail(fmt.Errorf("agy's tarball: the answer is more than %d bytes", maxAgy))
	case hex.EncodeToString(h.Sum(nil)) != want:
		return fail(errors.New("agy's tarball is not the one versions.lock pins: its sha512 differs"))
	}
	path := filepath.Join(dir, cli.AgyFile)
	if err := os.Rename(f.Name(), path); err != nil {
		return fail(err)
	}
	return path, nil
}
