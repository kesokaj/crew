package githubapp

import "errors"

// What the no_github_token signal says of why GitHub gave a sandbox no token (21, 13.9). The improver
// reads the signal through list_signals and get_metrics. The error of the database, of the network, or
// GitHub's own message can name the server's addresses: the broker logs the error with the sandbox,
// and the signal holds none of it.
const (
	// NoTokenNotSetUp is the reason when Setup has no App, or its credential does not open.
	NoTokenNotSetUp = "the GitHub App is not set up"
	// NoTokenNotInstalled is the reason when the App is not installed on the repositories' owner.
	NoTokenNotInstalled = "the GitHub App is not installed on the owner of the project's repositories"
	// NoTokenOther is the reason for every other error.
	NoTokenOther = "GitHub gave no token; the error is in Crew's server log"
)

// NoTokenReason is the fixed sentence the signal keeps for err, an error of TokenFor.
func NoTokenReason(err error) string {
	switch {
	case errors.Is(err, ErrNotSetUp):
		return NoTokenNotSetUp
	case errors.Is(err, ErrNotInstalled):
		return NoTokenNotInstalled
	}
	return NoTokenOther
}
