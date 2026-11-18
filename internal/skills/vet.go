// Package skills implements Crew's 4-tier Skills Registry: static AST and content
// security vetting, VENDORED.md commit SHA/licence pinning, per-factory activation via
// Tasks API, and live warm-VM sync into .gemini/config/skills/ (design 12.11, R221).
package skills

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/kesokaj/crew/internal/board"
)

var (
	skillNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	fencedGoRe  = regexp.MustCompile("(?s)```go\\n(.*?)\\n```")
)

// ScanReport records the verdict and findings of VetSkill on a skill bundle.
type ScanReport struct {
	Verdict        string   `json:"verdict"`
	Findings       []string `json:"findings"`
	Description    string   `json:"description"`
	PromptBytes    int      `json:"promptBytes"`
	TokenFootprint int      `json:"tokenFootprint"`
}

var blockedPromptPhrases = []struct {
	phrase string
	reason string
}{
	{"ignore previous instructions", "prompt-injection directive (ignore previous instructions)"},
	{"ignore all prior", "prompt-injection directive (ignore all prior instructions)"},
	{"disregard system prompt", "prompt-injection directive (disregard system prompt)"},
	{"anthropic_api_key", "forbidden API key reference (Rule 11)"},
	{"skip review", "attempts to bypass check/review gate (Rule 2)"},
	{"bypass check", "attempts to bypass check gate (Rule 2)"},
	{"disable 13.8", "attempts to loosen 13.8 instruction-file guard"},
	{"<crew_data_fence>", "spoofs Crew untrusted data fence delimiter (Rule 14)"},
	{"===== begin", "spoofs Crew fence block delimiter (Rule 14)"},
}

var sensitivePaths = []string{
	"/etc/crew/secret.key",
	"antigravity-oauth-token",
	"/var/run/docker.sock",
	"$gh_token",
	"${gh_token}",
}

// VetSkill runs deterministic path, frontmatter, prompt-injection, secret, exfiltration, and Go
// AST static checks across every file of a skill bundle before it can enter the registry.
func VetSkill(name string, files map[string]string) ScanReport {
	var findings []string
	cleanName := strings.TrimSpace(name)
	if !skillNameRe.MatchString(cleanName) {
		findings = append(findings, fmt.Sprintf("invalid skill identifier %q", name))
	}

	var skillMD string
	for rawPath, content := range files {
		if strings.ContainsRune(rawPath, '\x00') || strings.Contains(rawPath, "\\") || strings.HasPrefix(rawPath, "/") {
			findings = append(findings, fmt.Sprintf("unsafe path %q", rawPath))
			continue
		}
		cleaned := path.Clean(rawPath)
		if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.Count(cleaned, "/") > 5 {
			findings = append(findings, fmt.Sprintf("path traversal or excessive depth: %q", rawPath))
			continue
		}
		if cleaned == "SKILL.md" || cleaned == ".agents/skills/"+cleanName+"/SKILL.md" {
			skillMD = content
		}
		scanTextContent(cleaned, content, &findings)
		if strings.HasSuffix(cleaned, ".go") {
			scanGoAST(cleaned, content, &findings)
		}
		for idx, m := range fencedGoRe.FindAllStringSubmatch(content, -1) {
			if len(m) > 1 {
				scanGoAST(fmt.Sprintf("%s#go-%d", cleaned, idx+1), m[1], &findings)
			}
		}
	}

	desc := ""
	if strings.TrimSpace(skillMD) == "" {
		findings = append(findings, "missing SKILL.md entrypoint")
	} else {
		fmName, fmDesc, ok := parseFrontmatter(skillMD)
		if !ok {
			findings = append(findings, "SKILL.md missing YAML frontmatter (name and description)")
		} else {
			if fmName == "" {
				findings = append(findings, "SKILL.md frontmatter missing name")
			} else if cleanName != "" && fmName != cleanName {
				findings = append(findings, fmt.Sprintf("SKILL.md frontmatter name %q != skill %q", fmName, cleanName))
			}
			if fmDesc == "" {
				findings = append(findings, "SKILL.md frontmatter missing description")
			}
			desc = fmDesc
		}
	}

	verdict := "PASS"
	if len(findings) > 0 {
		verdict = "BLOCKED"
	} else {
		findings = []string{}
	}
	pb := len(cleanName) + len(desc) + 24
	return ScanReport{
		Verdict:        verdict,
		Findings:       findings,
		Description:    desc,
		PromptBytes:    pb,
		TokenFootprint: max(1, pb/4),
	}
}

func parseFrontmatter(md string) (name, desc string, ok bool) {
	norm := strings.ReplaceAll(md, "\r\n", "\n")
	if !strings.HasPrefix(norm, "---\n") {
		return "", "", false
	}
	end := strings.Index(norm[4:], "\n---")
	if end < 0 {
		return "", "", false
	}
	var fm struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if yaml.Unmarshal([]byte(norm[4:4+end]), &fm) != nil {
		return "", "", false
	}
	return strings.TrimSpace(fm.Name), strings.TrimSpace(fm.Description), true
}

func scanTextContent(file, content string, findings *[]string) {
	lower := strings.ToLower(content)
	for _, p := range blockedPromptPhrases {
		if strings.Contains(lower, p.phrase) {
			*findings = append(*findings, fmt.Sprintf("%s: %s", file, p.reason))
		}
	}
	if board.HoldsSecret(nil, content) {
		*findings = append(*findings, fmt.Sprintf("%s: contains secret-shaped token or key material", file))
	}
	for _, sens := range sensitivePaths {
		if strings.Contains(lower, sens) {
			*findings = append(*findings, fmt.Sprintf("%s: references sensitive path/token %s", file, sens))
		}
	}
	hasNetCmd := strings.Contains(lower, "curl ") || strings.Contains(lower, "wget ") || strings.Contains(lower, "nc ")
	if hasNetCmd && (strings.Contains(lower, "| bash") || strings.Contains(lower, "| sh") ||
		strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "/etc/crew")) {
		*findings = append(*findings, fmt.Sprintf("%s: unsafe shell network command pattern", file))
	}
}

func scanGoAST(file, src string, findings *[]string) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.SkipObjectResolution)
	if err != nil {
		return
	}
	var imports []string
	for _, imp := range f.Imports {
		imports = append(imports, strings.Trim(imp.Path.Value, `"`))
	}
	hasNet := slices.Contains(imports, "net/http") || slices.Contains(imports, "net")
	hasExec := slices.Contains(imports, "os/exec")
	readsEnvOrSecret := false
	postsExternal := false

	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok {
					call := id.Name + "." + sel.Sel.Name
					if call == "os.Getenv" || call == "os.LookupEnv" || call == "os.ReadFile" {
						readsEnvOrSecret = true
					}
					if call == "http.Post" || call == "http.PostForm" || call == "http.NewRequest" || call == "net.Dial" {
						postsExternal = true
					}
					if hasExec && call == "exec.Command" {
						*findings = append(*findings, fmt.Sprintf("%s: Go AST disallows exec.Command in skill code", file))
					}
				}
			}
		case *ast.BasicLit:
			val := strings.ToLower(strings.Trim(x.Value, `"`))
			for _, sens := range sensitivePaths {
				if strings.Contains(val, sens) {
					*findings = append(*findings, fmt.Sprintf("%s: Go AST references sensitive credential path %s", file, sens))
				}
			}
		}
		return true
	})

	if hasNet && postsExternal && readsEnvOrSecret {
		*findings = append(*findings, fmt.Sprintf("%s: Go AST detected network egress paired with env/file read", file))
	}
}
