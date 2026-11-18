package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// A pull request per ticket (7.12): Crew opens it when the ticket leaves In progress, mirrors its
// records to it as comments and statuses, and never asks GitHub to merge: it pushes main, and GitHub
// marks the pull request merged by itself. Each call uses the gh login from Setup.

// RepoOf is owner/name of a repository by its SSH address, or "" for another address.
func RepoOf(remote string) string {
	if !SSHURL.MatchString(remote) {
		return ""
	}
	return strings.TrimSuffix(strings.TrimPrefix(remote, "git@github.com:"), ".git")
}

// Pull is a pull request as Crew keeps it.
type Pull struct {
	Number int    `json:"number"`
	URL    string `json:"html_url"`
	State  string `json:"state"` // open or closed
	Merged bool   `json:"merged"`
	Head   struct {
		SHA string `json:"sha"`
	} `json:"head"`
	// MergedBy is who merged it, when someone did.
	MergedBy struct {
		Login string `json:"login"`
	} `json:"merged_by"`
}

// statusContext is what Crew's statuses are named, with the check after it.
var statusContext = regexp.MustCompile(`^crew/[a-z0-9_ -]{1,40}$`)

// call sends one request to GitHub's API with gh's token, and decodes the answer into out when it is
// one of ok.
func (c *Client) call(ctx context.Context, method, path string, body any, out any, ok ...int) (int, error) {
	tok, err := c.token(ctx)
	if err != nil {
		return 0, err
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.api()+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return 0, &Error{Msg: "GitHub cannot be reached: " + strings.ReplaceAll(err.Error(), tok, "…")}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	for _, code := range ok {
		if resp.StatusCode == code {
			if out != nil && len(b) > 0 {
				if err := json.Unmarshal(b, out); err != nil {
					return resp.StatusCode, fmt.Errorf("GitHub's answer does not read: %w", err)
				}
			}
			return resp.StatusCode, nil
		}
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return resp.StatusCode, &Error{Msg: "GitHub refused gh's login (HTTP 401): sign gh in again in Setup"}
	}
	var e struct {
		Message string `json:"message"`
	}
	json.Unmarshal(b, &e)
	return resp.StatusCode, &Error{Msg: fmt.Sprintf("GitHub refused %s %s (HTTP %d): %s", method, path, resp.StatusCode, oneLine(e.Message, 200))}
}

func repoPath(repo string) (string, error) {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || !RepoName.MatchString(parts[0]) || !RepoName.MatchString(parts[1]) {
		return "", &Error{Msg: fmt.Sprintf("%q is not a repository on GitHub", repo)}
	}
	return "/repos/" + parts[0] + "/" + parts[1], nil
}

// OpenPull opens a pull request of branch into base, or finds the one that is open for it already.
func (c *Client) OpenPull(ctx context.Context, repo, branch, base, title, body string) (Pull, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return Pull{}, err
	}
	var p Pull
	code, err := c.call(ctx, http.MethodPost, rp+"/pulls", map[string]any{"title": oneLine(title, 250), "head": branch, "base": base,
		"body": body, "maintainer_can_modify": false}, &p, http.StatusCreated, http.StatusUnprocessableEntity)
	if err != nil {
		return Pull{}, err
	}
	if code == http.StatusCreated {
		return p, nil
	}
	// 422: one is open for the branch already, from an earlier try whose answer was lost.
	owner := strings.Split(repo, "/")[0]
	var list []Pull
	if _, err := c.call(ctx, http.MethodGet, rp+"/pulls?state=open&head="+url.QueryEscape(owner+":"+branch), nil, &list, http.StatusOK); err != nil {
		return Pull{}, err
	}
	if len(list) == 0 {
		return Pull{}, &Error{Msg: "GitHub would not open a pull request of " + branch + " and has none open for it"}
	}
	return list[0], nil
}

// GetPull reads a pull request.
func (c *Client) GetPull(ctx context.Context, repo string, number int) (Pull, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return Pull{}, err
	}
	var p Pull
	_, err = c.call(ctx, http.MethodGet, fmt.Sprintf("%s/pulls/%d", rp, number), nil, &p, http.StatusOK)
	return p, err
}

// Comment adds a comment to a pull request.
func (c *Client) Comment(ctx context.Context, repo string, number int, body string) error {
	rp, err := repoPath(repo)
	if err != nil {
		return err
	}
	if len(body) > 60000 {
		body = body[:60000] + "\n\n…"
	}
	_, err = c.call(ctx, http.MethodPost, fmt.Sprintf("%s/issues/%d/comments", rp, number), map[string]any{"body": body}, nil, http.StatusCreated)
	return err
}

// Status sets a commit status of Crew's: state is success, failure, error, or pending.
func (c *Client) Status(ctx context.Context, repo, sha, state, name, description string) error {
	rp, err := repoPath(repo)
	if err != nil {
		return err
	}
	if !statusContext.MatchString(name) {
		return &Error{Msg: fmt.Sprintf("%q is not a status of Crew's", name)}
	}
	switch state {
	case "success", "failure", "error", "pending":
	default:
		return &Error{Msg: "a status is success, failure, error, or pending"}
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(sha) {
		return &Error{Msg: "a status is about a commit"}
	}
	_, err = c.call(ctx, http.MethodPost, rp+"/statuses/"+sha, map[string]any{"state": state, "context": name,
		"description": oneLine(description, 140)}, nil, http.StatusCreated)
	return err
}

// ClosePull closes a pull request without merging it: its ticket was closed.
func (c *Client) ClosePull(ctx context.Context, repo string, number int) error {
	rp, err := repoPath(repo)
	if err != nil {
		return err
	}
	_, err = c.call(ctx, http.MethodPatch, fmt.Sprintf("%s/pulls/%d", rp, number), map[string]any{"state": "closed"}, nil, http.StatusOK)
	return err
}

// SignedIn says gh is signed in in Setup: the token can be read. It asks GitHub nothing.
func (c *Client) SignedIn(ctx context.Context) bool {
	_, err := c.token(ctx)
	return err == nil
}
