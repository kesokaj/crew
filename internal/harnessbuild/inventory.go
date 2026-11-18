package harnessbuild

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// Skill is one directory under skills/ holding a SKILL.md.
type Skill struct {
	Name        string
	Description string
	Dir         string // skills/<dir> inside the Source
	SlashOnly   bool   // disable-model-invocation: only typed as /name
}

// Agent is one Claude Code subagent definition under agents/.
type Agent struct {
	Name        string
	Description string
	Path        string
	Front       Frontmatter
	Body        string
	Raw         []byte
}

// Doc is a plain markdown customization — a command or a rule. Frontmatter
// is optional; without a description the first line of text stands in.
type Doc struct {
	Name        string
	Description string
	Path        string
	Raw         []byte
}

// Inventory is everything the harness holds. Each field is a directory:
// a file dropped into it is built into the next image with no code change.
type Inventory struct {
	Skills   []Skill // skills/<name>/SKILL.md
	Agents   []Agent // agents/<name>.md
	Commands []Doc   // commands/<name>.md and workflows/<name>.md
	Rules    []Doc   // rules/<name>.md
}

// identLike holds a frontmatter `name:` to the characters an identifier
// needs (`[A-Za-z0-9._-]`, leading alphanumeric): the same value is rendered
// verbatim into the context both CLIs read, so a backtick or a newline in it
// would inject prose into every run's context.
func identLike(name string) bool {
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case i > 0 && (r == '.' || r == '_' || r == '-'):
		default:
			return false
		}
	}
	return true
}

// validName is the parse boundary for a frontmatter `name:`, the value
// joined into agy's skills/<name>/… and agents/<name>/agent.md. A name is ONE path segment: nothing that filepath.Join would
// carry out of the home skeleton (`../x`, an absolute path), and no
// separator at all (`a/b` would nest a skill inside another's directory).
func validName(kind, file, name string) error {
	switch {
	case name == "", name == ".", name == "..":
		return fmt.Errorf("%s %s: name %q is not a path segment", kind, file, name)
	case strings.ContainsAny(name, `/\`), !filepath.IsLocal(name), !identLike(name):
		return fmt.Errorf("%s %s: name %q must be a single local path segment", kind, file, name)
	}
	return nil
}

// checkManagedTree refuses a symbolic link anywhere the build reads the
// harness's own bytes: each managed directory, and every entry directly
// inside the flat ones. os.DirFS follows a directory link on ReadDir and Glob
// and a file link on ReadFile, so either would build bytes from outside the
// harness into the image, and for context/ into the context both CLIs send
// to the model. One check at the boundary, so no reader can forget it. It
// runs only for a tree on disk.
func checkManagedTree(src Source) error {
	if src.Dir == "" {
		return nil
	}
	for _, dir := range []string{"context", "agents", "commands", "rules", "workflows", "skills"} {
		p := src.path(dir)
		info, err := os.Lstat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink — the harness ships regular directories only", p)
		}
		if !info.IsDir() || dir == "skills" {
			// skills/<name> may be a link, as a skill added with
			// `npx skills add -g` is, and os.DirFS follows it. A skill's own
			// files are walked in Plan, and its SKILL.md is read with
			// readRegular.
			continue
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("%s is a symlink — the harness ships regular files only", filepath.Join(p, e.Name()))
			}
		}
	}
	return nil
}

// LoadInventory reads every customization directory of the source.
func LoadInventory(src Source) (Inventory, error) {
	var inv Inventory
	if err := checkManagedTree(src); err != nil {
		return inv, err
	}
	var err error
	if inv.Skills, err = LoadSkills(src); err != nil {
		return inv, err
	}
	if inv.Agents, err = LoadAgents(src); err != nil {
		return inv, err
	}
	if inv.Commands, err = loadDocs(src, "commands", "workflows"); err != nil {
		return inv, err
	}
	if inv.Rules, err = loadDocs(src, "rules"); err != nil {
		return inv, err
	}
	return inv, nil
}

// LoadSkills reads every skills/<name>/SKILL.md. A directory without a
// SKILL.md is ignored; one with a SKILL.md but no description is an error,
// because the description is the only thing either CLI shows the model.
func LoadSkills(src Source) ([]Skill, error) {
	entries, err := fs.ReadDir(src.FS, "skills")
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", src.path("skills"), err)
	}
	var skills []Skill
	// Two skills of the same name build to one destination, so the second
	// would silently overwrite the first. A duplicate means something is
	// wrong in skills/ — an interrupted copy, a directory whose frontmatter
	// name belongs to another skill — and is refused rather than resolved by
	// walk order.
	byName := map[string]string{}
	for _, e := range entries {
		dir := path.Join("skills", e.Name())
		// fs.Stat follows a link, so a skills/<name> link to a directory is a
		// skill and a dangling one is refused out loud.
		info, err := fs.Stat(src.FS, dir)
		if err != nil {
			return nil, fmt.Errorf("resolving skill %s: %w", e.Name(), err)
		}
		if !info.IsDir() {
			continue
		}
		data, err := src.readRegular(path.Join(dir, "SKILL.md"))
		if err != nil {
			// No SKILL.md means the directory is not a skill. Any other
			// error, a symlinked SKILL.md above all, is refused out loud:
			// Plan's walk only visits skills this loader returned, so a
			// silent skip would drop the refusal altogether.
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		front, _, err := Split(string(data))
		if err != nil {
			return nil, fmt.Errorf("skill %s: %w", e.Name(), err)
		}
		fm, err := ParseStrict(front)
		if err != nil {
			return nil, fmt.Errorf("skill %s: %w", e.Name(), err)
		}
		name := fm["name"]
		if name == "" {
			name = e.Name()
		}
		if err := validName("skill", e.Name()+"/SKILL.md", name); err != nil {
			return nil, err
		}
		if fm["description"] == "" {
			return nil, fmt.Errorf("skill %s: SKILL.md has no description", e.Name())
		}
		// A skill's frontmatter is a permission surface: allowed-tools lets
		// every named tool run unprompted while the skill is active, and hooks
		// run commands, so the run's permission mode decides, never a skill.
		if k := permissionKey(front, "allowed-tools", "hooks"); k != "" {
			return nil, fmt.Errorf("skill %s: %s is never set in a harness skill; the run's permission mode decides what runs unprompted", name, k)
		}
		if first, dup := byName[name]; dup {
			return nil, fmt.Errorf("two skills are named %q: skills/%s and skills/%s — one destination, so one would silently overwrite the other", name, first, e.Name())
		}
		byName[name] = e.Name()
		skills = append(skills, Skill{
			Name:        name,
			Description: fm["description"],
			Dir:         dir,
			SlashOnly:   fm["disable-model-invocation"] == "true",
		})
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return skills, nil
}

// agentKeys is every frontmatter key a harness agent may set. Claude Code
// reads an agent file byte for byte, so a key it honours outside this list
// (hooks, permissionMode, mcpServers, memory, initialPrompt, and whatever a
// vendor adds next) would act in every run unreviewed: an allow-list
// refuses the next one by default. disallowedTools is not on it: the agy
// agent DeriveAgy writes drops it, so the restriction would hold on one CLI
// only.
var agentKeys = []string{"name", "description", "tools", "model", "maxTurns", "skills", "effort", "color"}

// LoadAgents reads every agents/*.md in Claude Code's subagent format.
func LoadAgents(src Source) ([]Agent, error) {
	paths, err := fs.Glob(src.FS, "agents/*.md")
	if err != nil {
		return nil, err
	}
	var agents []Agent
	for _, p := range paths {
		raw, err := fs.ReadFile(src.FS, p)
		if err != nil {
			return nil, err
		}
		front, body, err := Split(string(raw))
		if err != nil {
			return nil, fmt.Errorf("agent %s: %w", path.Base(p), err)
		}
		fm, err := ParseStrict(front)
		if err != nil {
			return nil, fmt.Errorf("agent %s: %w", path.Base(p), err)
		}
		name := fm["name"]
		if name == "" {
			name = strings.TrimSuffix(path.Base(p), ".md")
		}
		if err := validName("agent", path.Base(p), name); err != nil {
			return nil, err
		}
		if fm["description"] == "" {
			return nil, fmt.Errorf("agent %s: no description", name)
		}
		// One line, so it reads as one row wherever a CLI lists the agents,
		// and free of two phrases that described a mechanism that no longer
		// exists.
		if descriptionIsMultiLine(front) {
			return nil, fmt.Errorf("agent %s: description must be one line", name)
		}
		if k := unlistedKey(front, agentKeys); k != "" {
			return nil, fmt.Errorf("agent %s: %s is not a key a harness agent sets (%s); a new one is added to agentKeys by hand, and a key that grants a permission never is", name, k, strings.Join(agentKeys, ", "))
		}
		for _, banned := range []string{"dispatch prompt", "harness binary"} {
			if strings.Contains(strings.ToLower(fm["description"]), banned) {
				return nil, fmt.Errorf("agent %s: description names %q, a mechanism that no longer exists", name, banned)
			}
		}
		agents = append(agents, Agent{Name: name, Description: fm["description"], Path: src.path(p), Front: fm, Body: body, Raw: raw})
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].Name < agents[j].Name })
	return agents, nil
}

// loadDocs reads every *.md directly under the named source directories.
// Missing directories are fine; the same name in two of them is not.
func loadDocs(src Source, dirs ...string) ([]Doc, error) {
	seen := map[string]string{}
	var docs []Doc
	for _, dir := range dirs {
		paths, err := fs.Glob(src.FS, dir+"/*.md")
		if err != nil {
			return nil, err
		}
		for _, p := range paths {
			raw, err := fs.ReadFile(src.FS, p)
			if err != nil {
				return nil, err
			}
			shown := src.path(p)
			name := strings.TrimSuffix(path.Base(p), ".md")
			if prev, dup := seen[name]; dup {
				return nil, fmt.Errorf("%s and %s both define %q", prev, shown, name)
			}
			seen[name] = shown
			desc := ""
			text := string(raw)
			if front, body, err := Split(text); err == nil {
				fm, err := ParseStrict(front)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", shown, err)
				}
				if k := permissionKey(front, "allowed-tools", "hooks"); k != "" {
					return nil, fmt.Errorf("%s: %s is never set in a harness command or rule; the run's permission mode decides", shown, k)
				}
				desc = fm["description"]
				text = body
			} else if opensFrontmatter(text) {
				// Not "no frontmatter": a block that starts and cannot be
				// read would be built as written with its keys unchecked, and
				// agy drops a rule whose frontmatter it cannot read.
				return nil, fmt.Errorf("%s: frontmatter starts but cannot be read (the opening --- must be the very first line, alone on it, and a closing --- must follow); a command or rule has a readable frontmatter block or none", shown)
			}
			if desc == "" {
				desc = firstLine(text)
			}
			docs = append(docs, Doc{Name: name, Description: desc, Path: shown, Raw: raw})
		}
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].Name < docs[j].Name })
	return docs, nil
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(strings.TrimLeft(l, "# ")); l != "" {
			return l
		}
	}
	return ""
}

// RenderInventoryCompact is the form built into the context: names only.
// Both CLIs inject every skill's description at session start anyway, so
// repeating descriptions there would spend the context's bytes on what is
// already present.
func RenderInventoryCompact(inv Inventory) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Installed\n\nBuilt into Crew's runtime image from its `harness/`; the Crew MCP's `list_skills` lists every skill of this run, the project's own included.%s\n\n", slashNote(inv))
	var skills []string
	for _, s := range inv.Skills {
		if s.SlashOnly {
			skills = append(skills, "`/"+s.Name+"`")
		} else {
			skills = append(skills, "`"+s.Name+"`")
		}
	}
	fmt.Fprintf(&b, "- Skills (%d): %s\n", len(inv.Skills), strings.Join(skills, ", "))
	var agents []string
	for _, a := range inv.Agents {
		model := a.Front["model"]
		if model == "" {
			model = "inherit"
		}
		agents = append(agents, "`"+a.Name+"` ("+model+")")
	}
	fmt.Fprintf(&b, "- Subagents (%d): %s\n", len(inv.Agents), strings.Join(agents, ", "))
	if len(inv.Commands) > 0 {
		var names []string
		for _, c := range inv.Commands {
			names = append(names, "`/"+c.Name+"`")
		}
		fmt.Fprintf(&b, "- Commands (%d): %s\n", len(inv.Commands), strings.Join(names, ", "))
	}
	if len(inv.Rules) > 0 {
		var names []string
		for _, r := range inv.Rules {
			names = append(names, "`"+r.Name+"`")
		}
		fmt.Fprintf(&b, "- Rules (%d): %s\n", len(inv.Rules), strings.Join(names, ", "))
	}
	return b.String()
}

// opensFrontmatter reports whether the first non-blank line looks like a
// frontmatter fence to any reader more lenient than Split: `---` after
// byte-order marks, zero-width characters or blank lines, or followed by
// spaces of any kind, a tag or a comment. Split wants the bare line first; a
// file that is not that, yet looks like this, is refused rather than read as
// "no frontmatter". Four dashes or more are a markdown rule, not a fence.
func opensFrontmatter(text string) bool {
	invisible := func(r rune) bool {
		return unicode.IsSpace(r) || r == '\ufeff' || r == '\u200b' || r == '\u200c' || r == '\u200d' || r == '\u2060'
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimFunc(line, invisible)
		if line == "" {
			continue
		}
		return strings.HasPrefix(line, "---") && !strings.HasPrefix(line, "----")
	}
	return false
}

// slashNote explains the `/name` marking, but only when some skill carries
// `disable-model-invocation`; with none, the sentence would describe nothing.
func slashNote(inv Inventory) string {
	for _, s := range inv.Skills {
		if s.SlashOnly {
			return " Skills marked `/name` are typed by the user; the rest load when their description matches the task."
		}
	}
	return " Every skill loads when its description matches the task and can also be typed as `/name`."
}

// descriptionIsMultiLine reports whether the frontmatter's description is a
// block (`|`, `>`) or carries continuation lines: an agent's description is
// one line.
func descriptionIsMultiLine(front string) bool {
	for _, e := range entries(front) {
		if e.key != "description" {
			continue
		}
		return isBlockIndicator(e.val) || len(e.cont) > 0
	}
	return false
}
