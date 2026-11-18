package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Google asks Google whose an access token is: agy's adapter reads the account of its login with it (spike items 27 and 56): tokeninfo answers from
// Google's side, costs no model request, and cannot be fooled by an edited file.
type Google struct {
	HTTP     *http.Client
	Endpoint string // https://oauth2.googleapis.com/tokeninfo, or a test server
}

// TokenInfoURL is Google's tokeninfo. A test points it at a server of its own.
var TokenInfoURL = "https://oauth2.googleapis.com/tokeninfo"

// agyFile is the part of agy's token file that names its access token.
type agyFile struct {
	Token struct {
		AccessToken string `json:"access_token"`
	} `json:"token"`
}

// agyAccount returns the Google address an agy token file belongs to.
func (g Google) agyAccount(ctx context.Context, tokenFile []byte) (string, error) {
	var f agyFile
	if err := json.Unmarshal(tokenFile, &f); err != nil || f.Token.AccessToken == "" {
		return "", errors.New("agy's token file holds no access token")
	}
	return g.Email(ctx, f.Token.AccessToken)
}

// Email returns the verified address of the account an access token belongs to.
func (g Google) Email(ctx context.Context, accessToken string) (string, error) {
	ep := g.Endpoint
	if ep == "" {
		ep = TokenInfoURL
	}
	hc := g.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	// The token goes in the body, so no log of a URL holds it.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep, strings.NewReader(url.Values{"access_token": {accessToken}}.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("Google's tokeninfo: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Google's tokeninfo said %s", resp.Status)
	}
	var info struct {
		Email         string `json:"email"`
		EmailVerified string `json:"email_verified"`
	}
	if err := json.Unmarshal(body, &info); err != nil || info.Email == "" || info.EmailVerified != "true" {
		return "", errors.New("Google's tokeninfo named no verified address")
	}
	return strings.ToLower(info.Email), nil
}
