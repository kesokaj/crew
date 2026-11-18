// Package pins is the daily look (design 12.8, since 0.51; 0.56): what the publishers offer now,
// against what versions.lock of Crew's project pins, and a ticket on Crew's own board for each new
// version, once. It reads the publishers from the server, with no secret and no sandbox, and takes a
// value only in its own shape: a value becomes a line of versions.lock, a check command, and the
// text an agent reads (safety rule 14).
package pins

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The shapes a value must have, whole. None holds a quote, a space, a `$`, or a line break. A version
// has two to four parts of at most nine digits, none with a leading zero, so it fits a key and a title.
var (
	versionRE   = regexp.MustCompile(`^(0|[1-9][0-9]{0,8})(\.(0|[1-9][0-9]{0,8})){1,3}$`)
	sha256RE    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	sha512RE    = regexp.MustCompile(`^[0-9a-f]{128}$`)
	integrityRE = regexp.MustCompile(`^sha512-[A-Za-z0-9+/]{86}==$`)
	digestRE    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	// An image of versions.lock: its name, its tag, and its index digest.
	imageRE = regexp.MustCompile(`^([a-z0-9]+(?:[._/-][a-z0-9]+)*):([A-Za-z0-9_][A-Za-z0-9._-]{0,127})@(sha256:[0-9a-f]{64})$`)
	// A token of Docker Hub's: a JWT, which the look only passes back, of at most 8 KiB.
	tokenRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// maxAnswer is the most the look reads of an answer; the largest, nodejs.org's index, is about 340 KB.
const maxAnswer = 4 << 20

// Sources are the publishers the look reads. Each base is the publisher's own, https, unless a test
// gives its server.
type Sources struct {
	GoDL, Node, NPM, DockerAuth, DockerHub, MCR, GH string
	// Agy is the address of agy's release manifest, and AgyTarball the shape of the tarball's address
	// it names, with the version as its first group: the adapter's (internal/cli/agy.go).
	Agy        string
	AgyTarball *regexp.Regexp
	// Timeout bounds one request; 30 s when 0.
	Timeout time.Duration
}

// Publishers are the publishers' own addresses, with agy's from its adapter.
func Publishers(agyManifest string, agyTarball *regexp.Regexp) *Sources {
	return &Sources{
		GoDL: "https://go.dev", Node: "https://nodejs.org", NPM: "https://registry.npmjs.org",
		DockerAuth: "https://auth.docker.io", DockerHub: "https://registry-1.docker.io", MCR: "https://mcr.microsoft.com",
		GH: "https://cli.github.com", Agy: agyManifest, AgyTarball: agyTarball,
	}
}

// Error is why a source gave nothing, in Crew's own words: no byte of the answer is in it, since the
// look's lines are shown on Versions.
type Error struct{ Why string }

func (e *Error) Error() string { return e.Why }

var (
	errShape    = &Error{"answered out of shape"}
	errTooLarge = &Error{"answered more than 4 MiB"}
	errRedirect = &Error{"answered with a redirect, which the look does not follow"}
)

func refused(code int) error { return &Error{fmt.Sprintf("answered %d", code)} }

// client follows no redirect: an answer that points elsewhere, at an address of the machine's
// network too, is not fetched. It sends no header that names the installation (AGENTS.md, Clean 7).
func (s *Sources) client() *http.Client {
	t := s.Timeout
	if t == 0 {
		t = 30 * time.Second
	}
	return &http.Client{Timeout: t, CheckRedirect: func(*http.Request, []*http.Request) error { return errRedirect }}
}

// fetch is one request, its answer read up to maxAnswer.
func (s *Sources) fetch(ctx context.Context, method, addr string, header http.Header) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, method, addr, nil)
	if err != nil {
		return nil, nil, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	res, err := s.client().Do(req)
	if errors.Is(err, errRedirect) {
		return nil, nil, errRedirect
	}
	if err != nil {
		return nil, nil, &Error{"did not answer"}
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, nil, refused(res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxAnswer+1))
	if err != nil {
		return nil, nil, &Error{"broke off its answer"}
	}
	if len(body) > maxAnswer {
		return nil, nil, errTooLarge
	}
	return body, res.Header, nil
}

func (s *Sources) json(ctx context.Context, addr string, v any) error {
	body, _, err := s.fetch(ctx, http.MethodGet, addr, nil)
	if err != nil {
		return err
	}
	if json.Unmarshal(body, v) != nil {
		return errShape
	}
	return nil
}

// Release is a version a publisher offers, with the checksum of what a build downloads.
type Release struct{ Version, Sum string }

// GoRelease is go.dev's newest stable release, and the sha256 of its linux-amd64 archive.
func (s *Sources) GoRelease(ctx context.Context) (Release, error) {
	var list []struct {
		Version string `json:"version"`
		Stable  bool   `json:"stable"`
		Files   []struct {
			Filename, OS, Arch, Kind, SHA256 string
		} `json:"files"`
	}
	if err := s.json(ctx, s.GoDL+"/dl/?mode=json", &list); err != nil {
		return Release{}, err
	}
	for _, r := range list {
		if !r.Stable {
			continue
		}
		v, ok := strings.CutPrefix(r.Version, "go")
		if !ok || !versionRE.MatchString(v) {
			return Release{}, errShape
		}
		for _, f := range r.Files {
			if f.OS == "linux" && f.Arch == "amd64" && f.Kind == "archive" && f.Filename == "go"+v+".linux-amd64.tar.gz" {
				if !sha256RE.MatchString(f.SHA256) {
					return Release{}, errShape
				}
				return Release{v, f.SHA256}, nil
			}
		}
		return Release{}, &Error{"lists no linux-amd64 archive of its newest release"}
	}
	return Release{}, &Error{"lists no stable release"}
}

// NodeRelease is the newest release of a line of nodejs.org's, and the sha256 of its linux-x64 tarball.
func (s *Sources) NodeRelease(ctx context.Context, major string) (Release, error) {
	if !regexp.MustCompile(`^[0-9]{1,3}$`).MatchString(major) {
		return Release{}, errShape
	}
	var list []struct {
		Version string `json:"version"`
	}
	if err := s.json(ctx, s.Node+"/dist/index.json", &list); err != nil {
		return Release{}, err
	}
	newest := ""
	for _, r := range list {
		v, ok := strings.CutPrefix(r.Version, "v")
		if !ok || !versionRE.MatchString(v) || !strings.HasPrefix(v, major+".") {
			continue
		}
		if newest == "" || Newer(v, newest) {
			newest = v
		}
	}
	if newest == "" {
		return Release{}, &Error{"lists no release of the line " + major}
	}
	body, _, err := s.fetch(ctx, http.MethodGet, s.Node+"/dist/v"+newest+"/SHASUMS256.txt", nil)
	if err != nil {
		return Release{}, err
	}
	want := "node-v" + newest + "-linux-x64.tar.xz"
	for _, l := range strings.Split(string(body), "\n") {
		f := strings.Fields(l)
		if len(f) == 2 && f[1] == want {
			if !sha256RE.MatchString(f[0]) {
				return Release{}, errShape
			}
			return Release{newest, f[0]}, nil
		}
	}
	return Release{}, &Error{"lists no checksum of the linux-x64 tarball of " + newest}
}

// NPMRelease is a package's latest version on the npm registry, with the registry's integrity of it.
func (s *Sources) NPMRelease(ctx context.Context, pkg string) (Release, error) {
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,100}$`).MatchString(pkg) {
		return Release{}, errShape
	}
	var r struct {
		Version string `json:"version"`
		Dist    struct {
			Integrity string `json:"integrity"`
		} `json:"dist"`
	}
	if err := s.json(ctx, s.NPM+"/"+pkg+"/latest", &r); err != nil {
		return Release{}, err
	}
	if !versionRE.MatchString(r.Version) || !integrityRE.MatchString(r.Dist.Integrity) {
		return Release{}, errShape
	}
	return Release{r.Version, r.Dist.Integrity}, nil
}

// AgyRelease is the version agy's manifest names, its tarball's address, and that tarball's sha512.
func (s *Sources) AgyRelease(ctx context.Context) (version, tarball, sha512 string, err error) {
	var m struct {
		Version string `json:"version"`
		URL     string `json:"url"`
		SHA512  string `json:"sha512"`
	}
	if err := s.json(ctx, s.Agy, &m); err != nil {
		return "", "", "", err
	}
	if !versionRE.MatchString(m.Version) || !sha512RE.MatchString(m.SHA512) || s.AgyTarball == nil {
		return "", "", "", errShape
	}
	if g := s.AgyTarball.FindStringSubmatch(m.URL); g == nil || g[1] != m.Version {
		return "", "", "", &Error{"names a tarball that is not the vendor's of that version"}
	}
	return m.Version, m.URL, m.SHA512, nil
}

// GHVersion is the highest version of gh in GitHub's apt index for amd64.
func (s *Sources) GHVersion(ctx context.Context) (string, error) {
	body, _, err := s.fetch(ctx, http.MethodGet, s.GH+"/packages/dists/stable/main/binary-amd64/Packages", nil)
	if err != nil {
		return "", err
	}
	newest := ""
	for _, stanza := range strings.Split(string(body), "\n\n") {
		f := map[string]string{}
		sc := bufio.NewScanner(strings.NewReader(stanza))
		for sc.Scan() {
			if k, v, ok := strings.Cut(sc.Text(), ": "); ok {
				f[k] = v
			}
		}
		if f["Package"] != "gh" || f["Architecture"] != "amd64" {
			continue
		}
		if !versionRE.MatchString(f["Version"]) {
			return "", errShape
		}
		if newest == "" || Newer(f["Version"], newest) {
			newest = f["Version"]
		}
	}
	if newest == "" {
		return "", &Error{"lists no gh for amd64"}
	}
	return newest, nil
}

// indexTypes are what a registry is asked for: the index of a tag, which versions.lock pins, or its
// one manifest where it has no index.
var indexTypes = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}, ", ")

// Digest is the digest a registry gives a tag now, by a HEAD of its manifest, which Docker Hub does
// not count as a pull. Docker Hub's images and mcr's are read; another registry gives an error.
func (s *Sources) Digest(ctx context.Context, name, tag string) (string, error) {
	if !imageRE.MatchString(name + ":" + tag + "@sha256:" + strings.Repeat("0", 64)) {
		return "", errShape
	}
	header := http.Header{"Accept": {indexTypes}}
	var addr string
	switch host, repo, ok := strings.Cut(name, "/"); {
	case ok && host == "mcr.microsoft.com":
		addr = s.MCR + "/v2/" + repo + "/manifests/" + tag
	case ok && strings.Contains(host, "."):
		return "", &Error{"is a registry the look does not read"}
	default:
		if !strings.Contains(name, "/") {
			name = "library/" + name
		}
		q := url.Values{"service": {"registry.docker.io"}, "scope": {"repository:" + name + ":pull"}}
		var t struct {
			Token string `json:"token"`
		}
		if err := s.json(ctx, s.DockerAuth+"/token?"+q.Encode(), &t); err != nil {
			return "", err
		}
		if len(t.Token) > 8<<10 || !tokenRE.MatchString(t.Token) {
			return "", errShape
		}
		header.Set("Authorization", "Bearer "+t.Token)
		addr = s.DockerHub + "/v2/" + name + "/manifests/" + tag
	}
	_, h, err := s.fetch(ctx, http.MethodHead, addr, header)
	if err != nil {
		return "", err
	}
	d := h.Get("Docker-Content-Digest")
	if !digestRE.MatchString(d) {
		return "", errShape
	}
	return d, nil
}

// Newer says version a is higher than b, part by part as numbers; false when either is not a version.
func Newer(a, b string) bool {
	if !versionRE.MatchString(a) || !versionRE.MatchString(b) {
		return false
	}
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(pa), len(pb)) {
		x, y := part(pa, i), part(pb, i)
		if x != y {
			return x > y
		}
	}
	return false
}

func part(p []string, i int) int {
	if i >= len(p) {
		return 0
	}
	n, err := strconv.Atoi(p[i])
	if err != nil {
		return -1
	}
	return n
}
