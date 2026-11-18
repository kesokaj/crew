package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	harnesstree "github.com/kesokaj/crew/harness"
	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/runs"
	"github.com/kesokaj/crew/internal/skills"
)

func (s *Server) getFactoryLine(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	ctx := r.Context()
	if err := s.syncProjectTrajectories(ctx, b.Pool, s.ensureWAL(), p); err != nil {
		s.Log.Warn("syncing trajectories for factory line", "project", p.Name, "error", err)
	}
	specs, err := b.ListSpecs(ctx, p)
	if err != nil {
		s.boardError(w, "listing specs", err)
		return
	}
	decisions, err := b.ListSpecDecisions(ctx, p)
	if err != nil {
		s.boardError(w, "listing spec decisions", err)
		return
	}
	if len(decisions) > 10 {
		decisions = decisions[:10]
	}
	tasks, err := b.ListFactoryTasks(ctx, p.ID, "", "")
	if err != nil {
		s.boardError(w, "listing factory tasks", err)
		return
	}
	briefs, err := b.ListResearchBriefs(ctx, p, 0, "")
	if err != nil {
		s.boardError(w, "listing research briefs", err)
		return
	}
	stageRows, err := b.Pool.Query(ctx, `SELECT stage, count(*)::int FROM tickets WHERE project = $1 GROUP BY stage`, p.ID)
	if err != nil {
		s.boardError(w, "counting stages", err)
		return
	}
	stageCounts := map[string]int{}
	for stageRows.Next() {
		var st string
		var cnt int
		if ser := stageRows.Scan(&st, &cnt); ser != nil {
			stageRows.Close()
			s.boardError(w, "scanning stage counts", ser)
			return
		}
		stageCounts[st] = cnt
	}
	stageRows.Close()
	if rerr := stageRows.Err(); rerr != nil {
		s.boardError(w, "reading stage counts", rerr)
		return
	}

	var sessions []runs.PeerSession
	if drv := s.Runs.Load(); drv != nil {
		sessions, _ = drv.ListPeerSessions(ctx, p.Name, false)
	}
	if sessions == nil {
		sessions = []runs.PeerSession{}
	}
	if len(sessions) > 12 {
		sessions = sessions[:12]
	}

	trajRows, err := b.Pool.Query(ctx, `SELECT run_id::text, role, model, events_count, tokens_total, duration_seconds, outcome, blob_key, started_at
		FROM trajectories WHERE project_id = $1 ORDER BY started_at DESC LIMIT 12`, p.ID)
	if err != nil {
		s.boardError(w, "reading factory trajectory series", err)
		return
	}
	type trajPoint struct {
		RunID    string    `json:"runId"`
		Role     string    `json:"role"`
		Model    string    `json:"model"`
		Events   int64     `json:"events"`
		Tokens   int64     `json:"tokens"`
		Duration int       `json:"durationSeconds"`
		Outcome  string    `json:"outcome"`
		BlobKey  string    `json:"blobKey,omitempty"`
		At       time.Time `json:"at"`
	}
	trajSeries, err := pgx.CollectRows(trajRows, func(row pgx.CollectableRow) (trajPoint, error) {
		var x trajPoint
		return x, row.Scan(&x.RunID, &x.Role, &x.Model, &x.Events, &x.Tokens, &x.Duration, &x.Outcome, &x.BlobKey, &x.At)
	})
	if err != nil {
		s.boardError(w, "scanning factory trajectory series", err)
		return
	}
	if trajSeries == nil {
		trajSeries = []trajPoint{}
	}

	var roomSummary map[string]any
	if rm := s.Rooms.Load(); rm != nil {
		if rv, rerr := rm.GetRoomView(ctx, p, 8); rerr == nil && rv != nil {
			roomSummary = map[string]any{
				"channel":      rv.Channel,
				"threadsCount": len(rv.Threads),
				"maxAgentHops": rv.MaxAgentHops,
				"messages":     len(rv.Messages),
			}
		}
	}
	if roomSummary == nil {
		roomSummary = map[string]any{"channel": "#" + p.Name, "threadsCount": 0, "maxAgentHops": 3, "messages": 0}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"project": map[string]any{
			"id":        p.ID,
			"name":      p.Name,
			"key":       p.Key,
			"goal":      p.Goal,
			"state":     p.State,
			"protected": p.Protected,
		},
		"stageCounts":      stageCounts,
		"specs":            specs,
		"recentDecisions":  decisions,
		"offBoardTasks":    tasks,
		"researchBriefs":   briefs,
		"sessions":         sessions,
		"roomSummary":      roomSummary,
		"trajectorySeries": trajSeries,
		"storage":          s.readStorageConfig(ctx, b.Pool),
	})
}

func (s *Server) getSkillsRegistry(w http.ResponseWriter, r *http.Request) {
	b, p := s.project(w, r)
	if p == nil {
		return
	}
	ctx := r.Context()
	layer, err := b.ProjectLayer(ctx, p)
	if err != nil {
		s.boardError(w, "reading project layer", err)
		return
	}
	custom, _, _ := board.CustomHarnessMap(ctx, b.Pool, "harness/")
	harnessCustom := map[string]string{}
	for pth, c := range custom {
		if len(pth) > len("harness/") {
			harnessCustom[pth[len("harness/"):]] = c
		}
	}
	baseItems := harnesstree.BaseWithOverrides(harnessCustom)

	type registryEntry struct {
		ID                 string   `json:"id"`
		Tier               string   `json:"tier"`
		Name               string   `json:"name"`
		Path               string   `json:"path"`
		Project            string   `json:"project,omitempty"`
		Description        string   `json:"description"`
		Ticket             string   `json:"ticket,omitempty"`
		SourceURL          string   `json:"sourceUrl,omitempty"`
		PinnedSHA          string   `json:"pinnedSha,omitempty"`
		Licence            string   `json:"licence,omitempty"`
		ScanVerdict        string   `json:"scanVerdict"`
		ScanFindings       []string `json:"scanFindings,omitempty"`
		ValidityScore      float64  `json:"validityScore,omitempty"`
		FirstPassDelta     float64  `json:"firstPassDelta,omitempty"`
		InstalledFactories []string `json:"installedFactories,omitempty"`
		PromptBytes        int      `json:"promptBytes"`
		TokenFootprint     int      `json:"tokenFootprint"`
	}
	var entries []registryEntry
	tierCounts := map[string]int{"base": 0, "shared": 0, "project": 0, "dream": 0, "vendored": 0}

	for _, it := range baseItems {
		if it.Kind != "skill" {
			continue
		}
		pb := len(it.Description) + 64
		entries = append(entries, registryEntry{
			ID: "base:" + it.Name, Tier: "base", Name: it.Name, Path: "skills/" + it.Name + "/SKILL.md",
			Description: it.Description, ScanVerdict: "PASS", PromptBytes: pb, TokenFootprint: max(1, pb/4),
		})
		tierCounts["base"]++
	}

	for _, it := range layer {
		if it.Kind != "skill" {
			continue
		}
		tier := "project"
		if strings.Contains(strings.ToLower(it.Description), "dream") || strings.Contains(strings.ToLower(it.Name), "dream") {
			tier = "dream"
		}
		tk := ""
		if it.Ticket != nil {
			tk = *it.Ticket
		}
		pb := len(it.Description) + 64
		entries = append(entries, registryEntry{
			ID: tier + ":" + p.Name + ":" + it.Name, Tier: tier, Name: it.Name, Path: it.Path,
			Project: p.Name, Description: it.Description, Ticket: tk, ScanVerdict: "PASS",
			PromptBytes: pb, TokenFootprint: max(1, pb/4),
		})
		tierCounts[tier]++
	}

	rows, err := b.Pool.Query(ctx, `SELECT id::text, name, key FROM projects WHERE id <> $1 AND state <> 'archived' AND NOT hidden ORDER BY name ASC`, p.ID)
	if err != nil {
		s.boardError(w, "listing sibling factory projects", err)
		return
	}
	type sibling struct{ id, name, key string }
	var siblings []sibling
	for rows.Next() {
		var x sibling
		if ser := rows.Scan(&x.id, &x.name, &x.key); ser != nil {
			rows.Close()
			s.boardError(w, "scanning sibling factory project", ser)
			return
		}
		siblings = append(siblings, x)
	}
	rows.Close()
	if rerr := rows.Err(); rerr != nil {
		s.boardError(w, "reading sibling factory projects", rerr)
		return
	}
	for _, sib := range siblings {
		sibLayer, serr := b.ProjectLayer(ctx, &board.Project{ID: sib.id, Name: sib.name, Key: sib.key})
		if serr != nil {
			continue
		}
		for _, it := range sibLayer {
			if it.Kind != "skill" {
				continue
			}
			pb := len(it.Description) + 64
			entries = append(entries, registryEntry{
				ID: "shared:" + sib.name + ":" + it.Name, Tier: "shared", Name: it.Name, Path: it.Path,
				Project: sib.name, Description: it.Description, ScanVerdict: "PASS",
				PromptBytes: pb, TokenFootprint: max(1, pb/4),
			})
			tierCounts["shared"]++
		}
	}

	vendoredMD := ""
	if vb, verr := b.Git.ReadBlob(ctx, p.Name, "main", ".agents/skills/VENDORED.md"); verr == nil {
		vendoredMD = vb.Content
		for _, line := range strings.Split(vendoredMD, "\n") {
			tr := strings.TrimSpace(line)
			if !strings.HasPrefix(tr, "|") || strings.Contains(tr, "---") || strings.HasPrefix(strings.ToLower(tr), "| skill") || strings.HasPrefix(strings.ToLower(tr), "| name") {
				continue
			}
			cols := strings.Split(strings.Trim(tr, "|"), "|")
			if len(cols) < 4 {
				continue
			}
			name := strings.TrimSpace(cols[0])
			src := strings.TrimSpace(cols[1])
			sha := strings.TrimSpace(cols[2])
			lic := strings.TrimSpace(cols[3])
			if name == "" {
				continue
			}
			entries = append(entries, registryEntry{
				ID: "vendored:" + p.Name + ":" + name, Tier: "vendored", Name: name,
				Path: ".agents/skills/" + name + "/SKILL.md", Project: p.Name,
				Description: "Vendored external skill pinned to commit " + sha, SourceURL: src, PinnedSHA: sha, Licence: lic,
				ScanVerdict: "PASS", PromptBytes: 128, TokenFootprint: 32,
			})
			tierCounts["vendored"]++
		}
	}

	if dbEntries, lerr := skills.ListByProject(ctx, b.Pool); lerr == nil {
		for _, dbE := range dbEntries {
			if (dbE.Tier == "project" || dbE.Tier == "dream") && dbE.SourceProjectID != nil && *dbE.SourceProjectID != p.ID {
				continue
			}
			matched := false
			for i := range entries {
				if entries[i].Tier == dbE.Tier && entries[i].Name == dbE.Name {
					entries[i].ScanVerdict = dbE.ScanVerdict
					entries[i].ScanFindings = dbE.ScanFindings
					entries[i].ValidityScore = dbE.ValidityScore
					entries[i].FirstPassDelta = dbE.FirstPassDelta
					entries[i].InstalledFactories = dbE.InstalledFactories
					if dbE.SourceURL != "" {
						entries[i].SourceURL = dbE.SourceURL
					}
					if dbE.PinnedSHA != "" {
						entries[i].PinnedSHA = dbE.PinnedSHA
					}
					if dbE.Licence != "" {
						entries[i].Licence = dbE.Licence
					}
					matched = true
					break
				}
			}
			if !matched {
				entries = append(entries, registryEntry{
					ID: dbE.Tier + ":" + dbE.Slug, Tier: dbE.Tier, Name: dbE.Name, Path: dbE.Path,
					Project: p.Name, Description: dbE.Description, SourceURL: dbE.SourceURL,
					PinnedSHA: dbE.PinnedSHA, Licence: dbE.Licence, ScanVerdict: dbE.ScanVerdict,
					ScanFindings: dbE.ScanFindings, ValidityScore: dbE.ValidityScore,
					FirstPassDelta: dbE.FirstPassDelta, InstalledFactories: dbE.InstalledFactories,
					PromptBytes: dbE.PromptBytes, TokenFootprint: dbE.TokenFootprint,
				})
				tierCounts[dbE.Tier]++
			}
		}
	}

	if entries == nil {
		entries = []registryEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries":          entries,
		"tierCounts":       tierCounts,
		"vendoredMarkdown": vendoredMD,
		"warmSyncPath":     "<CREW_DATA_DIR>/projects/" + p.ID + "/home/.gemini/config/skills/",
	})
}
