// Package harnesstree gives the server the base layer's words (12.9): the name and the description of
// each skill, agent, and rule the runtime image is built with, for list_skills and the Settings
// screen. The image itself takes this tree through internal/harnessbuild.
package harnesstree

import (
	"embed"
	"io/fs"
	"path"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

//go:embed skills/*/SKILL.md agents/*.md rules/*.md context/AGENTS.md
var tree embed.FS

// Item is one skill, agent, or rule of the base.
type Item struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Base lists the base layer: its skills, its agents, and its rules, each by name.
func Base() []Item {
	return BaseWithOverrides(nil)
}

// BaseWithOverrides lists the base layer with any operator-saved custom harness files overlaid.
// Keys in custom are paths inside harness/ (such as skills/foo/SKILL.md, agents/bar.md, rules/baz.md).
func BaseWithOverrides(custom map[string]string) []Item {
	files := FilesMap()
	for k, v := range custom {
		if strings.TrimSpace(v) == "" {
			delete(files, k)
		} else {
			files[k] = v
		}
	}
	var out []Item
	for f, content := range files {
		var kind, fallbackName string
		switch {
		case strings.HasPrefix(f, "skills/") && strings.HasSuffix(f, "/SKILL.md") && strings.Count(f, "/") == 2:
			kind = "skill"
			fallbackName = path.Base(path.Dir(f))
		case strings.HasPrefix(f, "agents/") && strings.HasSuffix(f, ".md") && strings.Count(f, "/") == 1:
			kind = "agent"
			fallbackName = strings.TrimSuffix(path.Base(f), ".md")
		case strings.HasPrefix(f, "rules/") && strings.HasSuffix(f, ".md") && strings.Count(f, "/") == 1:
			kind = "rule"
			fallbackName = strings.TrimSuffix(path.Base(f), ".md")
		default:
			continue
		}
		var fm struct {
			Name        string `yaml:"name"`
			Description string `yaml:"description"`
		}
		if s := content; strings.HasPrefix(s, "---\n") {
			if end := strings.Index(s[4:], "\n---"); end >= 0 {
				_ = yaml.Unmarshal([]byte(s[4:4+end]), &fm)
			}
		}
		if fm.Name == "" {
			fm.Name = fallbackName
		}
		out = append(out, Item{Kind: kind, Name: fm.Name, Description: strings.TrimSpace(fm.Description)})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Kind < out[j].Kind || out[i].Kind == out[j].Kind && out[i].Name < out[j].Name
	})
	return out
}

// Paths lists every embedded markdown file of the base layer by its path in harness/.
func Paths() []string {
	var out []string
	for _, glob := range []string{"context/AGENTS.md", "rules/*.md", "agents/*.md", "skills/*/SKILL.md"} {
		files, _ := fs.Glob(tree, glob)
		out = append(out, files...)
	}
	sort.Strings(out)
	return out
}

// FilesMap returns all embedded markdown files of harness/ keyed by their path in harness/.
func FilesMap() map[string]string {
	out := map[string]string{}
	for _, p := range Paths() {
		if b, err := tree.ReadFile(p); err == nil {
			out[p] = string(b)
		}
	}
	return out
}

// Text is one markdown file of the base layer by its path in harness/, such as agents/reviewer.md: what
// an improvement of it replaces (10.4).
func Text(p string) (string, error) {
	b, err := fs.ReadFile(tree, p)
	return string(b), err
}
