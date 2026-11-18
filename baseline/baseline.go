// Package baseline is what every project runs on beside the harness (design 10.3): the agents'
// definitions, the guides of each kind of work, the templates, and the words of the Crew MCP's
// tools. It is built into the server, so a baseline version is a Crew release.
package baseline

import (
	"embed"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

//go:embed agents guides stages templates tools.yaml roles.yaml WORKFLOW.md
var files embed.FS

// Text returns one file of the baseline, by its path in it, such as "guides/planning.md".
func Text(path string) (string, error) {
	b, err := fs.ReadFile(files, path)
	if err != nil {
		return "", fmt.Errorf("baseline: %w", err)
	}
	return string(b), nil
}

// MustText is Text for a path the code names itself.
func MustText(path string) string {
	s, err := Text(path)
	if err != nil {
		panic(err)
	}
	return s
}

// Body is a markdown file without its front matter.
func Body(text string) string {
	if rest, ok := strings.CutPrefix(text, "---\n"); ok {
		if _, body, ok := strings.Cut(rest, "\n---\n"); ok {
			return strings.TrimLeft(body, "\n")
		}
	}
	return text
}

// Paths lists every file of the baseline.
func Paths() []string {
	var out []string
	fs.WalkDir(files, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	slices.Sort(out)
	return out
}

// Tool is one tool's entry in tools.yaml.
type Tool struct {
	Group       string   `yaml:"group" json:"group"`
	Roles       []string `yaml:"roles" json:"roles"`
	Description string   `yaml:"description" json:"description"`
}

// Tools reads tools.yaml.
func Tools() (map[string]Tool, error) {
	b, err := fs.ReadFile(files, "tools.yaml")
	if err != nil {
		return nil, err
	}
	var out map[string]Tool
	if err := yaml.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("baseline: tools.yaml: %w", err)
	}
	for name, t := range out {
		if strings.TrimSpace(t.Description) == "" || len(t.Roles) == 0 {
			return nil, fmt.Errorf("baseline: tools.yaml: %s needs a description and its roles", name)
		}
	}
	return out, nil
}

// Role is what one agent runs on in the baseline: its entries, each a CLI with a model, in order, and
// its effort (12.3).
type Role struct {
	Model  []string `yaml:"model"`
	Effort string   `yaml:"effort"`
}

// Roles reads roles.yaml: the entries install copies into the machine's agents.roles.
func Roles() (map[string]Role, error) {
	b, err := fs.ReadFile(files, "roles.yaml")
	if err != nil {
		return nil, err
	}
	var out map[string]Role
	if err := yaml.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("baseline: roles.yaml: %w", err)
	}
	for name, r := range out {
		if len(r.Model) == 0 || r.Effort == "" {
			return nil, fmt.Errorf("baseline: roles.yaml: %s needs its entries and its effort", name)
		}
	}
	return out, nil
}
