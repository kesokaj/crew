package cli

import (
	"errors"
	"strings"
	"testing"
)

// An index file names at most MaxIndexEntries folders, entries and inherits counted together, none
// longer than MaxEntryBytes: past either it is ErrIndexEntries, which the hook and the guards stop on,
// with every folder it names, which crew-run reads as before (since 1.07).
func TestAnIndexFileNamesAtMost1024FoldersOf4096Bytes(t *testing.T) {
	entries := func(n int, key string) string {
		var parts []string
		for range n {
			parts = append(parts, `{"path": "tools"}`)
		}
		return `"` + key + `": [` + strings.Join(parts, ",") + `]`
	}
	long := func(n int) string {
		return `"entries": [{"path": "` + strings.Repeat("a/", n/2) + strings.Repeat("b", n%2) + `"}]`
	}
	for name, c := range map[string]struct {
		doc   string
		names int
		over  bool
	}{
		"1024 entries":              {doc: entries(1024, "entries"), names: 1024},
		"1025 entries":              {doc: entries(1025, "entries"), names: 1025, over: true},
		"1000 entries, 24 inherits": {doc: entries(1000, "entries") + "," + entries(24, "inherits"), names: 1024},
		"1000 entries, 25 inherits": {doc: entries(1000, "entries") + "," + entries(25, "inherits"), names: 1025, over: true},
		"an entry of 4096 bytes":    {doc: long(4096), names: 1},
		"an entry of 4097 bytes":    {doc: long(4097), names: 1, over: true},
	} {
		names, err := Named([]byte("{" + c.doc + "}"))
		if len(names) != c.names || errors.Is(err, ErrIndexEntries) != c.over || (!c.over && err != nil) {
			t.Errorf("%s: %d names, %v", name, len(names), err)
		}
		_, instructions, err := Indexed(func(f string) ([]byte, Way) {
			if f == ".agents/rules.json" {
				return []byte("{" + c.doc + "}"), Way{}
			}
			return nil, Way{}
		})
		if len(instructions) != c.names || errors.Is(err, ErrIndexEntries) != c.over {
			t.Errorf("%s, through Indexed: %d names, %v", name, len(instructions), err)
		}
	}
}
