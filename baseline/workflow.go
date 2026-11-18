package baseline

import "strings"

// RenderWorkflow returns the baseline WORKFLOW.md template populated with the project's name and key.
func RenderWorkflow(projectName, projectKey string) string {
	raw, err := files.ReadFile("WORKFLOW.md")
	if err != nil {
		return ""
	}
	r := strings.NewReplacer(
		"{{PROJECT_NAME}}", oneLineYAML(projectName),
		"{{PROJECT_KEY}}", oneLineYAML(projectKey),
	)
	return r.Replace(string(raw))
}

func oneLineYAML(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, `"`, `\"`)
}
