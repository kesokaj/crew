package github

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// The private copy of a release on GitHub (16.9): its tag, at the commit it was built from, and a
// release page with its notes. The images go to the registry with the same login (RegistryLogin).

var tagName = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)
var commitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Tag makes the tag at sha, or finds it there. A tag of that name at another commit is refused:
// a release is never moved.
func (c *Client) Tag(ctx context.Context, repo, tag, sha string) error {
	p, err := repoPath(repo)
	if err != nil {
		return err
	}
	if !tagName.MatchString(tag) || !commitSHA.MatchString(sha) {
		return &Error{Msg: "a release's tag is vX.Y.Z at a commit"}
	}
	code, err := c.call(ctx, http.MethodPost, p+"/git/refs", map[string]string{"ref": "refs/tags/" + tag, "sha": sha}, nil, http.StatusCreated)
	if err == nil || code != http.StatusUnprocessableEntity {
		return err
	}
	var ref struct {
		Object struct {
			SHA  string `json:"sha"`
			Type string `json:"type"`
		} `json:"object"`
	}
	if _, err := c.call(ctx, http.MethodGet, p+"/git/ref/tags/"+tag, nil, &ref, http.StatusOK); err != nil {
		return err
	}
	if ref.Object.SHA != sha {
		return &Error{Msg: "the tag " + tag + " is on GitHub at another commit: a release is never moved"}
	}
	return nil
}

// Release makes the release page of tag, or finds it, and returns its address.
func (c *Client) Release(ctx context.Context, repo, tag, name, body string) (string, error) {
	p, err := repoPath(repo)
	if err != nil {
		return "", err
	}
	var rel struct {
		HTMLURL string `json:"html_url"`
	}
	code, err := c.call(ctx, http.MethodGet, p+"/releases/tags/"+url.PathEscape(tag), nil, &rel, http.StatusOK)
	if err == nil {
		return rel.HTMLURL, nil
	}
	if code != http.StatusNotFound {
		return "", err
	}
	_, err = c.call(ctx, http.MethodPost, p+"/releases", map[string]any{"tag_name": tag, "name": name, "body": body,
		"draft": false, "prerelease": false}, &rel, http.StatusCreated)
	return rel.HTMLURL, err
}

// RegistryLogin is the account gh is signed in as and its token, for the registry of GitHub. The
// token is used for one push and kept nowhere (16.9).
func (c *Client) RegistryLogin(ctx context.Context) (user, token string, err error) {
	var me struct {
		Login string `json:"login"`
	}
	if _, err := c.call(ctx, http.MethodGet, "/user", nil, &me, http.StatusOK); err != nil {
		return "", "", err
	}
	tok, err := c.token(ctx)
	if err != nil {
		return "", "", err
	}
	if !RepoName.MatchString(me.Login) {
		return "", "", &Error{Msg: "gh's account has no login GitHub gave"}
	}
	return strings.ToLower(me.Login), tok, nil
}
