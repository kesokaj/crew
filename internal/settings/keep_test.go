package settings_test

import (
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/settings"
)

// The keep settings are bounded (16.10, M17 phase 2): a release kept is never fewer than the running
// one and the one before it, nor more than 20 (D30), and no other keep time or count is 0 or less, so
// a typo cannot remove what retention keeps or stop the backups. The CLI's files are kept at most a
// year, so their keep time fits an interval (security review of 1be8ea7, finding 3).
// keep_trajectories_days may stay null.
func TestTheKeepSettingsAreBounded(t *testing.T) {
	defaults, _ := settings.Defaults(machine)
	refused := map[string]string{
		"updates.keep_versions: 1":                    "version: 1\nupdates: {keep_versions: 1}\n",
		"updates.keep_versions: 21":                   "version: 1\nupdates: {keep_versions: 21}\n",
		"updates.keep_versions: 3.0, no whole number": "version: 1\nupdates: {keep_versions: 3.0}\n",
		"updates.backup_keep: 0":                      "version: 1\nupdates: {backup_keep: 0}\n",
		"keep_cli_files_days: 0":                      "version: 1\nkeep_cli_files_days: 0\n",
		"conversations.keep_cli_files_days: -1":       "version: 1\nconversations: {keep_cli_files_days: -1}\n",
		"hosting.keep_images: 0":                      "version: 1\nhosting: {keep_images: 0}\n",
		"keep_trajectories_days: 0":                   "version: 1\nkeep_trajectories_days: 0\n",
		"keep_cli_files_days: 366":                    "version: 1\nkeep_cli_files_days: 366\n",
		"conversations.keep_cli_files_days: 400":      "version: 1\nconversations: {keep_cli_files_days: 400}\n",
	}
	for want, text := range refused {
		d, err := settings.Parse([]byte(text))
		if err != nil {
			t.Fatal(err)
		}
		err = settings.Validate(d, defaults)
		key := strings.SplitN(want, ":", 2)[0]
		if err == nil || !strings.Contains(err.Error(), key+": ") {
			t.Errorf("%s: %v", want, err)
		}
	}
	// The same through JSON, as the screens send it: 3.0 is no whole number (round 2 of the security
	// review of 1be8ea7).
	for _, text := range []string{`{"version": 1, "updates": {"keep_versions": 3.0}}`, `{"version": 1, "updates": {"keep_versions": 3e0}}`} {
		d, err := settings.ParseJSON([]byte(text))
		if err != nil {
			t.Fatal(err)
		}
		if err := settings.Validate(d, defaults); err == nil || !strings.Contains(err.Error(), "updates.keep_versions: must be a whole number") {
			t.Errorf("%s: %v", text, err)
		}
	}
	for _, text := range []string{
		"version: 1\nupdates: {keep_versions: 2, backup_keep: 1}\n",
		"version: 1\nupdates: {keep_versions: 20}\nkeep_cli_files_days: 1\nconversations: {keep_cli_files_days: 1}\nhosting: {keep_images: 1}\n",
		"version: 1\nkeep_trajectories_days: null\n",
		"version: 1\nkeep_trajectories_days: 365\n",
		"version: 1\nkeep_cli_files_days: 365\nconversations: {keep_cli_files_days: 365}\n",
	} {
		d, _ := settings.Parse([]byte(text))
		if err := settings.Validate(d, defaults); err != nil {
			t.Errorf("%q refused: %v", text, err)
		}
	}
}

// A session lasts at most the ingress's 30 days, and a fresh sign-in is at most 15 minutes old (20.3):
// the keys only shorten them.
func TestTheSessionKeysAreBounded(t *testing.T) {
	defaults, _ := settings.Defaults(machine)
	for text, ok := range map[string]bool{
		"sessions: {days: 0}": false, "sessions: {days: 31}": false, "sessions: {days: 1}": true, "sessions: {days: 30}": true,
		"sessions: {fresh_minutes: 0}": false, "sessions: {fresh_minutes: 16}": false, "sessions: {fresh_minutes: 5}": true,
	} {
		d, err := settings.Parse([]byte("version: 1\n" + text + "\n"))
		if err != nil {
			t.Fatal(err)
		}
		if err := settings.Validate(d, defaults); (err == nil) != ok {
			t.Errorf("%s: %v", text, err)
		}
	}
}
