package harness

import (
	"fmt"
	"os"
	"path"

	"github.com/kesokaj/crew/internal/runspec"
)

// clearTurn removes what a turn's folder holds of a turn's end before the turn starts: an exit.json,
// a spool or a standard error that an agent wrote there ahead would end the turn at once with its
// words, while crew-run and the CLI ran on unfollowed (12.4, since 1.01). A file that cannot be removed
// fails the turn's start. The removal stays inside the home (os.Root).
func clearTurn(home string, turn int) error {
	root, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, f := range []string{runspec.ExitFile, runspec.EventsFile, runspec.StderrFile} {
		if err := root.RemoveAll(path.Join(runspec.TurnDir(turn), f)); err != nil {
			return fmt.Errorf("clearing the turn's folder: %w", err)
		}
	}
	return nil
}
