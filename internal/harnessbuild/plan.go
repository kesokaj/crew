// Package harnessbuild builds Crew's harness into a home skeleton, in the
// place agy, Crew's one agent CLI, reads it (design 12.1). The runtime
// image build is its only caller and the only installer of the harness:
// nothing is installed when a sandbox starts, and there is no manifest or
// install state, because an image is built once and never updated in place.
package harnessbuild

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// AgyRoot is the root in the home skeleton: agy reads its global rules,
// skills, agents and workflows from ~/.gemini/config (spike item 33).
const AgyRoot = ".gemini/config"

// agy's limits on its global rules (design 12.2, quirks). agy builds one
// block from every file in ~/.gemini/config/rules/ and cuts each file at
// AgyRuleFileLimit bytes, and a rule file whose frontmatter it cannot read
// is dropped without a message. The context and the rules share the block,
// so their sum is held under ContextBudget, a margin under the cut.
const (
	AgyRuleFileLimit = 24000
	ContextBudget    = 22000
)

// File is one path of the home skeleton, relative to the home.
type File struct {
	Rel  string
	Data []byte
	Mode fs.FileMode
}

// Skeleton is the harness built for agy.
type Skeleton struct {
	Files     []File
	Inventory Inventory
	// ContextBytes is the size of the rendered context. AgyRulesBytes is the whole block agy builds from its rules
	// directory: the context and every rule beside it.
	ContextBytes  int
	AgyRulesBytes int
}

// Plan computes every file of the home skeleton. Each harness directory maps
// to one place, so a file dropped into harness/ is built without a code
// change:
//
//	context/*.md          → .gemini/config/rules/AGENTS.md
//	skills/<name>/**      → .gemini/config/skills/<name>/**
//	agents/<name>.md      → .gemini/config/agents/<name>/agent.md (derived)
//	commands/<name>.md    → .gemini/config/workflows/<name>.md
//	workflows/<name>.md   → same as commands/
//	rules/<name>.md       → .gemini/config/rules/<name>.md
//
// It refuses, with the file named, what agy would read differently from
// what harness/ says: a file agy
// would drop or cut, an agent tool agy has no name for, a frontmatter key
// that grants a permission, a link, and a secret-shaped file.
func Plan(src Source) (Skeleton, error) {
	inv, err := LoadInventory(src)
	if err != nil {
		return Skeleton{}, err
	}
	context, err := RenderContext(src, inv)
	if err != nil {
		return Skeleton{}, err
	}

	files := []File{
		{Rel: filepath.Join(AgyRoot, "rules", "AGENTS.md"), Data: context, Mode: 0o644},
	}

	for _, s := range inv.Skills {
		err := fs.WalkDir(src.FS, s.Dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			// A link is refused, not followed: reading it would build the
			// target's bytes, from anywhere on the machine, into the image.
			// A skill ships regular files only.
			if d.Type()&fs.ModeSymlink != 0 {
				return fmt.Errorf("%s is a symlink — a skill ships regular files only", src.path(p))
			}
			if d.IsDir() || d.Name() == ".DS_Store" {
				return nil
			}
			// A secret-shaped file is refused rather than built into the
			// image, which travels to every machine Crew is installed on.
			// git ignores such names, so review never sees one that sits in
			// a skill directory.
			if secretShaped(d.Name()) {
				return fmt.Errorf("%s is a secret-shaped file — the build refuses it; move it out of the skill", src.path(p))
			}
			rel := filepath.FromSlash(strings.TrimPrefix(p, s.Dir+"/"))
			info, err := d.Info()
			if err != nil {
				return err
			}
			data, err := fs.ReadFile(src.FS, p)
			if err != nil {
				return err
			}
			// Group and other write are masked off: a file that is 0664 or
			// 0666 in the checkout would otherwise be writable in the home by
			// another account, and its contents are instructions every run
			// reads. Read and execute bits are kept.
			mode := info.Mode().Perm()&0o755 | 0o600
			files = append(files, File{Rel: filepath.Join(AgyRoot, "skills", s.Name, rel), Data: data, Mode: mode})
			return nil
		})
		if err != nil {
			return Skeleton{}, fmt.Errorf("skill %s: %w", s.Name, err)
		}
	}

	for _, a := range inv.Agents {
		derived, err := DeriveAgy(a)
		if err != nil {
			return Skeleton{}, err
		}
		files = append(files, File{Rel: filepath.Join(AgyRoot, "agents", a.Name, "agent.md"), Data: derived, Mode: 0o644})
	}

	// A command is a markdown prompt invoked as /name: agy calls it a
	// workflow. Copied verbatim; agy ignores frontmatter keys it does not
	// know.
	for _, c := range inv.Commands {
		files = append(files, File{Rel: filepath.Join(AgyRoot, "workflows", c.Name+".md"), Data: c.Raw, Mode: 0o644})
	}

	agyRules := len(context)
	if err := checkAgyRule("the rendered context", len(context)); err != nil {
		return Skeleton{}, err
	}
	for _, r := range inv.Rules {
		if err := checkAgyRule(r.Path, len(r.Raw)); err != nil {
			return Skeleton{}, err
		}
		agyRules += len(r.Raw)
		files = append(files, File{Rel: filepath.Join(AgyRoot, "rules", r.Name+".md"), Data: r.Raw, Mode: 0o644})
	}
	if agyRules > ContextBudget {
		return Skeleton{}, fmt.Errorf("agy's rules block would be %d bytes (the context %d, the rules %d), over the budget of %d (agy cuts at %d): cut %d bytes from context/ or rules/, or move a rule into a skill",
			agyRules, len(context), agyRules-len(context), ContextBudget, AgyRuleFileLimit, agyRules-ContextBudget)
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Rel < files[j].Rel })
	for i := 1; i < len(files); i++ {
		if files[i].Rel == files[i-1].Rel {
			return Skeleton{}, fmt.Errorf("two sources map to ~/%s (a rule may not be named AGENTS.md)", files[i].Rel)
		}
	}
	return Skeleton{Files: files, Inventory: inv, ContextBytes: len(context), AgyRulesBytes: agyRules}, nil
}

// checkAgyRule refuses a file that agy would cut.
func checkAgyRule(name string, n int) error {
	if n > AgyRuleFileLimit {
		return fmt.Errorf("%s is %d bytes: agy cuts a rule file at %d, so the rest would never reach the model", name, n, AgyRuleFileLimit)
	}
	return nil
}

// secretShaped reports whether a file name matches the credential shapes the
// context's Git rule stops a commit for. The match ignores case (`.ENV` is
// `.env`).
func secretShaped(name string) bool {
	name = strings.ToLower(name)
	for _, pattern := range []string{
		".env", ".env.*", "*.env", ".envrc", ".netrc", ".npmrc", ".pypirc",
		"*.pem", "*.key", "*.p12", "*.pfx", "*.keystore", "*.jks",
		"*.tfvars", "*.tfstate", "*.tfstate.backup",
		"*credentials*.json", "*-key.json", "id_rsa", "id_ed25519", "id_ecdsa", "id_dsa",
		"*oauth-token*",                   // agy keeps its login in antigravity-oauth-token
		".git-credentials", "credentials", // git's credential store; the AWS CLI's ~/.aws/credentials
	} {
		if ok, _ := filepath.Match(pattern, name); ok {
			return true
		}
	}
	return false
}
