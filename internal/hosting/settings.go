package hosting

import (
	"context"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/settings"
)

// Settings are a project's hosting.* settings as they apply (19.3).
type Settings struct {
	Dockerfile string
	Port       int
	Health     string
	Data       string
	Size       string
	Access     string
	Internet   bool
	Allowed    []string
	PublicRPS  int
	LogDays    int
	DataCopies int
	FollowMain bool
}

// settingsOf reads a project's hosting settings. A project that never set its access takes the
// machine's hosting.default_access (19.4).
func settingsOf(ctx context.Context, q db.Querier, project, name, key string) (Settings, error) {
	d, err := settings.EffectiveProject(ctx, q, project, name, key)
	if err != nil {
		return Settings{}, err
	}
	h, _ := d["hosting"].(settings.Doc)
	st := Settings{
		Dockerfile: str(h["dockerfile"]), Port: num(h["port"]), Health: str(h["health"]), Data: str(h["data"]),
		Size: str(h["size"]), Access: str(h["access"]), PublicRPS: num(h["public_rps"]), LogDays: num(h["log_days"]),
		DataCopies: num(h["data_copies"]),
	}
	st.Internet, _ = h["internet"].(bool)
	st.FollowMain, _ = h["follow_main"].(bool)
	if list, ok := h["allowed"].([]any); ok {
		for _, e := range list {
			if s, ok := e.(string); ok {
				st.Allowed = append(st.Allowed, s)
			}
		}
	}
	own, err := settings.LoadProject(ctx, q, project)
	if err != nil {
		return Settings{}, err
	}
	if oh, _ := own["hosting"].(settings.Doc); oh == nil || oh["access"] == nil {
		v, err := settings.MachineValue(ctx, q, "hosting.default_access")
		if err != nil {
			return Settings{}, err
		}
		if a, ok := v.(string); ok && a != "" {
			st.Access = a
		}
	}
	return st, nil
}

func str(v any) string { s, _ := v.(string); return s }

// num reads a number of a settings document, which YAML gives as an int and JSON as a float64.
func num(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}
