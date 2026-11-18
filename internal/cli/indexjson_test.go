package cli

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// An index file is read by its exact keys: one JSON object, each key once, only entries and inherits at
// the top and path in each entry and inherit, only in their own case, lists of objects and a path that
// is a string, taken as it is: white space at either end, or a NUL, is no path Crew trims (its second
// round). Anything else, including any key Crew does not read, is ErrIndexParse, never a file that names
// nothing, as the CLI's own reader may take from it what Go's does not; an empty file names nothing
// (13.8, since 1.07, after its security review, and register row 49).
func TestAnIndexFileIsReadByItsExactKeys(t *testing.T) {
	if !strings.Contains(ErrIndexParse.Error(), "key Crew does not read") {
		t.Errorf("ErrIndexParse = %q", ErrIndexParse)
	}
	for doc, want := range map[string][]string{
		`{"entries": [{"path": "tools"}]}`:                              {"tools/"},
		` { "entries" : [ { "path" : "tools" } ] , "inherits": null } `: {"tools/"},
		`{"inherits": [{"path": "a"}, {}], "entries": []}`:              {"a/"},
		``:      nil,
		" \n\t": nil,
		`{}`:    nil,
	} {
		if names, err := Named([]byte(doc)); err != nil || !slices.Equal(names, want) {
			t.Errorf("%q: %q %v", doc, names, err)
		}
	}
	for _, doc := range []string{
		`{"entries": [{"path": "tools", "note": "x"}]}`,
		`{"entries": [{"path": "tools"}], "plugins": []}`,
		`{"inherits": [{"path": "a"}, {"other": 1}], "entries": []}`,
		`{"entries": [{"path": "tools", "Other": {"Entries": [], "PATH": 1}}], "Version": 2}`,
		`{"entries":[{"path":"tools"}],"inherits":{}}`,
		`{"entries":[{"path":"tools"},{"path":7}]}`,
		`{"entries":[{"path":"tools"}]} // c`,
		`// c` + "\n" + `{"entries":[{"path":"tools"}]}`,
		`{"entries":[{"path":"tools"}],"Entries":[]}`,
		`{"entries":[{"path":"tools","PATH":"x"}]}`,
		`{"entries":[{"path":"tools"}],"entries":[]}`,
		`{"entries":[{"path":"tools","path":"x"}]}`,
		`{"entries":[{"path":"tools"},]}`,
		`{"entries":"tools"}`,
		`{"entries":{"path":"tools"}}`,
		`{"entries":[["tools"]]}`,
		`{"entries":[{"path":null}]}`,
		`[{"path":"tools"}]`,
		`{"entries":[{"path":"tools"}]} {}`,
		`not json`,
		`{"entries":[{"path":" tools"}]}`,
		`{"entries":[{"path":"tools "}]}`,
		`{"entries":[{"path":"tools\n"}]}`,
		`{"entries":[{"path":"\u00a0tools"}]}`,
		`{"entries":[{"path":"tools\u0000x"}]}`,
	} {
		if names, err := Named([]byte(doc)); !errors.Is(err, ErrIndexParse) || names != nil {
			t.Errorf("%q: %q %v", doc, names, err)
		}
	}
	// Indexed sees the error of each index file, whichever came first.
	_, _, err := Indexed(func(f string) ([]byte, Way) {
		switch f {
		case ".agents/rules.json":
			return []byte(`{"entries": [{"path": "/proc/self/cwd/x"}]}`), Way{}
		case ".agents/skills.json":
			return []byte(`{"Entries": []}`), Way{}
		}
		return nil, Way{}
	})
	if !errors.Is(err, ErrIndexParse) || !errors.Is(err, ErrNamedOutside) {
		t.Fatalf("%v", err)
	}
}
