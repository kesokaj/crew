package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/kesokaj/crew/internal/release"
)

// Client asks the updater over its socket, as the server does (16.4, step 1).
type Client struct {
	// HTTP reaches the socket; NewClient makes one for a path.
	HTTP *http.Client
	// Base is the address the requests go to, http://updater on the socket.
	Base string
	// Keep reads updates.keep_versions, which every install ask names (D30); nil names none.
	Keep func(context.Context) (int, error)
}

// NewClient is a client of the socket at path.
func NewClient(path string) *Client {
	return &Client{Base: "http://updater", HTTP: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}}}
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) (int, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, r)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, fmt.Errorf("the updater does not answer: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("the updater's answer: %w", err)
		}
	}
	return resp.StatusCode, nil
}

// Status is where the updater is.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var st Status
	code, err := c.do(ctx, http.MethodGet, "/status", nil, &st)
	if err == nil && code != http.StatusOK {
		err = fmt.Errorf("the updater's status answered %d", code)
	}
	return st, err
}

// Install asks for a version. A refusal is a *Refusal; a request that runs already is ErrBusy.
func (c *Client) Install(ctx context.Context, a Ask) (*Request, error) {
	if a.Keep == 0 && c.Keep != nil {
		// A number that cannot be read, or one a set stored before its bound holds, is sent as
		// none: the updater keeps the number it has, and the install is not refused for it.
		if n, err := c.Keep(ctx); err == nil && n >= 2 && n <= 20 {
			a.Keep = n
		}
	}
	var out struct {
		Request
		Err     string   `json:"error"`
		Refused *Request `json:"request"`
	}
	code, err := c.do(ctx, http.MethodPost, "/install", a, &out)
	switch {
	case err != nil:
		return nil, err
	case code == http.StatusAccepted:
		r := out.Request
		return &r, nil
	case code == http.StatusConflict:
		return nil, ErrBusy
	case code == http.StatusUnprocessableEntity:
		return out.Refused, &Refusal{Reason: out.Err}
	}
	return nil, fmt.Errorf("the updater answered %d: %s", code, out.Err)
}

// Stop stops the request that runs, before its switch.
func (c *Client) Stop(ctx context.Context) error {
	var out struct {
		Err string `json:"error"`
	}
	code, err := c.do(ctx, http.MethodPost, "/stop", nil, &out)
	if err == nil && code != http.StatusOK {
		err = &Refusal{Reason: out.Err}
	}
	return err
}

// Load hands a build over to be loaded as a release (16.2 step 4). A refusal is a *Refusal; another
// load that runs is ErrBusy. Loading copies and hashes some GB, so it has its own time.
func (c *Client) Load(ctx context.Context, a LoadAsk) (*Loaded, error) {
	var out struct {
		Loaded
		Err string `json:"error"`
	}
	lc := *c
	if c.HTTP != nil {
		h := *c.HTTP
		h.Timeout = 30 * time.Minute
		lc.HTTP = &h
	}
	code, err := lc.do(ctx, http.MethodPost, "/load", a, &out)
	switch {
	case err != nil:
		return nil, err
	case code == http.StatusOK:
		l := out.Loaded
		return &l, nil
	case code == http.StatusConflict:
		return nil, ErrBusy
	case code == http.StatusUnprocessableEntity:
		return nil, &Refusal{Reason: out.Err}
	}
	return nil, fmt.Errorf("the updater answered %d: %s", code, out.Err)
}

// Gate asks for a gate's steps on a release (16.2 step 5): its request runs on, as an install's does.
// A refusal is a *Refusal; a request that runs already is ErrBusy.
func (c *Client) Gate(ctx context.Context, a Ask) (*Request, error) {
	var out struct {
		Request
		Err     string   `json:"error"`
		Refused *Request `json:"request"`
	}
	code, err := c.do(ctx, http.MethodPost, "/gate", a, &out)
	switch {
	case err != nil:
		return nil, err
	case code == http.StatusAccepted:
		r := out.Request
		return &r, nil
	case code == http.StatusConflict:
		return nil, ErrBusy
	case code == http.StatusUnprocessableEntity:
		return out.Refused, &Refusal{Reason: out.Err}
	}
	return nil, fmt.Errorf("the updater answered %d: %s", code, out.Err)
}

// Live sends the result of a gate's live steps, which the updater writes into its gate.json. A
// refusal is a *Refusal; a gate that does not wait for one is ErrNoPendingGate.
func (c *Client) Live(ctx context.Context, a LiveAsk) (*release.Gate, error) {
	var out struct {
		release.Gate
		Err string `json:"error"`
	}
	code, err := c.do(ctx, http.MethodPost, "/gate/live", a, &out)
	switch {
	case err != nil:
		return nil, err
	case code == http.StatusOK:
		g := out.Gate
		return &g, nil
	case code == http.StatusConflict:
		return nil, ErrNoPendingGate
	case code == http.StatusUnprocessableEntity:
		return nil, &Refusal{Reason: out.Err}
	}
	return nil, fmt.Errorf("the updater answered %d: %s", code, out.Err)
}
