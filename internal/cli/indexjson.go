package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
)

// ErrIndexParse is an index file that is not JSON Crew reads: never read as one that names
// nothing, since the CLI's own reader may take from it what Go's does not, such as an entry beside one
// of another type, a key in another case, a key Crew does not read, or a list after a comment (since
// 1.07, after its security review, the operator's popup answer of session 74, "Exact keys, else error",
// and register row 49).
var ErrIndexParse = errors.New("an index file is not JSON Crew reads: an object whose entries and inherits are lists of objects with a path, each key once, in its own case, and with no key Crew does not read")

// indexPaths are the paths of an index file's entries and inherits, read by its exact keys: the file is
// one JSON object, and it and each of its entries hold each key once; entries and inherits at the top,
// and path in each entry and inherit, are the only keys, and only in that case, so a key in another case
// or any other key is ErrIndexParse; entries and inherits are lists of objects, or null; a path is a
// string with no white space at either end and no NUL. An entry with no path names nothing, and an
// empty file names nothing. count is the number of entries and inherits.
func indexPaths(index []byte) (paths []string, count int, err error) {
	if len(bytes.TrimSpace(index)) == 0 {
		return nil, 0, nil
	}
	doc, err := exactObject(index, "entries", "inherits")
	if err != nil {
		return nil, 0, err
	}
	for _, key := range []string{"entries", "inherits"} {
		raw, ok := doc[key]
		if !ok || string(raw) == "null" {
			continue
		}
		var list []json.RawMessage
		if raw[0] != '[' || json.Unmarshal(raw, &list) != nil {
			return nil, 0, ErrIndexParse
		}
		count += len(list)
		for _, e := range list {
			entry, err := exactObject(e, "path")
			if err != nil {
				return nil, 0, err
			}
			p, ok := entry["path"]
			if !ok {
				continue
			}
			var s string
			if p[0] != '"' || json.Unmarshal(p, &s) != nil {
				return nil, 0, ErrIndexParse
			}
			// Taken as it is, never trimmed: another reader may open " tools " where Crew read tools/
			// (its second round).
			if strings.TrimSpace(s) != s || strings.ContainsRune(s, 0) {
				return nil, 0, ErrIndexParse
			}
			paths = append(paths, s)
		}
	}
	return paths, count, nil
}

// exactObject reads b as one JSON object and nothing after it, its values kept as they are: a key that
// comes twice, or that is not in names (in another case or any other key), is ErrIndexParse.
func exactObject(b []byte, names ...string) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, ErrIndexParse
	}
	out := map[string]json.RawMessage{}
	for dec.More() {
		t, err := dec.Token()
		key, ok := t.(string)
		if err != nil || !ok {
			return nil, ErrIndexParse
		}
		if _, twice := out[key]; twice {
			return nil, ErrIndexParse
		}
		if !slices.Contains(names, key) {
			return nil, ErrIndexParse
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, ErrIndexParse
		}
		out[key] = v
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return nil, ErrIndexParse
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, ErrIndexParse
	}
	return out, nil
}
