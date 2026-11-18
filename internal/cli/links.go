package cli

import (
	"bytes"
	"errors"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/kesokaj/crew/internal/runspec"
)

// Entry is what a tree holds at a path, as a lookup of it reads: its mode as git writes it, its
// object and its size; Mode is "" when the tree holds nothing there.
type Entry struct {
	Mode   string
	Object string
	Size   int64
}

// The modes of a tree's entries that Follow tells apart.
const (
	ModeLink   = "120000"
	ModeFolder = "040000"
)

// The most a way through links is followed: the links Linux follows in one open, the steps of the way
// that each read the tree, and the length of a link's text, as Linux's longest path.
const (
	MaxLinks     = 40
	MaxLinkSteps = 256
	MaxLinkText  = 4096
)

// The most links of a tree Linked follows, and the most link texts LinkedTree reads, each object once,
// about the git processes of 1.05's hook for each side of a push: past either, as past a way's bounds,
// ErrLinkWay (since 1.06, after its security review and its second round).
const (
	MaxLinked    = 4096
	MaxLinkTexts = 1024
)

// ErrLinkWay is a way through links that is longer than Follow follows or that ends out of the
// workspace, or more links than Linked follows (since 1.06, of a folder's way too).
var ErrLinkWay = errors.New("a way through links is longer than Crew follows, or ends out of the workspace")

// Way is how a path of a tree is reached through links (since 1.05): the file it leads to, "" when it
// leads to none, with its entry; the links on the way, by their own paths; and where the way ends in
// the tree, the file, a folder, a submodule or the part that is not there.
// Every path on a way that went through a link stands in for the index file (13.8): a change of one
// changes what the CLI reads.
type Way struct {
	File  string
	Entry Entry
	Links []string
	End   string
}

// Paths are the links of the way and its end, each as a folder at the root, as Below reads it, so the
// path, what is below it, and a link or a file put where a folder above it is, all match; nil when no
// link was followed.
func (w Way) Paths() []string {
	if len(w.Links) == 0 {
		return nil
	}
	var out []string
	for _, p := range append(append([]string{}, w.Links...), w.End) {
		if p != "" {
			out = append(out, p+"/")
		}
	}
	return out
}

// Follow reads a path of a tree as the CLI's open of it does, part by part, in a workspace that is the
// tree, at runspec.Workspace: a part that is a link goes on where its text leads, from the link's own
// folder, and ".." from where the way is. A way that leaves the tree, by a link's text that starts with
// "/" or by ".." past its root, is followed as a place of the sandbox, and comes back into the tree
// where it reaches the workspace again (since 1.05, after its security review); one that ends
// elsewhere is ErrLinkWay: the sandbox has links of its own, such as /proc/self/cwd, which lead back
// into the workspace, so no place outside is read as one that names nothing (its second round). A way
// to a folder, to a submodule, through a file, or to a part the tree does not have ends at no file,
// though its end is on the way. lookup reads the entry of one path, linkText the text of a link's
// entry. A way through more than MaxLinks links or MaxLinkSteps lookups, or a link's text longer than
// MaxLinkText or holding a NUL, is ErrLinkWay.
func Follow(p string, lookup func(path string) (Entry, error), linkText func(Entry) (string, error)) (Way, error) {
	var way Way
	parts := strings.Split(p, "/")
	done := ""
	// outside is where the way is while it is out of the tree, from the sandbox's root; nil in the tree.
	var outside []string
	steps := 0
	for len(parts) > 0 {
		part := parts[0]
		parts = parts[1:]
		if outside != nil {
			outside = sandboxPlace(outside, part)
			if slices.Equal(outside, workspaceParts) {
				outside, done = nil, "" // back in the tree, at its root
			}
			continue
		}
		switch part {
		case "", ".":
			continue
		case "..":
			if done == "" {
				outside = slices.Clone(workspaceParts[:len(workspaceParts)-1])
				continue
			}
			if done = path.Dir(done); done == "." {
				done = ""
			}
			continue
		}
		next := path.Join(done, part)
		if steps++; steps > MaxLinkSteps {
			return way, ErrLinkWay
		}
		e, err := lookup(next)
		if err != nil {
			return way, err
		}
		switch {
		case e.Mode == ModeLink:
			way.Links = append(way.Links, next)
			if len(way.Links) > MaxLinks || e.Size > MaxLinkText {
				return way, ErrLinkWay
			}
			text, err := linkText(e)
			if err != nil {
				return way, err
			}
			if len(text) > MaxLinkText || strings.ContainsRune(text, 0) {
				return way, ErrLinkWay
			}
			if text == "" {
				return way, nil // nowhere
			}
			if strings.HasPrefix(text, "/") {
				outside = []string{}
			}
			parts = append(strings.Split(text, "/"), parts...)
			continue
		case e.Mode == ModeFolder && len(parts) > 0:
			done = next
			continue
		case e.Mode == "" || len(parts) > 0 || !strings.HasPrefix(e.Mode, "100"):
			// Not there, a file where a folder goes on, or a folder or a submodule at the end.
			way.End = next
			return way, nil
		}
		way.File, way.Entry, way.End = next, e, next
		return way, nil
	}
	if outside != nil {
		return way, ErrLinkWay // out of the workspace
	}
	// The way ended in a folder, as a link's text "cfg/" does.
	way.End = done
	return way, nil
}

// workspaceParts are the parts of the workspace's path, from the sandbox's root.
var workspaceParts = strings.Split(strings.TrimPrefix(runspec.Workspace, "/"), "/")

// sandboxPlace is where part leads from place, a path of the sandbox outside the tree, read by its name
// alone: /home/agent/work by its own name comes back into the tree, and every other place is an error.
func sandboxPlace(place []string, part string) []string {
	switch part {
	case "", ".":
		return place
	case "..":
		if len(place) == 0 {
			return place
		}
		return place[:len(place)-1]
	}
	return append(place, part)
}

// Linked are the paths of a tree that stand for a path of patterns through a link (since 1.06): a link
// at or below a pattern, such as .agents/skills made a link to a folder elsewhere, or a skill's folder
// in it, is followed to where it leads, and a pattern below a link, such as .agents/skills when .agents
// is one, is followed itself. Every path of such a way counts as the pattern does, as Way.Paths gives
// them, and where it ends is read again for more links. links are the links of the tree, and follow
// reads a path of it as Follow does. A way that ends at the tree's root stands for AllPaths. A way follow
// refuses, or more than MaxLinked, is an error: what a link leads to is never read as nothing. AllPaths
// as a pattern stands for no link, as Below and Above read it: every path is one already. Each pattern
// looks only at the links its own prefix, folders and name can match (linkIndex, since 1.07).
func Linked(links, patterns []string, follow func(path string) (Way, error)) ([]string, error) {
	var out []string
	// Each path once: ways that share a chain of links stand for it once (its second round).
	have := map[string]bool{}
	add := func(p string) {
		if !have[p] {
			have[p] = true
			out = append(out, p)
		}
	}
	followed := map[string]bool{}
	queued := map[string]bool{}
	queue := slices.Clone(patterns)
	index := newLinkIndex(links)
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		for _, i := range index.candidates(p) {
			l := links[i]
			at := l
			switch {
			case Below(l, []string{p}):
			case Above(l, []string{p}):
				at = strings.TrimSuffix(p, "/")
			default:
				continue
			}
			if followed[at] {
				continue
			}
			if followed[at] = true; len(followed) > MaxLinked {
				return nil, ErrLinkWay
			}
			way, err := follow(at)
			if err != nil {
				return nil, err
			}
			for _, p := range way.Paths() {
				add(p)
			}
			if way.File == "" && way.End == "" && len(way.Links) > 0 {
				// A way that ends at the tree's root, or at a link with no text: the whole workspace.
				add(AllPaths)
			}
			// Only where a way ends can hold more links: nothing of a tree is below one of its links.
			if end := way.End; end != "" && !queued[end] {
				queued[end] = true
				queue = append(queue, end+"/")
			}
		}
	}
	return out, nil
}

// ErrListing is a listing of a tree with a record that does not read: never read as one with no links.
var ErrListing = errors.New("a listing of a tree does not read")

// modes are the modes git lists a tree's entries with.
var modes = map[string]string{"100644": "100644", "100755": "100755", ModeLink: ModeLink, ModeFolder: ModeFolder, "160000": "160000"}

// LinkedTree is Linked over a tree as `git ls-tree -r -t -l -z --full-tree` lists it: every lookup of a
// way is read from the listing, never from git, and a link's text by read, once for each object, at
// most MaxLinkTexts of them; a record of the listing that does not read is ErrListing (since 1.06, after its security review: a lookup each from git let a push
// of a few KB start some 66,000 of its processes). It returns the links of the tree too.
func LinkedTree(listing []byte, patterns []string, read func(object string) (string, error)) (linked, links []string, err error) {
	// What Follow needs of each entry: a link with its object and size, and of the rest only the mode,
	// as one of the few strings a mode is, so a tree's listing is not kept whole (its second round).
	entries := map[string]Entry{}
	for _, rec := range bytes.Split(listing, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		meta, p, ok := bytes.Cut(rec, []byte("\t"))
		f := strings.Fields(string(meta))
		if !ok || len(f) != 4 || len(p) == 0 {
			return nil, nil, ErrListing
		}
		mode, known := modes[f[0]]
		if !known {
			return nil, nil, ErrListing
		}
		if mode != ModeLink {
			entries[string(p)] = Entry{Mode: mode}
			continue
		}
		size, err := strconv.ParseInt(f[3], 10, 64)
		if err != nil {
			return nil, nil, ErrListing
		}
		entries[string(p)] = Entry{Mode: mode, Object: f[2], Size: size}
		links = append(links, string(p))
	}
	texts := map[string]string{}
	text := func(e Entry) (string, error) {
		if t, ok := texts[e.Object]; ok {
			return t, nil
		}
		if len(texts) >= MaxLinkTexts {
			return "", ErrLinkWay
		}
		t, err := read(e.Object)
		if err != nil {
			return "", err
		}
		texts[e.Object] = t
		return t, nil
	}
	lookup := func(p string) (Entry, error) { return entries[p], nil }
	linked, err = Linked(links, patterns, func(p string) (Way, error) { return Follow(p, lookup, text) })
	return linked, links, err
}
