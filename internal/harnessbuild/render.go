package harnessbuild

import (
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// Marker is the first line of the context files the build writes, so an
// agent or a person who opens the context in a sandbox knows where
// it comes from and that an edit there does not last.
const Marker = "<!-- built into Crew's runtime image by crew-harnessbuild from harness/ in Crew's repository: change it there, through a ticket -->"

// Canary is the last line of every rendered context. A check asks agy for the
// word; only a file loaded to its end can answer, which is how agy's cut of
// its rules block is caught (spike item 74).
const Canary = "Canary: this context ends with the word GANNET."

// RenderContext assembles the context: context/AGENTS.md first, every other
// context/*.md in name order, then the compact inventory, then the Canary
// line, which is last so a check that reads it proves the whole file loaded.
func RenderContext(src Source, inv Inventory) ([]byte, error) {
	if err := checkManagedTree(src); err != nil {
		return nil, err
	}
	main, err := fs.ReadFile(src.FS, "context/AGENTS.md")
	if err != nil {
		return nil, fmt.Errorf("reading context: %w", err)
	}
	extras, err := fs.Glob(src.FS, "context/*.md")
	if err != nil {
		return nil, err
	}
	sort.Strings(extras)

	var b strings.Builder
	b.WriteString(Marker + "\n")
	b.Write(main)
	for _, p := range extras {
		if path.Base(p) == "AGENTS.md" {
			continue
		}
		data, err := fs.ReadFile(src.FS, p)
		if err != nil {
			return nil, err
		}
		b.WriteString("\n")
		b.Write(data)
	}
	b.WriteString("\n")
	b.WriteString(RenderInventoryCompact(inv))
	b.WriteString("\n" + Canary + "\n")
	return []byte(b.String()), nil
}
