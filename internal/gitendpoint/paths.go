package gitendpoint

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kesokaj/crew/internal/cli"
)

// PathRule is what a push to one ref may change (13.8, 7.11): every path its commits change, measured
// from where the ref leaves Base, is one Allow names, and one Touches names when Touches is set. A file
// that instructs agents, for any CLI, is allowed only when a pattern of Allow other than Everything
// names it: a ticket's may_change lists it, or the push is refused.
type PathRule struct {
	// Base is the ref the change is measured from: main for a ticket's branch, the ticket's branch
	// for a subtask's.
	Base string `json:"base"`
	// Allow are patterns as cli.Below reads them: a name with no slash at any depth, a folder that
	// ends with a slash at the root, a path, or Everything.
	Allow []string `json:"allow"`
	// Touches are the files or folders a parallel subtask said it changes (7.11), in the same form.
	Touches []string `json:"touches,omitempty"`
}

// Everything allows every file that does not instruct agents: the may_change a ticket has unless its
// analysis says otherwise.
const Everything = "**"

// Refused says why a changed path may not be pushed under the rule, or "" when it may. instructs says
// the path instructs agents.
func (r PathRule) Refused(path string, instructs bool) string {
	if len(r.Touches) > 0 && !cli.Below(path, r.Touches) {
		return path + " is outside the files this subtask said it touches (" + strings.Join(r.Touches, ", ") + ")"
	}
	named := slices.DeleteFunc(slices.Clone(r.Allow), func(p string) bool { return p == Everything })
	switch {
	case instructs && !cli.Below(path, named):
		return path + " instructs agents, and the ticket's may_change does not name it (13.8)"
	case !instructs && !slices.Contains(r.Allow, Everything) && !cli.Below(path, named):
		return path + " is outside the ticket's may_change (" + strings.Join(r.Allow, ", ") + ")"
	}
	return ""
}

// The environment that carries the path rules of a request to the hook, and the program that tests
// them: the server itself, as `crew git-pre-receive`.
const (
	envPaths = "CREW_PUSH_PATHS"
	envHook  = "CREW_HOOK"
	// HookCommand is the argument a program is started with to test a push's paths.
	HookCommand = "git-pre-receive"
)

// The most of git's answer the hook reads (since 1.04): the files a push changes list in at most
// MaxListBytes, as a tree's listing does, and an index file, of which only what it names is read, is
// at most MaxIndexBytes, the bound the server's guards read one within too since 1.07. Past either, or
// when an index file cannot be read, the push is refused: an index file read as one that names nothing
// would let what it names in by itself (13.8).
const MaxIndexBytes = cli.MaxIndexBytes

var (
	errPushList     = fmt.Errorf("the files this push changes list longer than %d bytes", MaxListBytes)
	errIndexFile    = fmt.Errorf("an index file of this push is longer than %d bytes", MaxIndexBytes)
	errIndexRead    = errors.New("an index file of this push could not be read")
	errIndexLink    = errors.New("an index file, of this push or of main, is reached through more links than Crew follows")
	errIndexOutside = errors.New("an index file, of this push or of main, names a folder through /proc or /dev")
	errIndexParse   = errors.New("an index file, of this push or of main, is not JSON Crew reads: an object whose entries and inherits are lists of objects with a path, each key once, in its own case, and with no key Crew does not read")
	errIndexEntries = fmt.Errorf("an index file, of this push or of main, names more than %d folders, or a folder longer than %d bytes", cli.MaxIndexEntries, cli.MaxEntryBytes)
	errLinkedWay    = fmt.Errorf("a file or a folder that instructs agents, of this push or of main, is reached through a link out of the workspace, or through more links than Crew follows (at most %d)", cli.MaxLinked)
	errLinkList     = fmt.Errorf("a tree of this push or of main lists longer than %d bytes", MaxListBytes)
	errGitAnswer    = errors.New("git's answer is longer than the hook reads")
)

// hookBounds are the bounds of the hook's reads; tests make them small.
type hookBounds struct{ list, index int64 }

// emptyTree is git's tree with nothing in it: what a ref that shares no history with its base is
// measured from.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// PreReceive is the part of the endpoint's hook that tests what a push changes (13.8): it reads the
// hook's lines, and for each ref with a rule in CREW_PUSH_PATHS lists the paths its new commit
// changes from where it leaves the rule's base, as git's quarantine holds them, and refuses the push
// when one is not allowed. It runs in the repository, with the hook's environment, and returns the
// hook's exit status.
func PreReceive(stdin io.Reader, stderr io.Writer) int {
	var rules map[string]PathRule
	if err := json.Unmarshal([]byte(os.Getenv(envPaths)), &rules); err != nil {
		fmt.Fprintln(stderr, "crew: the rules for the files of this push do not read")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	sc := bufio.NewScanner(stdin)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 3 {
			continue
		}
		newID, ref := f[1], f[2]
		rule, ok := rules[ref]
		if !ok {
			continue
		}
		refused, err := testPush(ctx, rule, newID, hookBounds{list: MaxListBytes, index: MaxIndexBytes})
		if err != nil {
			fmt.Fprintf(stderr, "crew: the files of this push could not be read: %v\n", err)
			return 1
		}
		if len(refused) > 0 {
			if len(refused) > 10 {
				refused = append(refused[:10], fmt.Sprintf("and %d more", len(refused)-10))
			}
			fmt.Fprintf(stderr, "crew: this push to %s changes files it may not:\n  %s\n", ref, strings.Join(refused, "\n  "))
			return 1
		}
	}
	return 0
}

// testPush lists what the commit changes from where it leaves the rule's base, and the refusals.
func testPush(ctx context.Context, rule PathRule, commit string, max hookBounds) ([]string, error) {
	git := func(limit int64, args ...string) ([]byte, error) {
		return hookGit(ctx, limit, args...)
	}
	// A subtask's branch is measured from its ticket's branch, or from main while that is not pushed.
	base := emptyTree
	for _, from := range []string{rule.Base, "refs/heads/main"} {
		if out, err := git(4096, "merge-base", "--", commit, from); err == nil && len(bytes.TrimSpace(out)) > 0 {
			base = string(bytes.TrimSpace(out))
			break
		}
	}
	out, err := git(max.list, "diff", "--name-only", "--no-renames", "-z", base, commit, "--")
	if errors.Is(err, errGitAnswer) {
		return nil, errPushList
	}
	if err != nil {
		return nil, fmt.Errorf("git diff: %w", err)
	}
	var ierr error
	var pushedLinks []string
	read := func(rev string) func(string) ([]byte, cli.Way) {
		return func(path string) ([]byte, cli.Way) {
			if rev == emptyTree || ierr != nil {
				return nil, cli.Way{}
			}
			b, way, err := indexFile(git, max.index, rev, path)
			ierr = cmp.Or(ierr, err)
			if rev == commit {
				pushedLinks = append(pushedLinks, way.Links...)
			}
			return b, way
		}
	}
	// What an index file names is an instruction too, on either side of the change and on main as it
	// is now, which may have moved on since the branch left it (13.8), and so is every path of its way
	// through a link (since 1.05). One that cannot be read refuses the push, never names nothing
	// (since 1.04).
	_, namedNew, nerr := cli.Indexed(read(commit))
	ierr = cmp.Or(ierr, nerr)
	_, namedBase, nerr := cli.Indexed(read(base))
	ierr = cmp.Or(ierr, nerr)
	// main as it is now: a repository with no main has none to read, and a git that cannot say is an
	// error, never a main with no index files.
	heads, err := git(4096, "for-each-ref", "--format=%(objectname)", "refs/heads/main")
	if err != nil {
		return nil, errIndexRead
	}
	var namedMain []string
	main := string(bytes.TrimSpace(heads))
	if main != "" {
		_, namedMain, nerr = cli.Indexed(read(main))
		ierr = cmp.Or(ierr, nerr)
	}
	if errors.Is(ierr, cli.ErrNamedOutside) {
		return nil, errIndexOutside
	}
	// Past the entries Crew reads, as past an index file's bytes, never what it named so far (since 1.07).
	if errors.Is(ierr, cli.ErrIndexEntries) {
		return nil, errIndexEntries
	}
	// Nor one the CLI's reader may read otherwise than Go's (after its security review).
	if errors.Is(ierr, cli.ErrIndexParse) {
		return nil, errIndexParse
	}
	if ierr != nil {
		return nil, ierr
	}
	instr := append(append(append(cli.InstructionFiles(), namedNew...), namedBase...), namedMain...)
	// A link that stands for a file or a folder that instructs, of either side or of main, is followed
	// within the tree, and what it leads to instructs too (since 1.06).
	var linked []string
	newLinks := map[string]bool{}
	var sides []string
	for _, rev := range []string{commit, base, main} {
		if rev == "" || rev == emptyTree || slices.Contains(sides, rev) {
			continue
		}
		sides = append(sides, rev)
		listing, err := hookListing(git, max.list, rev)
		if err != nil {
			return nil, err
		}
		l, links, err := cli.LinkedTree(listing, instr, func(object string) (string, error) {
			b, err := git(cli.MaxLinkText, "cat-file", "blob", object)
			if err != nil {
				return "", errIndexRead
			}
			return string(b), nil
		})
		if errors.Is(err, cli.ErrLinkWay) {
			return nil, errLinkedWay
		}
		if err != nil {
			return nil, err
		}
		linked = append(linked, l...)
		for _, p := range links {
			if rev == commit {
				newLinks[p] = true
			}
		}
	}
	instr = append(instr, linked...)
	match := cli.NewMatcher(instr)
	var refused []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p == "" {
			continue
		}
		// A link on the way to an index file is made or changed by no push, whatever may_change says:
		// what it leads to would be read by the CLI as the index file (since 1.05).
		if slices.Contains(pushedLinks, p) {
			refused = append(refused, p+" is a link on the way to an index file, which no push changes (13.8)")
			continue
		}
		// Nor a link at, above or below a file or a folder that instructs: what it leads to would be read
		// by the CLI in its place (since 1.06).
		if newLinks[p] && match.Matches(p) {
			refused = append(refused, p+" is a link where a file or a folder that instructs agents is, which no push makes or changes (13.8)")
			continue
		}
		if why := rule.Refused(p, match.Matches(p)); why != "" {
			refused = append(refused, why)
		}
	}
	return refused, nil
}

// indexFile reads an index file of rev as the CLI opens it, its way through links followed within the
// tree (cli.Follow, since 1.05): nil when it leads to no file, its size from the listing before it is
// read, and an error past max, past the way's bounds, or when git cannot read it.
func indexFile(git func(int64, ...string) ([]byte, error), max int64, rev, path string) ([]byte, cli.Way, error) {
	way, err := hookFollow(git, rev, path)
	if errors.Is(err, cli.ErrLinkWay) {
		return nil, way, errIndexLink
	}
	if err != nil || way.File == "" {
		return nil, way, err
	}
	if way.Entry.Size > max {
		return nil, way, errIndexFile
	}
	b, err := git(max, "cat-file", "blob", way.Entry.Object)
	if err != nil {
		return nil, way, errIndexRead
	}
	return b, way, nil
}

// hookFollow reads a path of rev as the CLI opens it (cli.Follow).
func hookFollow(git func(int64, ...string) ([]byte, error), rev, path string) (cli.Way, error) {
	lookup := func(p string) (cli.Entry, error) {
		entry, err := git(MaxPathBytes+256, "--literal-pathspecs", "ls-tree", "-l", "-z", rev, "--", p)
		if err != nil {
			return cli.Entry{}, errIndexRead
		}
		meta, listed, ok := bytes.Cut(entry, []byte("\t"))
		f := strings.Fields(string(meta))
		// Only the entry of the path itself: git lists a folder's files for "dir/".
		if !ok || len(f) != 4 || string(bytes.TrimSuffix(listed, []byte("\x00"))) != p {
			return cli.Entry{}, nil
		}
		size, _ := strconv.ParseInt(f[3], 10, 64) // "-" for a folder or a submodule
		return cli.Entry{Mode: f[0], Object: f[2], Size: size}, nil
	}
	linkText := func(e cli.Entry) (string, error) {
		b, err := git(cli.MaxLinkText, "cat-file", "blob", e.Object)
		if err != nil {
			return "", errIndexRead
		}
		return string(b), nil
	}
	return cli.Follow(path, lookup, linkText)
}

// hookListing is rev's tree as cli.LinkedTree reads it, at most max bytes: the tree is listed once, and
// every lookup of a way through its links is read from the listing (since 1.06).
func hookListing(git func(int64, ...string) ([]byte, error), max int64, rev string) ([]byte, error) {
	out, err := git(max, "ls-tree", "-r", "-t", "-l", "-z", "--full-tree", rev)
	if errors.Is(err, errGitAnswer) {
		return nil, errLinkList
	}
	if err != nil {
		return nil, errIndexRead
	}
	return out, nil
}

// hookGit runs git in the hook's repository and reads at most limit bytes of its answer: past it, git
// is stopped and the answer is errGitAnswer. The hook's environment holds git's quarantine, where the
// pushed objects are until it ends; of git's error side, the start is kept.
func hookGit(ctx context.Context, limit int64, args ...string) ([]byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	errText := &LimitedText{Max: 64 << 10}
	cmd.Stderr = errText
	cmd.WaitDelay = 5 * time.Second
	r, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	out, rerr := io.ReadAll(io.LimitReader(r, limit+1))
	if int64(len(out)) > limit {
		cancel()
		cmd.Wait()
		return nil, errGitAnswer
	}
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("%v: %s", err, bytes.TrimSpace(errText.Bytes()))
	}
	return out, rerr
}

// LimitedText keeps the first Max bytes written to it and takes the rest without keeping it, so a
// command is never stopped by a full pipe of what it says on its error side.
type LimitedText struct {
	Max int
	buf bytes.Buffer
}

func (l *LimitedText) Write(p []byte) (int, error) {
	if room := l.Max - l.buf.Len(); room > 0 {
		l.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// Bytes is what was kept.
func (l *LimitedText) Bytes() []byte { return l.buf.Bytes() }
