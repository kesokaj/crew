package gitstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kesokaj/crew/internal/gitendpoint"
)

// The cuts of a history's fields (6.1, since 1.09). A read of git log keeps at most these of each
// commit as git's output streams, so no field is held whole: a pack of a few hundred kilobytes carries
// a message of hundreds of megabytes, and a main from a remote or a commit stored before 1.09 never
// came through the git endpoint's bound of a commit. A cut field ends in "…", and its commit says Cut.
// One commit's message, which LastChange reads for its trailer, is cut at the endpoint's bound of a
// commit, so every message a push brought is read whole.
const (
	MaxSubject = 1 << 10
	MaxBody    = 16 << 10
	MaxMessage = gitendpoint.MaxCommitBytes
	// An author, an email, an id, a time or a ref; and the parents, about 60 of them.
	maxName    = 1 << 10
	maxParents = 4 << 10
)

// marks are the separators of one reading of git log: random, so a commit's message or author, which
// a run wrote, cannot hold them and shift or hide a commit. Both hold key, once each.
type marks struct{ field, record, key string }

func newMarks() marks {
	var b [12]byte
	rand.Read(b[:])
	k := hex.EncodeToString(b[:])
	return marks{field: "\x1fF" + k + "\x1f", record: "\x1eR" + k + "\x1e", key: k}
}

// format is git's format for fields, each followed by the field mark, and the record mark after them.
func (m marks) format(fields ...string) string {
	esc := func(s string) string { return strings.NewReplacer("\x1f", "%x1f", "\x1e", "%x1e").Replace(s) }
	return strings.Join(fields, esc(m.field)) + esc(m.record)
}

// record is one commit's fields as a history keeps them, and which of them were cut.
type record struct {
	fields []string
	cut    []bool
}

// history takes git log's output of one reading's marks as it streams: each record's fields, each
// kept to its cut, and whether it was cut. What comes after the last record mark, git's newline, is
// no record. Past most records, past keep bytes kept in all, or past raw bytes of git's output, it is
// ErrOutputTooLarge: what is past a cut is still read, and the raw bound ends that read in time.
type history struct {
	m          marks
	cuts       []int
	most       int
	keep, kept int64
	raw, read  int64
	buf, carry []byte
	field      []byte
	fieldCut   bool
	cur        record
	recs       []record
}

func newHistory(m marks, cuts []int, most int, keep, raw int64) *history {
	return &history{m: m, cuts: cuts, most: most, keep: keep, raw: raw}
}

func (h *history) Write(p []byte) (int, error) {
	if h.read += int64(len(p)); h.read > h.raw {
		return 0, ErrOutputTooLarge
	}
	h.buf = append(append(h.buf[:0], h.carry...), p...)
	data := h.buf
	for {
		at, rec, ok := h.next(data)
		if !ok {
			break
		}
		if err := h.add(data[:at]); err != nil {
			return 0, err
		}
		data = data[at+len(h.m.field):]
		if err := h.end(rec); err != nil {
			return 0, err
		}
	}
	// A mark may begin in the last bytes: they wait for the next write.
	hold := min(len(data), len(h.m.field)-1)
	if err := h.add(data[:len(data)-hold]); err != nil {
		return 0, err
	}
	h.carry = append(h.carry[:0], data[len(data)-hold:]...)
	return len(p), nil
}

// next is where the first whole mark in data begins, and whether it ends a record. The two marks are
// as long, and differ only in their first two bytes and their last.
func (h *history) next(data []byte) (int, bool, bool) {
	from := 0
	for {
		i := bytes.Index(data[from:], []byte(h.m.key))
		if i < 0 {
			return 0, false, false
		}
		at := from + i - 2
		if at >= 0 && at+len(h.m.field) <= len(data) {
			switch string(data[at : at+len(h.m.field)]) {
			case h.m.field:
				return at, false, true
			case h.m.record:
				return at, true, true
			}
		}
		if at+len(h.m.field) > len(data) {
			return 0, false, false
		}
		from += i + 1
	}
}

func (h *history) add(b []byte) error {
	cut := 0
	if n := len(h.cur.fields); n < len(h.cuts) {
		cut = h.cuts[n]
	}
	take := min(len(b), max(cut-len(h.field), 0))
	if take < len(b) {
		h.fieldCut = true
	}
	h.kept += int64(take)
	if h.kept > h.keep {
		return ErrOutputTooLarge
	}
	h.field = append(h.field, b[:take]...)
	return nil
}

func (h *history) end(rec bool) error {
	h.cur.fields = append(h.cur.fields, string(whole(h.field, h.fieldCut)))
	h.cur.cut = append(h.cur.cut, h.fieldCut)
	h.field, h.fieldCut = h.field[:0], false
	if !rec {
		return nil
	}
	h.recs = append(h.recs, h.cur)
	h.cur = record{}
	if len(h.recs) > h.most {
		return ErrOutputTooLarge
	}
	return nil
}

// whole leaves out a rune the cut split.
func whole(b []byte, cut bool) []byte {
	if !cut {
		return b
	}
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				return b[:i]
			}
			break
		}
	}
	return b
}

// rawReads is how many times the bound of a read one history reads of git's output: 512 MiB, within
// which 500 commits at the git endpoint's bound of a commit list, each in UTF-8 as the hook takes it,
// since git prints another encoding as UTF-8, up to three times as long (since 1.09, the security
// review of 1.09's change, and the operator's popup answers of session 76, "IDs once full + 512 MiB
// raw" and "IDs after a failure + UTF-8 only").
const rawReads = 8

// readHistory runs git log in dir with the format of m, and keeps each record's fields to cuts: at
// most most records, at most the bound of a read kept in all, and at most rawReads times it read.
func (s *Store) readHistory(ctx context.Context, dir string, m marks, cuts []int, most int, args ...string) ([]record, error) {
	h := newHistory(m, cuts, most, s.maxOutput(), rawReads*s.maxOutput())
	if err := s.stream(ctx, dir, h, args...); err != nil {
		return nil, err
	}
	return h.recs, nil
}

// The fields of a commit as a history reads them, with their cuts; ref is the branch a history of all
// branches reached it by.
var (
	commitFields = []string{"%H", "%P", "%an", "%ae", "%aI", "%s", "%b"}
	commitCuts   = []int{maxName, maxParents, maxName, maxName, maxName, MaxSubject, MaxBody}
)

// commits are the records of a history as the screens show them; a record of other fields than
// fields is none.
func commits(recs []record, fields int) []Commit {
	cs := []Commit{}
	for _, r := range recs {
		f := r.fields
		if len(f) != fields {
			continue
		}
		text := func(i int) string {
			if r.cut[i] {
				return f[i] + "…"
			}
			return f[i]
		}
		when, _ := time.Parse(time.RFC3339, strings.TrimSpace(f[4]))
		parents := strings.Fields(f[1])
		if r.cut[1] && len(parents) > 0 {
			// The last one the cut split.
			parents = parents[:len(parents)-1]
		}
		c := Commit{ID: strings.TrimSpace(f[0]), Parents: parents, Author: text(2), Email: text(3), When: when,
			Subject: text(5), Body: strings.TrimSpace(f[6])}
		if r.cut[6] {
			c.Body += "…"
		}
		c.Cut = r.cut[1] || r.cut[2] || r.cut[3] || r.cut[5] || r.cut[6]
		if fields > 7 {
			c.Ref = strings.TrimPrefix(f[7], "refs/heads/")
		}
		cs = append(cs, c)
	}
	return cs
}
