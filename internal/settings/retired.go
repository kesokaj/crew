package settings

import "strings"

// retired are the settings that are no longer settings: since 0.53, when every limit in dollars and
// every cost estimate went (7.7, 12.7), and since 0.79 the two that left gcloud out, when no run and no
// conversation gets it (12.6, AGENTS.md safety rule 8). A document saved, a file imported, or a stored
// doc read that still holds one has it taken out, so a file written before reads, and so does a doc an
// older server stored after a roll back; schema steps 62 and 78 took them out of the stored settings.
var retired = []string{
	"budget_usd", "limits.run_budget_usd", "limits.default_task_budget_usd", "knowledge.housekeeping_budget_usd",
	"knowledge.evals.budget_usd", "knowledge.evals.max_cost_increase", "improvements.budget_usd_per_week",
	"prices", "agents.auto.cheaper_share",
	"sandbox.credentials", "conversations.credentials",
	"lead_queue", "max_subagent_runs",
}

// DropRetired takes the retired settings out of d and migrates retired "lead" role entries to "curator".
func DropRetired(d Doc) {
	if ls, ok := d["lead_slots"].(Doc); ok {
		if _, hasMax := d["max_concurrent_runs"]; !hasMax {
			if v, ok := ls["agy"]; ok {
				d["max_concurrent_runs"] = v
			}
		}
		delete(d, "lead_slots")
	}
	for _, path := range retired {
		parts := strings.Split(path, ".")
		at := d
		for _, p := range parts[:len(parts)-1] {
			next, ok := at[p].(Doc)
			if !ok {
				at = nil
				break
			}
			at = next
		}
		if at != nil {
			delete(at, parts[len(parts)-1])
		}
	}
	if agents, ok := d["agents"].(Doc); ok {
		if roles, ok := agents["roles"].(Doc); ok {
			if lead, ok := roles["lead"]; ok {
				if _, has := roles["curator"]; !has {
					roles["curator"] = lead
				}
				delete(roles, "lead")
			}
		}
		if auto, ok := agents["auto"].(Doc); ok {
			if set, ok := auto["set_by_you"].([]any); ok {
				for i, v := range set {
					if v == "lead" {
						set[i] = "curator"
					}
				}
			}
		}
		if lead, ok := agents["lead"]; ok {
			if _, has := agents["curator"]; !has {
				agents["curator"] = lead
			}
			delete(agents, "lead")
		}
	}
	if stages, ok := d["stages"].([]any); ok {
		for _, raw := range stages {
			st, ok := raw.(Doc)
			if !ok {
				continue
			}
			if st["id"] == "analysis" && st["role"] == "lead" {
				st["role"] = "curator"
			}
		}
	}
}
