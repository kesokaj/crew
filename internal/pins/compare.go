package pins

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/kesokaj/crew/internal/cli"
)

// Finding is a ticket the look makes: a new version of one tool, or the image digests that moved.
type Finding struct {
	// Keys are its keys in pin_tickets, one ever per project: "go@1.27.4", or "image:KEY@sha256:…" for
	// each image it moves.
	Keys []string
	// Tool names it in the ticket's title, and Source the publisher; From and To are the pin and the new
	// version, none for images.
	Tool, Source, From, To string
	// Set are the lines of versions.lock it changes, with their new values.
	Set []KV
	// Places are the other files that name the same values, which tools/check-versions.py holds in step.
	Places []string
}

// KV is one line of versions.lock.
type KV struct{ Key, Value string }

// LockKeys are the keys of versions.lock the finding changes.
func (f Finding) LockKeys() []string {
	out := make([]string, len(f.Set))
	for i, kv := range f.Set {
		out[i] = kv.Key
	}
	return out
}

// Line is what the look read of one source: Crew's words, with only values that passed their shape.
type Line struct {
	Source string `json:"source"`
	Text   string `json:"text"`
	OK     bool   `json:"ok"`
}

// Compare reads each source the lock has a pin of, and gives what is newer, with a line for each
// source. A source that fails gives its line and nothing else; the others go on.
func (s *Sources) Compare(ctx context.Context, lock map[string]string) ([]Finding, []Line) {
	var found []Finding
	var lines []Line
	add := func(f *Finding, l Line) {
		lines = append(lines, l)
		if f != nil {
			found = append(found, *f)
		}
	}
	add(s.goFinding(ctx, lock))
	add(s.nodeFinding(ctx, lock))
	add(s.versionFinding(ctx, lock, "npm", "chrome-devtools-mcp", "CHROME_DEVTOOLS_MCP_VERSION", func() (Release, error) {
		return s.NPMRelease(ctx, "chrome-devtools-mcp")
	}, func(r Release) []KV {
		return []KV{{"CHROME_DEVTOOLS_MCP_VERSION", r.Version}, {"CHROME_DEVTOOLS_MCP_INTEGRITY", r.Sum}}
	}))
	var agyURL string
	add(s.versionFinding(ctx, lock, "agy's manifest", cli.AgyName, "AGY_VERSION", func() (Release, error) {
		v, u, sum, err := s.AgyRelease(ctx)
		agyURL = u
		return Release{v, sum}, err
	}, func(r Release) []KV {
		return []KV{{"AGY_VERSION", r.Version}, {"AGY_URL", agyURL}, {"AGY_SHA512", r.Sum}}
	}))
	add(s.versionFinding(ctx, lock, "gh's apt index", "gh", "RUNTIME_GH_VERSION", func() (Release, error) {
		v, err := s.GHVersion(ctx)
		return Release{Version: v}, err
	}, func(r Release) []KV { return []KV{{"RUNTIME_GH_VERSION", r.Version}} }))
	// The image digests, but not an image a version found above moves to its new tag.
	var taken []string
	for _, f := range found {
		taken = append(taken, f.LockKeys()...)
	}
	f, l := s.imageFinding(ctx, lock, taken)
	lines = append(lines, l...)
	if f != nil {
		found = append(found, *f)
	}
	return found, lines
}

func notRead(source string, err error) Line {
	return Line{source, "not read: it " + err.Error(), false}
}

// goFinding is a newer Go, with its image's new tag, which must be there already: a ticket is made
// only when it can be done.
func (s *Sources) goFinding(ctx context.Context, lock map[string]string) (*Finding, Line) {
	const src = "go.dev"
	pin := lock["GO_VERSION"]
	if !versionRE.MatchString(pin) {
		return nil, Line{src, "versions.lock pins no Go version", false}
	}
	r, err := s.GoRelease(ctx)
	if err != nil {
		return nil, notRead(src, err)
	}
	if !Newer(r.Version, pin) {
		return nil, Line{src, fmt.Sprintf("Go %s; the pin is %s", r.Version, pin), true}
	}
	image, why := s.retag(ctx, lock["GO_IMAGE"], pin, r.Version)
	if why != "" {
		return nil, Line{src, fmt.Sprintf("Go %s, newer than the pin %s; %s", r.Version, pin, why), true}
	}
	return &Finding{
		Keys: []string{"go@" + r.Version}, Tool: "Go", Source: src, From: pin, To: r.Version,
		Set:    []KV{{"GO_VERSION", r.Version}, {"RUNTIME_GO_VERSION", r.Version}, {"RUNTIME_GO_SHA256", r.Sum}, {"GO_IMAGE", image}},
		Places: []string{"go.mod", "ingress/go.mod", "deploy/Dockerfile.server", "ingress/Dockerfile", "image/Dockerfile"},
	}, Line{src, fmt.Sprintf("Go %s, newer than the pin %s", r.Version, pin), true}
}

// nodeFinding is a newer Node of the pin's line, with the UI's image at its new tag.
func (s *Sources) nodeFinding(ctx context.Context, lock map[string]string) (*Finding, Line) {
	const src = "nodejs.org"
	pin, web := lock["RUNTIME_NODE_VERSION"], lock["WEB_NODE_VERSION"]
	if !versionRE.MatchString(pin) || !versionRE.MatchString(web) {
		return nil, Line{src, "versions.lock pins no Node version", false}
	}
	major, _, _ := strings.Cut(pin, ".")
	r, err := s.NodeRelease(ctx, major)
	if err != nil {
		return nil, notRead(src, err)
	}
	if !Newer(r.Version, pin) && !Newer(r.Version, web) {
		return nil, Line{src, fmt.Sprintf("Node %s; the pin is %s", r.Version, pin), true}
	}
	image, why := s.retag(ctx, lock["WEB_NODE_IMAGE"], web, r.Version)
	if why != "" {
		return nil, Line{src, fmt.Sprintf("Node %s, newer than the pin %s; %s", r.Version, pin, why), true}
	}
	return &Finding{
		Keys: []string{"node@" + r.Version}, Tool: "Node", Source: src, From: pin, To: r.Version,
		Set:    []KV{{"RUNTIME_NODE_VERSION", r.Version}, {"RUNTIME_NODE_SHA256", r.Sum}, {"WEB_NODE_VERSION", r.Version}, {"WEB_NODE_IMAGE", image}},
		Places: []string{"web/.node-version", "deploy/Dockerfile.server", "image/Dockerfile"},
	}, Line{src, fmt.Sprintf("Node %s, newer than the pin %s", r.Version, pin), true}
}

// versionFinding is a newer version of a tool the runtime image alone names.
func (s *Sources) versionFinding(ctx context.Context, lock map[string]string, src, tool, key string, read func() (Release, error), set func(Release) []KV) (*Finding, Line) {
	pin := lock[key]
	if !versionRE.MatchString(pin) {
		return nil, Line{src, "versions.lock pins no version of " + tool, false}
	}
	r, err := read()
	if err != nil {
		return nil, notRead(src, err)
	}
	if !Newer(r.Version, pin) {
		return nil, Line{src, fmt.Sprintf("%s %s; the pin is %s", tool, r.Version, pin), true}
	}
	return &Finding{Keys: []string{tool + "@" + r.Version}, Tool: tool, Source: src, From: pin, To: r.Version, Set: set(r), Places: []string{"image/Dockerfile"}},
		Line{src, fmt.Sprintf("%s %s, newer than the pin %s", tool, r.Version, pin), true}
}

// retag is a pinned image at the tag of a new version, by its digest now: the pin's tag with its
// version replaced, golang:1.27.3-trixie to golang:1.27.4-trixie. why says why there is none.
func (s *Sources) retag(ctx context.Context, image, pin, version string) (string, string) {
	m := imageRE.FindStringSubmatch(image)
	if m == nil {
		return "", "its image is not pinned by a tag and a digest"
	}
	name, tag := m[1], m[2]
	rest, ok := strings.CutPrefix(tag, pin)
	if !ok || (rest != "" && !strings.HasPrefix(rest, "-")) {
		return "", "its image's tag does not begin with the pin"
	}
	d, err := s.Digest(ctx, name, version+rest)
	if err != nil {
		return "", fmt.Sprintf("its image %s:%s%s is not to be had yet: it %v", name, version, rest, err)
	}
	return name + ":" + version + rest + "@" + d, ""
}

// imageFinding is every pinned image whose tag's digest moved, in one ticket, but none of the keys
// taken. Each image gives a line.
func (s *Sources) imageFinding(ctx context.Context, lock map[string]string, taken []string) (*Finding, []Line) {
	var keys []string
	for k := range lock {
		if strings.HasSuffix(k, "_IMAGE") && !slices.Contains(taken, k) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	f := &Finding{Tool: "the images"}
	var lines []Line
	for _, k := range keys {
		m := imageRE.FindStringSubmatch(lock[k])
		if m == nil {
			lines = append(lines, Line{k, "versions.lock does not pin it by a tag and a digest", false})
			continue
		}
		d, err := s.Digest(ctx, m[1], m[2])
		switch {
		case err != nil:
			lines = append(lines, notRead(k, err))
		case d == m[3]:
			lines = append(lines, Line{k, m[1] + ":" + m[2] + " as pinned", true})
		default:
			lines = append(lines, Line{k, m[1] + ":" + m[2] + " moved to " + d, true})
			f.Keys = append(f.Keys, "image:"+k+"@"+d)
			f.Set = append(f.Set, KV{k, m[1] + ":" + m[2] + "@" + d})
		}
	}
	if len(f.Set) == 0 {
		return nil, lines
	}
	f.Places = []string{"deploy/Dockerfile.server", "ingress/Dockerfile", "image/Dockerfile"}
	return f, lines
}
