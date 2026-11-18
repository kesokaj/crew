package runs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/runspec"
)

// The CLI's own files of a run are stored sealed with the key file, for their conversation and path,
// and put back all or none (12.10; plan of 2026-10-26, D1 to D17).

const cliWord = "a-word-only-the-cli-file-holds"

// keptCLIFiles is a run that stopped after a turn that left the CLI's file, with its conversation and
// the file's path: the next continue in a new microVM takes way 2 when the file is put back.
func keptCLIFiles(t *testing.T, k *kit) (run, conv, file string, sub int64) {
	t.Helper()
	file = ".gemini/antigravity-cli/conversations/" + agyConv + ".db"
	k.play(harness.Script{Recording: agyRun, Hold: true, Exit: runspec.Exit{WIP: "abc"},
		Files: map[string][]byte{file: []byte(`{"say":"` + cliWord + `"}` + "\n")}})
	run = k.start(StartRequest{})
	k.wait(run, "working")
	k.d.Stop(context.Background(), run)
	v := k.wait(run, "ended")
	if err := k.db.Server.QueryRow(context.Background(), `SELECT conversation FROM runs WHERE id = $1`, run).Scan(&conv); err != nil {
		t.Fatal(err)
	}
	return run, conv, file, v.Subtask
}

// continueWay continues a subtask in a new microVM and gives the way the new run took.
func continueWay(t *testing.T, k *kit, sub int64) (string, harness.RunSpec) {
	t.Helper()
	next, err := k.d.Continue(context.Background(), sub, ContinueRequest{})
	if err != nil {
		t.Fatal(err)
	}
	n := k.wait(next, "idle")
	s := started(k)
	return n.Way, s[len(s)-1]
}

// E2E 1: no stored byte is the file or its gzip, the sum is of what is stored, and the next sandbox
// gets the same file back.
func TestTheCLIsFilesAreStoredSealed(t *testing.T) {
	k := newKit(t)
	_, conv, file, sub := keptCLIFiles(t, k)
	var stored []byte
	var sum string
	if err := k.db.Server.QueryRow(context.Background(), `SELECT gzip, sha256 FROM cli_files WHERE conversation = $1 AND path = $2`,
		conv, file).Scan(&stored, &sum); err != nil {
		t.Fatal(err)
	}
	if bytes.HasPrefix(stored, []byte{0x1f, 0x8b}) || bytes.Contains(stored, []byte(cliWord)) {
		t.Fatal("the CLI's file is stored as a gzip anyone can read")
	}
	if h := sha256.Sum256(stored); sum != hex.EncodeToString(h[:]) {
		t.Fatalf("the stored sum %s is not of the stored bytes", sum)
	}
	gz, err := k.vault.Open(cliFilePurpose(conv, file), stored)
	if err != nil {
		t.Fatalf("the stored file does not open for its conversation and path: %v", err)
	}
	if b, err := gunzip(gz); err != nil || !bytes.Contains(b, []byte(cliWord)) {
		t.Fatalf("opened: %q %v", b, err)
	}
	way, spec := continueWay(t, k, sub)
	if way != WayFiles || !bytes.Contains(spec.Files[file], []byte(cliWord)) {
		t.Fatalf("way %s, files %q", way, spec.Files)
	}
}

// failingSealer seals nothing.
type failingSealer struct{ Sealer }

func (failingSealer) Seal(string, []byte) ([]byte, error) { return nil, errors.New("the seal failed") }

// E2E 2: a turn whose files cannot be sealed stores none and removes the files of the turns before, so
// the next sandbox is rebuilt from the turns, not continued from an older state (D5, D11).
func TestATurnWhoseFilesCannotBeSealedLeavesNone(t *testing.T) {
	for name, seal := range map[string]func(k *kit) Sealer{
		"a seal that errors": func(k *kit) Sealer { return failingSealer{k.vault} },
		"no key file":        func(*kit) Sealer { return nil },
	} {
		t.Run(name, func(t *testing.T) {
			k := newKit(t)
			file := ".gemini/antigravity-cli/conversations/" + agyConv + ".db"
			k.play(harness.Script{Recording: agyRun, Files: map[string][]byte{file: []byte("turn 1\n")}})
			run := k.start(StartRequest{})
			v := k.wait(run, "idle")
			if n := k.count(`SELECT count(*) FROM cli_files`); n != 1 {
				t.Fatalf("%d files kept after the first turn", n)
			}
			k.d.Seal = seal(k)
			k.play(harness.Script{Recording: agyRun, Files: map[string][]byte{file: []byte("turn 2\n")}})
			if _, err := k.d.Continue(context.Background(), v.Subtask, ContinueRequest{Text: "Go on."}); err != nil {
				t.Fatal(err)
			}
			if v = k.wait(run, "idle"); v.Turn != 2 {
				t.Fatalf("turn %d", v.Turn)
			}
			if n := k.count(`SELECT count(*) FROM cli_files`); n != 0 {
				t.Fatalf("%d files of an older turn kept after a turn whose files were not stored", n)
			}
			k.d.Seal = k.vault
			k.d.Stop(context.Background(), run)
			k.wait(run, "ended")
			if way, _ := continueWay(t, k, v.Subtask); way != WayRebuilt {
				t.Fatalf("way %s", way)
			}
		})
	}
}

// E2E 3: files sealed with another key file are not put back, and the next sandbox is rebuilt; with no
// key file none is read, and a continue does not fail (D6).
func TestCLIFilesOfAnotherKeyAreNotPutBack(t *testing.T) {
	k := newKit(t)
	_, _, _, sub := keptCLIFiles(t, k)
	k.d.Seal = vaultOf(t, k) // the key file changed
	// The rebuilt turn is a new conversation of the CLI, whose file has another path.
	other := ".gemini/antigravity-cli/conversations/rebuilt.db"
	k.play(harness.Script{Recording: agyRun, Hold: true, Exit: runspec.Exit{WIP: "abc"}, Files: map[string][]byte{other: []byte("rebuilt\n")}})
	next, err := k.d.Continue(context.Background(), sub, ContinueRequest{})
	if err != nil {
		t.Fatal(err)
	}
	n := k.wait(next, "working")
	if s := started(k); n.Way != WayRebuilt || len(s[len(s)-1].Files) != 0 {
		t.Fatalf("way %s, %d files", n.Way, len(s[len(s)-1].Files))
	}
	k.d.Stop(context.Background(), next)
	v := k.wait(next, "ended")
	// Its file replaces the one that does not open, so the conversation is rebuilt once, not at every
	// continue until the old file expires (review of 6751b80, finding 1).
	if way, spec := continueWay(t, k, v.Subtask); way != WayFiles || string(spec.Files[other]) != "rebuilt\n" || len(spec.Files) != 1 {
		t.Fatalf("after the rebuilt turn: way %s, files %q", way, spec.Files)
	}
	k2 := newKit(t)
	_, _, _, sub2 := keptCLIFiles(t, k2)
	k2.d.Seal = nil
	if way, _ := continueWay(t, k2, sub2); way != WayRebuilt {
		t.Fatalf("with no key file: way %s", way)
	}
}

// E2E 4: a sealed file moved to another path or another conversation does not open, and the files are
// put back all or none (D10).
func TestASealedCLIFileOpensOnlyForItsConversationAndPath(t *testing.T) {
	k := newKit(t)
	ctx := context.Background()
	_, conv, file, _ := keptCLIFiles(t, k)
	if got, err := k.d.cliFiles(ctx, conv); err != nil || len(got) != 1 {
		t.Fatalf("its own: %d files, %v", len(got), err)
	}
	// A second file of the conversation, moved from the first's path.
	if _, err := k.db.Server.Exec(ctx, `INSERT INTO cli_files (conversation, path, gzip, sha256, keep_until)
		SELECT conversation, 'other.db', gzip, sha256, keep_until FROM cli_files WHERE conversation = $1 AND path = $2`, conv, file); err != nil {
		t.Fatal(err)
	}
	if got, err := k.d.cliFiles(ctx, conv); err != nil || len(got) != 0 {
		t.Fatalf("a file moved to another path: %d files put back, %v", len(got), err)
	}
	_, conv2, _, _ := keptCLIFiles(t, k)
	if _, err := k.db.Server.Exec(ctx, `UPDATE cli_files SET gzip = (SELECT gzip FROM cli_files WHERE conversation = $1 AND path = $2)
		WHERE conversation = $3`, conv, file, conv2); err != nil {
		t.Fatal(err)
	}
	if got, err := k.d.cliFiles(ctx, conv2); err != nil || len(got) != 0 {
		t.Fatalf("a file of another conversation: %d files put back, %v", len(got), err)
	}
	// One that opens and does not unzip puts none back either.
	_, conv3, file3, _ := keptCLIFiles(t, k)
	notGzip, err := k.vault.Seal(cliFilePurpose(conv3, file3), []byte("no gzip"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.db.Server.Exec(ctx, `UPDATE cli_files SET gzip = $2 WHERE conversation = $1`, conv3, notGzip); err != nil {
		t.Fatal(err)
	}
	if got, err := k.d.cliFiles(ctx, conv3); err != nil || len(got) != 0 {
		t.Fatalf("a file that does not unzip: %d files put back, %v", len(got), err)
	}
}

// A turn that ends well or for the operator and leaves no files, as one whose file could not be read,
// keeps the files of the turn before: only a turn that keeps files replaces them (review of 0d70b2b, finding 1).
func TestATurnWithNoFilesKeepsTheTurnBefores(t *testing.T) {
	k := newKit(t)
	file := ".gemini/antigravity-cli/conversations/" + agyConv + ".db"
	k.play(harness.Script{Recording: agyRun, Files: map[string][]byte{file: []byte("turn 1\n")}})
	run := k.start(StartRequest{})
	v := k.wait(run, "idle")
	k.play(harness.Script{Recording: agyRun})
	if _, err := k.d.Continue(context.Background(), v.Subtask, ContinueRequest{Text: "Go on."}); err != nil {
		t.Fatal(err)
	}
	if v = k.wait(run, "idle"); v.Turn != 2 {
		t.Fatalf("turn %d", v.Turn)
	}
	if n := k.count(`SELECT count(*) FROM cli_files`); n != 1 {
		t.Fatalf("%d files kept after a turn that left none", n)
	}
	k.d.Stop(context.Background(), run)
	k.wait(run, "ended")
	if way, spec := continueWay(t, k, v.Subtask); way != WayFiles || string(spec.Files[file]) != "turn 1\n" {
		t.Fatalf("way %s, files %q", way, spec.Files)
	}
}

// A turn that stops with a CLI error such as ReasonQuota writes step_type=17 into agy's conversation
// file, which agy replays on --conversation (BUG-0006): those files are not kept or put back via
// WayFiles, so the next microVM rebuilds from the turns and finishes cleanly.
func TestContinueAfterQuotaDoesNotReplayCLIError(t *testing.T) {
	k := newKit(t)
	ctx := context.Background()
	file := ".gemini/antigravity-cli/conversations/" + agyConv + ".db"
	tainted := []byte("step_type=17 AGY_ERROR RESOURCE_EXHAUSTED\n")

	// Turn 1 hits ReasonQuota and leaves an error-tainted conversation file.
	k.play(harness.Script{
		Recording: agyRun,
		Exit:      runspec.Exit{Reason: cli.ReasonQuota, Exit: 3, Detail: "agy reached a limit: Individual quota reached."},
		Files:     map[string][]byte{file: tainted},
	})
	run := k.start(StartRequest{})
	v := k.wait(run, "ended")
	if v.StopReason != cli.ReasonQuota {
		t.Fatalf("stop reason %s, want %s", v.StopReason, cli.ReasonQuota)
	}
	if n := k.count(`SELECT count(*) FROM cli_files WHERE conversation = $1`, v.Conversation); n != 0 {
		t.Fatalf("%d error-tainted cli_files kept after quota stop", n)
	}

	// Continuing the subtask in a new microVM falls back to WayRebuilt instead of restoring the
	// tainted .db file via WayFiles, and the resumed turn succeeds.
	way, spec := continueWay(t, k, v.Subtask)
	if way != WayRebuilt || spec.Continue != "" || len(spec.Files) != 0 || !strings.HasPrefix(spec.Prompt, "The conversation so far") {
		t.Fatalf("way %s, continue %q, files %q, prompt %q", way, spec.Continue, spec.Files, spec.Prompt)
	}

	// If an earlier turn stored clean cli_files and a later turn stops with ReasonQuota (or another
	// non-OK, non-Operator reason), the stored files are cleared and not restored on continue.
	k.play(harness.Script{
		Recording: agyRun,
		Files:     map[string][]byte{file: []byte("clean turn\n")},
	})
	run2 := k.start(StartRequest{})
	v2 := k.wait(run2, "idle")
	if n := k.count(`SELECT count(*) FROM cli_files WHERE conversation = $1`, v2.Conversation); n != 1 {
		t.Fatalf("%d files kept after clean turn, want 1", n)
	}
	k.play(harness.Script{
		Recording: agyRun,
		Exit:      runspec.Exit{Reason: cli.ReasonQuota, Exit: 3, Detail: "agy reached a limit: Individual quota reached."},
		Files:     map[string][]byte{file: tainted},
	})
	if _, err := k.d.Continue(ctx, v2.Subtask, ContinueRequest{Text: "Continue."}); err != nil {
		t.Fatal(err)
	}
	v2 = k.wait(run2, "ended")
	if v2.StopReason != cli.ReasonQuota {
		t.Fatalf("stop reason %s, want %s", v2.StopReason, cli.ReasonQuota)
	}
	if n := k.count(`SELECT count(*) FROM cli_files WHERE conversation = $1`, v2.Conversation); n != 0 {
		t.Fatalf("%d cli_files kept after second turn hit quota", n)
	}
	way2, spec2 := continueWay(t, k, v2.Subtask)
	if way2 != WayRebuilt || spec2.Continue != "" || len(spec2.Files) != 0 {
		t.Fatalf("after second turn quota: way %s, continue %q, files %q", way2, spec2.Continue, spec2.Files)
	}
}
