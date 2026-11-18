package gitstore_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/gitstore"
)

// An index file is read by the guards only up to the hook's bound, cli.MaxIndexBytes, by its size in
// the tree before it is read, also where a link on its way leads: past it, it is an error with a fixed
// text, never a file read whole up to the bound of a read (13.8, since 1.07).
func TestAnIndexFilePastItsBoundIsAnErrorForTheGuards(t *testing.T) {
	ctx := context.Background()
	s := shop(t)
	work := t.TempDir()
	git(t, work, "clone", "-q", s.Canonical("shop"), ".")
	os.MkdirAll(filepath.Join(work, ".agents"), 0o755)
	os.MkdirAll(filepath.Join(work, "cfg"), 0o755)
	os.Symlink("../cfg/p.json", filepath.Join(work, ".agents/plugins.json"))
	write := func(size int) string {
		head := `{"entries": [{"path": "tools"}]`
		body := head + strings.Repeat(" ", size-len(head)-1) + "}"
		os.WriteFile(filepath.Join(work, ".agents/rules.json"), []byte(body), 0o644)
		os.WriteFile(filepath.Join(work, "cfg/p.json"), []byte(body), 0o644)
		git(t, work, "add", "-A")
		git(t, work, "commit", "-q", "-m", "index files")
		git(t, work, "push", "-q", "origin", "main")
		return s.Main(ctx, "shop")
	}
	for _, c := range []struct {
		size int
		over bool
	}{{cli.MaxIndexBytes, false}, {cli.MaxIndexBytes + 1, true}} {
		main := write(c.size)
		for _, path := range []string{".agents/rules.json", ".agents/plugins.json"} {
			for name, read := range map[string]func() ([]byte, cli.Way, error){
				"IndexFile":   func() ([]byte, cli.Way, error) { return s.IndexFile(ctx, "shop", path) },
				"IndexFileAt": func() ([]byte, cli.Way, error) { return s.IndexFileAt(ctx, "shop", main, path) },
			} {
				b, _, err := read()
				if c.over != errors.Is(err, gitstore.ErrIndexFileTooLarge) || (c.over && b != nil) || (!c.over && (err != nil || len(b) != c.size)) {
					t.Errorf("%s of %s, %d bytes: %d read, %v", name, path, c.size, len(b), err)
				}
			}
		}
	}
}
