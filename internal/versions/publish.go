package versions

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/docker/docker/api/types/image"
	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/release"
	"github.com/kesokaj/crew/internal/settings"
)

// The private copy of a release on GitHub (16.9): the tag vX.Y.Z at the commit it was built from, a
// release page with its notes and the gate's report, and its images in the repository's registry
// under the same version. The copy is not how an update arrives; a release installs whether it is
// copied or not, and what is not yet copied is tried again.

// GitHub is what publishing needs of GitHub, with the gh login of Setup.
type GitHub interface {
	Tag(ctx context.Context, repo, tag, sha string) error
	Release(ctx context.Context, repo, tag, name, body string) (string, error)
	RegistryLogin(ctx context.Context) (user, token string, err error)
}

// Registry pushes a local image to a registry under another name, with a login used for that push.
type Registry interface {
	Push(ctx context.Context, local, remote, user, token string) error
}

// RegistryHost is GitHub's container registry.
const RegistryHost = "ghcr.io"

// published is the images of a release that are copied: every one but PostgreSQL's, which is
// PostgreSQL's own.
var published = []string{"crew-server", "crew-caddy", "crew-runtime", "crew-updater"}

var releaseNumber = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// Copy is the published column: what is on GitHub, each with the time it got there.
type Copy struct {
	Tag    *time.Time        `json:"tag,omitempty"`
	Page   string            `json:"page,omitempty"`
	Images map[string]string `json:"images,omitempty"`
	Done   *time.Time        `json:"done,omitempty"`
}

// Publish copies each release that passed the gate and is not yet on GitHub, when its next try is
// due (16.9). A release whose copy fails is tried again later, longer apart.
func (s *Service) Publish(ctx context.Context) error {
	repo, on, err := s.publishing(ctx)
	if err != nil || !on {
		return err
	}
	rows, err := s.Pool.Query(ctx, `SELECT version, commit, published, gate FROM releases
		WHERE gate IS NOT NULL AND state <> 'removed' AND published->>'done' IS NULL AND (publish_next IS NULL OR publish_next <= $1)
		ORDER BY found_at`, s.now())
	if err != nil {
		return err
	}
	type todo struct {
		version, commit string
		copy            Copy
		gate            release.Gate
	}
	var list []todo
	for rows.Next() {
		var t todo
		if err := rows.Scan(&t.version, &t.commit, &t.copy, &t.gate); err != nil {
			rows.Close()
			return err
		}
		if releaseNumber.MatchString(t.version) {
			list = append(list, t)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, t := range list {
		c := t.copy
		err := s.publish(ctx, repo, t.version, t.commit, t.gate, &c)
		if err := s.recordCopy(ctx, t.version, c, err); err != nil {
			return err
		}
	}
	return nil
}

// publishing says whether releases are copied, and to which repository: updates.publish is github and
// there is a GitHub and a registry to copy to. The Versions screen says the same (16.9).
func (s *Service) publishing(ctx context.Context) (string, bool, error) {
	if s.GitHub == nil || s.Registry == nil {
		return "", false, nil
	}
	d, err := s.Settings(ctx)
	if err != nil {
		return "", false, err
	}
	u, _ := d["updates"].(settings.Doc)
	if p, _ := u["publish"].(string); p != "github" {
		return "", false, nil
	}
	repo, _ := u["source_repo"].(string)
	return repo, true, nil
}

// publish makes what is missing of the copy, in order, recording each part in c as it is made.
func (s *Service) publish(ctx context.Context, repo, v, commit string, gate release.Gate, c *Copy) error {
	owner, _, ok := strings.Cut(repo, "/")
	if !ok || strings.Contains(repo, "<") {
		return errors.New("updates.source_repo is not set, so Crew does not know its repository on GitHub")
	}
	tag := "v" + v
	if c.Tag == nil {
		if err := s.GitHub.Tag(ctx, repo, tag, commit); err != nil {
			return fmt.Errorf("the tag: %w", err)
		}
		at := s.now()
		c.Tag = &at
	}
	if c.Page == "" {
		notes, _ := s.notes(v)
		page, err := s.GitHub.Release(ctx, repo, tag, "Crew "+v, publishedNotes(notes)+"\n"+gateReport(gate))
		if err != nil {
			return fmt.Errorf("the release page: %w", err)
		}
		c.Page = page
	}
	if c.Images == nil {
		c.Images = map[string]string{}
	}
	var user, token string
	for _, name := range published {
		if c.Images[name] != "" {
			continue
		}
		if token == "" {
			var err error
			if user, token, err = s.GitHub.RegistryLogin(ctx); err != nil {
				return fmt.Errorf("the registry's login: %w", err)
			}
		}
		remote := fmt.Sprintf("%s/%s/%s:%s", RegistryHost, strings.ToLower(owner), name, v)
		if err := s.Registry.Push(ctx, name+":"+v, remote, user, token); err != nil {
			return fmt.Errorf("the image %s: %w", name, err)
		}
		c.Images[name] = remote
	}
	at := s.now()
	c.Done = &at
	return nil
}

// gateReport is the gate's steps, for the release page: its own steps, the install test, and the live
// part with what it skipped and what it held. Each text is a step's that agents' code may have
// printed, so it is one plain line with Markdown's characters escaped, and a step's detail is not
// published: the gate.json on the machine keeps it (security review of M16 P4).
func gateReport(g release.Gate) string {
	// A gate a server before 0.53 recorded is published without its amounts (since 0.54).
	g = *g.Unpriced()
	var b strings.Builder
	b.WriteString("## The gate\n\n")
	step := func(st release.GateStep, notRun bool) {
		mark := "passed"
		switch {
		case !st.OK && notRun:
			mark = "not run"
		case !st.OK:
			mark = "failed"
		}
		fmt.Fprintf(&b, "- %s: %s\n", markdownText(st.Name), mark)
	}
	for _, st := range g.Steps {
		step(st, false)
	}
	fmt.Fprintf(&b, "- The install test on a fresh virtual machine: %s\n", markdownText(g.InstallTest))
	if g.Live != "" {
		fmt.Fprintf(&b, "- The live steps: %s\n", markdownText(g.Live))
		for _, st := range g.LiveSteps {
			step(st, g.Live == release.LiveSkipped)
		}
	}
	for _, n := range g.Skipped {
		fmt.Fprintf(&b, "- Not run: %s\n", markdownText(n))
	}
	if g.Hold != "" {
		fmt.Fprintf(&b, "- Held: %s\n", markdownText(g.Hold))
	}
	return b.String()
}

// publishedNotes are a release's notes as its GitHub page shows them: each line as text, its heading
// and its list marks kept, since a ticket's title or a commit's subject, which agents and the operator
// wrote, would render a link, an image, a mention, a reference, or HTML (security review of 95d116d).
// notes.md on the machine stays plain.
func publishedNotes(notes string) string {
	lines := strings.Split(notes, "\n")
	for i, l := range lines {
		switch {
		case strings.TrimSpace(l) == "":
		case strings.HasPrefix(l, "# "):
			lines[i] = "# " + markdownEscape(oneLine(l[2:]))
		case strings.HasPrefix(l, "- "):
			lines[i] = "- " + markdownEscape(oneLine(l[2:]))
		default:
			lines[i] = markdownEscape(oneLine(l))
		}
	}
	return strings.Join(lines, "\n")
}

// markdownText is a text as one plain line of at most 120 characters in which Markdown and HTML read
// nothing but text, and GitHub no mention and no link.
func markdownText(t string) string { return markdownEscape(plain(t)) }

// oneLine is a line without control, invisible, or direction-changing characters, of any length: a
// line of the notes keeps its end, the commit's hash (round 2 of the reviews of 95d116d).
func oneLine(t string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r != ' ' && (unicode.Is(unicode.C, r) || unicode.Is(unicode.Z, r)) {
			return -1
		}
		return r
	}, t))
}

// markdownEscape escapes every character Markdown, HTML, or an entity reads; an @ gets a zero-width
// space, so GitHub makes no mention of it.
func markdownEscape(t string) string {
	var b strings.Builder
	for _, r := range t {
		if r == '@' {
			// A zero-width space after it: no mention, escaped or not.
			b.WriteString("@\u200b")
			continue
		}
		if strings.ContainsRune("\\`*_{}[]()<>#+-.!|~:&", r) {
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// recordCopy writes what was copied, and when the next try is due after a failure.
func (s *Service) recordCopy(ctx context.Context, v string, c Copy, failure error) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if failure == nil {
			if _, err := tx.Exec(ctx, `UPDATE releases SET published = $2, publish_error = NULL, publish_next = NULL WHERE version = $1`, v, c); err != nil {
				return err
			}
			return db.RecordChange(ctx, tx, "crew", "releases", v, nil, map[string]any{"published": c.Page, "images": c.Images})
		}
		var tries int
		if err := tx.QueryRow(ctx, `SELECT publish_tries FROM releases WHERE version = $1`, v).Scan(&tries); err != nil {
			return err
		}
		wait := min(5*time.Minute<<min(tries, 7), 6*time.Hour)
		_, err := tx.Exec(ctx, `UPDATE releases SET published = $2, publish_error = $3, publish_tries = publish_tries + 1, publish_next = $4 WHERE version = $1`,
			v, c, failure.Error(), s.now().Add(wait))
		return err
	})
}

// pushDocker is what pushing an image needs of Docker.
type pushDocker interface {
	ImageTag(ctx context.Context, source, target string) error
	ImagePush(ctx context.Context, image string, options image.PushOptions) (io.ReadCloser, error)
	ImageRemove(ctx context.Context, imageID string, options image.RemoveOptions) ([]image.DeleteResponse, error)
}

// DockerRegistry pushes through the server's Docker, with a login given for the one push and
// written nowhere: no Docker config holds it (16.9).
type DockerRegistry struct{ Docker pushDocker }

// Push tags local as remote, pushes it, and removes the remote name again.
func (r DockerRegistry) Push(ctx context.Context, local, remote, user, token string) error {
	auth, err := json.Marshal(map[string]string{"username": user, "password": token, "serveraddress": RegistryHost})
	if err != nil {
		return err
	}
	if err := r.Docker.ImageTag(ctx, local, remote); err != nil {
		return err
	}
	defer r.Docker.ImageRemove(context.WithoutCancel(ctx), remote, image.RemoveOptions{}) //nolint:errcheck // the local name stays
	rc, err := r.Docker.ImagePush(ctx, remote, image.PushOptions{RegistryAuth: base64.URLEncoding.EncodeToString(auth)})
	if err != nil {
		return redact(err, token)
	}
	defer rc.Close()
	// The push answers with a stream of messages; a failure is one of them, not the status.
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var m struct {
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if json.Unmarshal(sc.Bytes(), &m) == nil && (m.Error != "" || m.ErrorDetail.Message != "") {
			msg := m.ErrorDetail.Message
			if msg == "" {
				msg = m.Error
			}
			return redact(errors.New(msg), token)
		}
	}
	return redact(sc.Err(), token)
}

func redact(err error, token string) error {
	if err == nil || token == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), token, "…"))
}
