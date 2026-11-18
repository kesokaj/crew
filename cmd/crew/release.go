package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/release"
	"github.com/kesokaj/crew/internal/version"
)

// releaseCmd is `crew release write`, which crew-ctl build runs in a container of the release's own
// server image (16.11): this program knows its schema and the steps the version before reads. The
// facts git knows come on standard input: the lines of `git ls-tree -r <commit>`, then one line
// "agent PATH<TAB>LINE" for each model or effort line of an agent file.
func releaseCmd(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 || args[0] != "write" {
		return usageError("release write --dir DIR --commit SHA --machine-setup N [--tree FILE.tar] --image NAME=TAG@ID... [--pin NAME=VALUE...]")
	}
	fs := flag.NewFlagSet("release write", flag.ContinueOnError)
	dir := fs.String("dir", "", "the release folder, named by the version")
	commit := fs.String("commit", "", "the commit it was built from")
	setup := fs.Int("machine-setup", 0, "the number of the machine setup it needs")
	tree := fs.String("tree", "", "the commit's tree as git archive wrote it: the runtime image's inputs and the deploy archive come from it")
	var images, pins multi
	fs.Var(&images, "image", "NAME=TAG@ID, for each image of the release")
	fs.Var(&pins, "pin", "NAME=VALUE: the version of a tool in the release")
	if err := fs.Parse(args[1:]); err != nil {
		return usageError(err.Error())
	}
	if *dir == "" || *commit == "" || *setup < 1 {
		return usageError("release write needs --dir, --commit, and --machine-setup")
	}
	if filepath.Base(*dir) != version.Version {
		return fmt.Errorf("this is the program of %s, and the folder is %s", version.Version, filepath.Base(*dir))
	}
	steps, err := db.Steps()
	if err != nil {
		return err
	}
	facts := release.Facts{Version: version.Version, Commit: *commit, BuiltAt: time.Now(), Schema: len(steps),
		ReadBy: db.ReadBy(steps), MachineSetup: *setup, Images: map[string]release.Image{}, Pins: map[string]string{}}
	for _, kv := range images {
		name, ref, ok := strings.Cut(kv, "=")
		tag, id, ok2 := strings.Cut(ref, "@")
		if !ok || !ok2 {
			return usageError("--image NAME=TAG@ID, not " + kv)
		}
		// release.json names the config digest, which every image store keeps, not the id the
		// builder's store gave (D48); the file must hold the image that id is.
		file, err := release.ReadImageFile(filepath.Join(*dir, release.ImagesDir, name+".tar"))
		if err != nil {
			return fmt.Errorf("the image file of %s: %w", tag, err)
		}
		if file.Config != "" {
			if !file.AcceptsID(file.Config, id) {
				return fmt.Errorf("%s is %s, which is not the image its file holds, %s", tag, id, file.Config)
			}
			id = file.Config
		}
		facts.Images[name] = release.Image{Tag: tag, ID: id}
	}
	for _, kv := range pins {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			return usageError("--pin NAME=VALUE, not " + kv)
		}
		facts.Pins[name] = value
	}
	files, agents, err := readFacts(stdin)
	if err != nil {
		return err
	}
	if *tree != "" {
		if err := fromTree(*tree, *dir, &facts); err != nil {
			return err
		}
	}
	m, err := release.Write(*dir, facts, files, agents)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "release.json of %s: %d files, schema %d, read by %d\n", m.Version, len(m.Files), m.Schema, m.ReadBy)
	return nil
}

// fromTree adds what the release's tree says to facts, the same as the running release reads from a
// commit Crew builds (16.2 step 4): the runtime image's inputs, and the deploy archive, written into
// dir. A tree whose schema is not this program's, or whose machine setup is not the one given, is
// another commit's.
func fromTree(path, dir string, facts *release.Facts) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	t, err := release.ReadTree(f)
	if err != nil {
		return err
	}
	if t.Schema != facts.Schema || t.ReadBy != facts.ReadBy || t.MachineSetup != facts.MachineSetup {
		return fmt.Errorf("the tree has schema %d read by %d and machine setup %d; this program has %d, %d, and %d was given: it is another commit's",
			t.Schema, t.ReadBy, t.MachineSetup, facts.Schema, facts.ReadBy, facts.MachineSetup)
	}
	if err := os.WriteFile(filepath.Join(dir, release.DeployFile), t.Deploy, 0o644); err != nil {
		return err
	}
	facts.Runtime, facts.Deploy = t.Runtime, release.DeployFile
	// The gate's browser tests, which release.sh build packed beside the images (16.2 step 5).
	if _, err := os.Stat(filepath.Join(dir, release.BrowserFile)); err == nil {
		facts.Browser = release.BrowserFile
	}
	return nil
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

// readFacts reads the lines of git ls-tree and the agents' lines.
func readFacts(r io.Reader) (map[string]string, map[string]string, error) {
	var tree strings.Builder
	agents := map[string]string{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if rest, ok := strings.CutPrefix(line, "agent "); ok {
			path, setting, ok := strings.Cut(rest, "\t")
			if !ok || !release.AgentFile(path) {
				return nil, nil, fmt.Errorf("not an agent's line: %q", line)
			}
			if agents[path] != "" {
				agents[path] += "\n"
			}
			agents[path] += strings.TrimSpace(setting)
			continue
		}
		tree.WriteString(line + "\n")
	}
	if err := sc.Err(); err != nil {
		return nil, nil, err
	}
	files, err := release.ParseTree(strings.NewReader(tree.String()))
	if err != nil {
		return nil, nil, err
	}
	if len(files) == 0 {
		return nil, nil, errors.New("no files of the commit came on standard input")
	}
	return files, agents, nil
}
