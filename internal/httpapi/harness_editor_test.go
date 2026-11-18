package httpapi_test

import (
	"context"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/board"
)

func TestGlobalAndProjectHarnessEditor(t *testing.T) {
	t.Parallel()
	k := newTicketKit(t)
	k.shop([]string{}, nil, "one")

	// 1. GET /api/crew/baseline lists embedded harness files including frontend-ui-modern, frontend-ui-economist, and code-modularity.
	var baseResp struct {
		Files []struct {
			Path       string `json:"path"`
			Customized bool   `json:"customized"`
		} `json:"files"`
		HarnessFiles []struct {
			Path       string `json:"path"`
			Kind       string `json:"kind"`
			Name       string `json:"name"`
			Customized bool   `json:"customized"`
		} `json:"harnessFiles"`
	}
	k.call("GET", "/api/crew/baseline", "", 200, &baseResp)
	seenModern, seenEconomist, seenModularity := false, false, false
	for _, hf := range baseResp.HarnessFiles {
		switch hf.Path {
		case "harness/skills/frontend-ui-modern/SKILL.md":
			seenModern = true
		case "harness/skills/frontend-ui-economist/SKILL.md":
			seenEconomist = true
		case "harness/rules/code-modularity.md":
			seenModularity = true
		}
	}
	if !seenModern || !seenEconomist || !seenModularity {
		t.Fatalf("missing expected harness files: modern=%v economist=%v modularity=%v", seenModern, seenEconomist, seenModularity)
	}

	// 2. GET /api/crew/baseline/file reads a rule and a baseline file.
	var fileDet struct {
		Path           string `json:"path"`
		Kind           string `json:"kind"`
		Content        string `json:"content"`
		DefaultContent string `json:"defaultContent"`
		Customized     bool   `json:"customized"`
	}
	k.call("GET", "/api/crew/baseline/file?path=harness/rules/code-modularity.md", "", 200, &fileDet)
	if fileDet.Customized || !strings.Contains(fileDet.Content, "# Code modularity") {
		t.Fatalf("unexpected rule detail: %+v", fileDet)
	}

	// 3. PUT /api/crew/baseline/file refuses invalid skill frontmatter (missing description), and accepts a valid custom skill and baseline edit.
	k.call("PUT", "/api/crew/baseline/file", `{"path":"harness/skills/custom-skill/SKILL.md","content":"# No frontmatter\n"}`, 400, nil)
	k.call("PUT", "/api/crew/baseline/file", `{"path":"harness/skills/custom-skill/SKILL.md","content":"---\nname: custom-skill\ndescription: \"Custom global skill for testing.\"\n---\n\n# Custom skill\n"}`, 200, &fileDet)
	if !fileDet.Customized || fileDet.Kind != "skill" {
		t.Fatalf("expected customized global skill: %+v", fileDet)
	}
	skel, err := board.CustomHarnessSkeleton(context.Background(), k.b.Pool)
	if err != nil {
		t.Fatalf("CustomHarnessSkeleton: %v", err)
	}
	if !strings.Contains(string(skel[".gemini/config/skills/custom-skill/SKILL.md"]), "# Custom skill") ||
		!strings.Contains(string(skel[".gemini/config/rules/AGENTS.md"]), "`custom-skill`") {
		t.Fatalf("unexpected custom skeleton keys: %v", skel)
	}

	k.call("PUT", "/api/crew/baseline/file", `{"path":"baseline/stages/in_progress.md","content":"# Custom In Progress Stage\nWork carefully.\n"}`, 200, &fileDet)
	txt, src, err := board.BaselineText(context.Background(), k.b.Pool, "", "stages/in_progress.md", "0.3.21")
	if err != nil || !strings.Contains(txt, "# Custom In Progress Stage") || src != "baseline/stages/in_progress.md@custom" {
		t.Fatalf("BaselineText custom: txt=%q src=%q err=%v", txt, src, err)
	}

	// 4. DELETE /api/crew/baseline/file resets both back to defaults.
	k.call("DELETE", "/api/crew/baseline/file?path=harness/skills/custom-skill/SKILL.md", "", 200, nil)
	k.call("DELETE", "/api/crew/baseline/file?path=baseline/stages/in_progress.md", "", 200, nil)
	if skelAfter, err := board.CustomHarnessSkeleton(context.Background(), k.b.Pool); err != nil || len(skelAfter) != 0 {
		t.Fatalf("expected empty CustomHarnessSkeleton after reset, got %v (err %v)", skelAfter, err)
	}

	// 5. Per-project skills, rules, and context on a running project.
	// Refuses colliding with a global base skill name.
	k.call("PUT", "/api/projects/shop/skills/file", `{"path":".agents/skills/golang/SKILL.md","content":"---\nname: golang\ndescription: \"dup\"\n---\n# Dup\n"}`, 400, nil)
	// Saves a new project skill and a project rule on main.
	k.call("PUT", "/api/projects/shop/skills/file", `{"path":".agents/skills/shop-stripe/SKILL.md","content":"---\nname: shop-stripe\ndescription: \"Stripe billing conventions for shop.\"\n---\n\n# Shop Stripe\n"}`, 200, nil)
	k.call("PUT", "/api/projects/shop/skills/file", `{"path":".agents/rules/shop-style.md","content":"---\ndescription: \"Shop style rule\"\n---\n\n# Shop style\n"}`, 200, nil)

	var skillFile struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	k.call("GET", "/api/projects/shop/skills/file?path=.agents/skills/shop-stripe/SKILL.md", "", 200, &skillFile)
	if !strings.Contains(skillFile.Content, "# Shop Stripe") {
		t.Fatalf("unexpected project skill content: %+v", skillFile)
	}

	// Base file lookup and baseline file lookup via /api/projects/{p}/skills/file.
	k.call("GET", "/api/projects/shop/skills/file?path=skills/frontend-ui-modern/SKILL.md&base=1", "", 200, &skillFile)
	if !strings.Contains(skillFile.Content, "frontend-ui-modern") {
		t.Fatalf("unexpected base skill content: %+v", skillFile)
	}
	k.call("GET", "/api/projects/shop/skills/file?path=baseline/stages/in_progress.md", "", 200, &skillFile)
	if strings.TrimSpace(skillFile.Content) == "" {
		t.Fatalf("expected non-empty baseline file content")
	}

	// Delete the project rule.
	k.call("DELETE", "/api/projects/shop/skills/file?path=.agents/rules/shop-style.md", "", 200, nil)
	k.call("GET", "/api/projects/shop/skills/file?path=.agents/rules/shop-style.md", "", 404, nil)

	// Request a skill ticket on the running project.
	var reqOut struct {
		Ticket string `json:"ticket"`
		Joined bool   `json:"joined"`
	}
	k.call("POST", "/api/projects/shop/skills/request", `{"missing":"Calendar ICS recurrence","seenIn":"SHP-1","wanted":"RFC 5545 recurrence rule parser skill"}`, 201, &reqOut)
	if reqOut.Ticket == "" || reqOut.Joined {
		t.Fatalf("expected new skill ticket, got %+v", reqOut)
	}
}
