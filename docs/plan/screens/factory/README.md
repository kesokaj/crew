# Crew Software Factory Screens (Design 1.47)

Local screenshot suite captured at design version `1.47` across `docs/plan/simulation/ui-sim/index.html` (`Direction A` inset sidebar + **all 40 shipping themes** + `Skills Registry`) and `docs/plan/simulation/crew-simulation.html` (`#<project>` channel + per-feature threads + autonomous `@agent -> @planner` feature decisions).

| File | Surface | Theme / Mode / Width | What it demonstrates |
|---|---|---|---|
| `01-factory-line-dark.png` | Factory Line | Catppuccin Mocha · Dark · `1440px` | Two-tier specs -> `Tasks API` -> `@coordinator` -> `@builder` -> cross-family checks -> `main`, plus `max_tries -> @researcher` circuit breaker and Trajectory chart |
| `02-factory-line-light.png` | Factory Line | Catppuccin Latte · Light · `1440px` | Light-mode counterpart of the primary Factory Line control plane |
| `03-tasks-board-dark.png` | Tasks API (Board) | Catppuccin Mocha · Dark · `1440px` | 5-column code Kanban paired with Off-Board Tracked Tasks ledger (`spec`, `research`, `dream`, `chat` spawns — zero untracked work `R217`) |
| `04-task-detail-yaml-dark.png` | Task Detail & YAML | Catppuccin Mocha · Dark · `1440px` | Full `.crew/tickets/<KEY>.md` YAML frontmatter, `tries / max_tries`, sandbox checks, and linked `#crew` thread |
| `05-specs-workflow-planner-dark.png` | Specs & `WORKFLOW.md` | Catppuccin Mocha · Dark · `1440px` | Tier 1 (`000-foundation.md`) + Tier 2 Feature Specs showing autonomous `@builder -> @planner` decision `DEC-041` alongside `main`-pinned `WORKFLOW.md` (`13.8`) |
| `06-chat-project-channel-feature-threads-dark.png` | Project Chat (`#crew`) | Catppuccin Mocha · Dark · `1440px` | Unified `#crew` project channel and per-feature threads (`feat/004-knowledge-api`, `feat/003`, `feat/005`) with shared agent chatter and autonomous `@planner` decisions |
| `07-chat-project-channel-feature-threads-light.png` | Project Chat (`#crew`) | Catppuccin Latte · Light · `1440px` | Light-mode view of `#crew` channel & per-feature threads |
| `08-knowledge-api-dark.png` | Cross-Project Knowledge API | Catppuccin Mocha · Dark · `1440px` | Installation-wide secret-scrubbed (`internal/redact`) & fenced (`internal/fence`) shared patterns and bug advisories |
| `09-skills-registry-dark.png` | Skills Registry (`12.11`) | Catppuccin Mocha · Dark · `1440px` | Four-tier catalog (`Base`, `Cross-Factory Shared`, `Dreamed`, `Vendored` SHA-pinned `VENDORED.md`), static AST vetting, warm-VM sync, and live `SKILL.md` inspector |
| `10-skills-registry-light.png` | Skills Registry (`12.11`) | Catppuccin Latte · Light · `1440px` | Light-mode Skills Registry view |
| `11-memory-api-dreamstore-dark.png` | Memory API (Dreamstore) | Catppuccin Mocha · Dark · `1440px` | Typed `episodic` / `semantic` / `procedural` memory with `0.00`–`1.00` commit-drift validity scoring & Scoped Dreaming gate |
| `12-agent-api-acp-dark.png` | Agent API (`agy acp`) | Catppuccin Mocha · Dark · `1440px` | Multi-session ACP peer sessions (`1 session = 1 peer agent`, CLI subagents disabled, open `pi`/`claude` adapter seam) |
| `13-runtimes-storage-dark.png` | ComputeRuntime & Storage | Catppuccin Mocha · Dark · `1440px` | Kata + Cloud Hypervisor `PauseSandbox` / `UnpauseSandbox` / sealed `.tar.gz` snapshots, K8s seam, and Split Storage (`PostgreSQL` / `SQLite` WAL + `Local FS` / `GCS`) |
| `14-theme-swatch-gallery-40-dark.png` | Theme Swatch Gallery | All `40` Shipping Themes · `1440px` | Interactive theme picker grouping `11` Product & AI-Native, `9` official `shadcn/ui` presets, and `20` Dotfile classics shipped in `M25` |
| `15-theme-linear-dark.png` | Factory Line | Linear (Graphite & Indigo) · Dark · `1440px` | Production Product/AI-Native theme example (`linear`) |
| `16-theme-claude-light.png` | Factory Line | Claude (Warm Paper) · Light · `1440px` | Production Product/AI-Native theme example (`claude`) |
| `17-theme-warp-dark.png` | Factory Line | Warp (Obsidian & Iris) · Dark · `1440px` | Production Product/AI-Native theme example (`warp`) |
| `18-theme-shadcn-zinc-blue-light.png` | Factory Line | shadcn Zinc · Blue · Light · `1440px` | Official `shadcn/ui` preset example |
| `19-mobile-390-chat-feature-threads-dark.png` | Mobile Responsive (`390px`) | Catppuccin Mocha · Dark · `390px` | Mobile drawer navigation + `#crew` & feature threads stacked cleanly with zero horizontal overflow |
| `20-state-sim-project-channel-autonomous-planner.png` | State-Machine Simulator | Linear · Dark · `1440px` | Live state simulator (`crew-simulation.html`) showing `#storefront-api` feature threads and autonomous `@builder -> @planner` spec approval (`Ask @planner & Decide Autonomously`) |
