# UI decision — Crew Software Factory

Status: DECIDED (locked 2026-11-16: Direction A inset sidebar + ship all 40 runtime-switchable themes)

| Choice | Value | Why |
| --- | --- | --- |
| Direction | A — Inset sidebar control plane (also comparing B Top Command Bar, C Split Ops + Docked Slack Rail) | `sidebar-07` inset keeps multi-factory switching, 7-module grouping (Tasks/Knowledge/Memory/Agent/Runtimes), and live Kata + Coordinator gauges readable without crowding wide Factory Line / Kanban views. |
| Theme | All 40 themes shipped at runtime (default: `catppuccin` dark; switchable live via `Themes (40)` Swatch Gallery &amp; persisted per operator) | Warm semantic tokens distinguish Factory stages, ACP agent states, and Dreamstore validity scores cleanly in dark and light modes, while giving the operator runtime choice across all 40 curated palettes (`11` Product/AI, `9` official `shadcn/ui`, `20` Dotfile). |
| Default mode | dark | Matches terminal/IDE engineer workflow and Crew's existing operator surfaces while keeping full light mode toggle (`d`). |
| Style (shadcn/create) | nova (compact) | Crisp dense spacing suited for YAML specs, task ledgers, and multi-column Kanban without cramped typography. |
| Radius | default (0.625rem / --radius-md) | Balanced cards and badges with clean separation in inset chrome. |
| Base library | base (`@base-ui/react`) | Installed target for `shadcn@4.23.0` (`web/package.json` uses Vite 8 + React 19 + TanStack Router + Tailwind v4). |
| Icon library | lucide (`lucide-react`) | Consistent 2px stroke iconography matching shadcn blocks and simulation SVGs. |
| Fonts | body: System/Inter, heading: System/Inter, mono: ui-monospace | Fast zero-dependency font stack matching Go/TS operator tooling and YAML contracts. |
| Layout blocks | `sidebar-07` (`variant="inset"`), `dashboard-01` stat grid + area/bar/line/donut/radial Recharts (`recharts@3.8`), `tasks` faceted table, `sheet`/`dialog`/`command` | Maps directly to `Factory Line`, `Tasks API Board`, `Task Detail`, `Specs & WORKFLOW`, `Project Chat (#crew)`, `Knowledge API`, `Skills Registry`, `Memory API`, `Agent API`, and `Runtimes & Storage`. |
| Chart palette | `--chart-1..5` from active theme (40-theme catalog) | Consistent visual contrast across light/dark transitions. |
| Extras | `success` / `warning` / `info` semantic tokens: yes | Required for verified/drift/quarantined Dreamstore & Skills Registry scores, `max_tries` circuit breaker, and Kata suspend/resume states. |

Screens in scope: `factory-line`, `tasks-board`, `task-detail`, `specs-workflow`, `chat-rooms`, `knowledge-api`, `skills-registry`, `memory-api`, `agent-api`, `runtimes-storage` (`normal` / `loading` / `empty` / `error`), plus 7 overlays (`command`, `themes`, `dialog`, `confirm`, `sheet`, `toast`, `mobile-nav`). Screenshot suite: `docs/plan/screens/factory/`.
Out of scope for this round: Production React component wiring inside `web/src/` (planned across milestones M24–M25).
Resolved decisions:
1. Shell locked to **Direction A (`sidebar-07` inset collapsible control plane)** with responsive mobile drawer (`390px`).
2. **All 40 themes** (`11` Product &amp; AI-Native, `9` official `shadcn/ui` presets, `20` Dotfile &amp; Editor classics) + light/dark toggle ship in `web/` (`M25`) so the operator can switch anytime.
3. Contrast notes:
   - All 11 new Product & AI-Native themes (`linear`, `claude`, `vercel`, `warp`, `supabase`, `vesper`, `flexoki`, `oxocarbon`, `poimandres`, `night-owl`, `synthwave`) verified `>= 4.5:1` WCAG AA across `foreground/bg`, `foreground/card`, `muted-foreground/bg`, `primary-foreground/primary`, and `sidebar-foreground/sidebar` in both light and dark modes.
   - `github`: all pairs PASS in both Light (`15.80:1` fg/bg) and Dark (`17.39:1` fg/bg).
   - `catppuccin`: Latte passes all pairs (`7.06:1`); Mocha passes all primary/fg pairs (`11.34:1`) with 1 minor `muted-foreground on muted` warn (`4.10:1` vs `4.5` target — either accept or pair with `github` dark/high-contrast).
   - `tokyo-night`: Night passes all pairs (`11.14:1`), Day has 3 contrast warnings (`muted-foreground` and `primary-foreground`).
   - `nord`: 1 warn per mode (`primary-foreground on primary` `3.50:1` light, `muted-foreground on muted` `4.19:1` dark).
