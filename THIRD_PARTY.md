# Third-Party Notices and Provenance

Crew is distributed under the [Apache License, Version 2.0](LICENSE).

## Author-Adapted Components (Released Under Apache-2.0)

Several subsystems in Crew were originally copied as source files (without commit history) from the
author's prior internal projects (`styrman-crew` at `5c9e6fb`, `styrman-harness` at `43faca0`, and
the author's `fyr` harness) and subsequently modified and extended for Crew (design Section 17).
The author explicitly grants rights to all author-authored code, scripts, harness context, rules,
agents, and skills listed below under Crew's outbound **Apache-2.0** license (`LICENSE`).

| Part | Where it is | Provenance | Outbound Licence |
| --- | --- | --- | --- |
| The sandbox orchestrator and cut-down config loader | `internal/orchestrator/`, `internal/config/` | Adapted from author's `styrman-crew` `5c9e6fb` (same paths) | Apache-2.0 |
| The credential store and broker (rewritten in M2 for Crew's DB/tokens; value/file/git-credential delivery kept) | `internal/credstore/`, `internal/credbroker/` | Adapted from author's `styrman-crew` `5c9e6fb` (same paths) | Apache-2.0 |
| Setup identity helpers (`git.go` reader, `stripControls`, public key check) | `internal/setup/` | Adapted from author's `styrman-crew` `5c9e6fb` (`setup.go`, `credharvest.go`, `credentials.go`, `git.go`, `server.go`) | Apache-2.0 |
| Host setup scripts and helper library | `deploy/host-setup.sh`, `deploy/lib/` | Adapted from author's `styrman-crew` `5c9e6fb` (same paths) | Apache-2.0 |
| Host firewall rules and units | `deploy/firewall/` | Adapted from author's `styrman-crew` `5c9e6fb` (`deploy/firewall/`, renamed `crew-firewall.*`) | Apache-2.0 |
| Caddy ingress module (`ingress/`), simplified to Crew's fixed routes | `ingress/` (`ingress/Dockerfile`) | Adapted from author's `styrman-crew` `5c9e6fb` (`deploy/caddy/ingress/` and `deploy/caddy/Dockerfile`) | Apache-2.0 |
| Sandbox runtime container definition | `image/` | Adapted from author's `styrman-crew` `5c9e6fb` (`image/`, renamed `configs/crew-*`) | Apache-2.0 |
| Redaction needles (`internal/redact/needles.go`) | `internal/redact/needles.go` | Adapted from author's `styrman-harness` `43faca0` (`internal/harness/report.go`) | Apache-2.0 |
| Agent harness context, rules (`critical-thinking`, `code-modularity`), seven subagents, craft skills (`frontend-ui-modern`, `frontend-ui-economist`, etc.), and builder | `harness/`, `internal/harnessbuild/` | Adapted from author's `styrman-harness` `43faca0` (2.7.0) and author's `fyr` harness | Apache-2.0 |

Note: Third-party vendored skills inside `harness/skills/` keep their upstream open-source licences
listed below.

---

## Third-Party Skills, Palettes, and Prompt Assets

| Component | Where it is | Upstream Source & Pin | Licence |
| --- | --- | --- | --- |
| Skill `codebase-design` (with local `LICENSE`) | `harness/skills/codebase-design/` | `github.com/mattpocock/skills` (`6654f6b`, `skills/engineering/codebase-design`) | MIT |
| Skill `find-skills` (with local `LICENSE`) | `harness/skills/find-skills/` | `github.com/vercel-labs/skills` (`435076e`, `skills/find-skills`) | MIT |
| Prompt-audit reference in `review-standards` (with local `LICENSE.txt`) | `harness/skills/review-standards/references/prompt-audit/` | `github.com/anthropics/skills` (`8a1541c`, `skills/claude-api/shared/prompt-audit.md`) | Apache-2.0 |
| Skill `gke-basics` (forked `SKILL.md`, verbatim `references/` & `assets/`, with local `LICENSE`) | `harness/skills/gke-basics/` | `github.com/google/skills` (`6f0b877`, `skills/cloud/gke-basics`) | Apache-2.0 |
| Google skills catalog behind `google-catalog` (with `LICENSE-agents-cli` & `LICENSE-google-skills`) | `harness/skills/google-catalog/catalog/` | `github.com/google/agents-cli` (`2c39459`) and `github.com/google/skills` (`d5d9052`) | Apache-2.0 |
| Built-in UI color palettes (`themes-40.css`, `crew-simulation.html`) | `web/src/styles/themes-40.css`, `docs/plan/simulation/` | CSS token values adapted for the shadcn/ui contract from Catppuccin, Tokyo Night, Dracula, One Dark, GitHub Primer, Gruvbox, Nord, Rose Pine, Kanagawa, Solarized, Everforest, Ayu, and VS Code Monokai | MIT / Expat / permissive upstream palettes |

Note on `google-catalog/catalog/dbt-sf-to-bq-translator`: This directory is vendored verbatim
from the Apache-2.0 `github.com/google/skills` snapshot (`d5d9052`, covered by `LICENSE-google-skills`).
Two upstream Python scripts in that skill (`bulk_translate_via_gcloud.py` and `dbt_translator.py`, as
well as the header `dbt_translator.py` writes onto translated SQL files) carry an upstream
`# Copyright 2026 Google ... subject to your agreement with Google` file header inside the Apache-2.0
upstream snapshot; it is kept unmodified here so `google-catalog/catalog/CHECKSUMS` continues to
verify byte-for-byte upstream integrity.

---

## Direct Go, Ingress, Web, and Test Dependencies

Direct dependencies declared in `go.mod`, `ingress/go.mod`, `ingress/Dockerfile`,
`web/package.json`, and `tools/e2e/browser/package.json` (lockfiles in `go.sum`, `ingress/go.sum`,
`web/package-lock.json`, and `tools/e2e/browser/package-lock.json` pin all transitive dependencies):

### Backend Go Module (`go.mod`)

| Dependency | Version | Purpose | Licence |
| --- | --- | --- | --- |
| `github.com/coder/websocket` | `v1.8.15` | WebSocket transport for terminals and live updates | ISC |
| `github.com/docker/docker` | `v28.5.2+incompatible` | Container and microVM lifecycle client | Apache-2.0 |
| `github.com/golang-jwt/jwt/v5` | `v5.3.0` | GitHub App JWT signing | MIT |
| `github.com/jackc/pgx/v5` | `v5.11.0` | PostgreSQL driver and pool | MIT |
| `github.com/opencontainers/image-spec` | `v1.1.1` | OCI image manifest/config handling | Apache-2.0 |
| `go.yaml.in/yaml/v3` | `v3.0.5` | YAML parsing for specs, tickets, and config | MIT & Apache-2.0 |
| `golang.org/x/sys` | `v0.49.0` | Linux syscall helpers | BSD-3-Clause |

### Ingress Module & Image (`ingress/go.mod`, `ingress/Dockerfile`)

| Dependency | Version | Purpose | Licence |
| --- | --- | --- | --- |
| `github.com/caddyserver/caddy/v2` | `v2.11.7` | Core reverse proxy and ACME server | Apache-2.0 |
| `github.com/greenpau/caddy-security` | `v1.1.64` | Google OAuth portal and session enforcement | Apache-2.0 |
| `github.com/caddy-dns/cloudflare` | `v0.2.4` | DNS-01 ACME challenge provider | MIT |
| `github.com/mholt/caddy-ratelimit` | `v0.1.0` | HTTP rate limiting middleware | Apache-2.0 |

### Web Control Plane (`web/package.json`)

| Dependency | Version (`package-lock.json`) | Licence |
| --- | --- | --- |
| `react`, `react-dom` | `19.2.8` | MIT |
| `@tanstack/react-router` | `1.170.46` | MIT |
| `@tanstack/react-start` | `1.169.5` | MIT |
| `@xterm/xterm` (`@xterm/addon-fit`) | `6.0.0` (`0.11.0`) | MIT |
| `react-markdown`, `remark-gfm` | `10.1.0`, `4.0.1` | MIT |
| `tailwindcss`, `@tailwindcss/vite` | `4.3.3` | MIT |
| `vite`, `@vitejs/plugin-react` | `8.3.8`, `6.1.0` | MIT |
| `vitest` | `4.1.12` | MIT |
| `typescript` | `6.0.3` | Apache-2.0 |
| `@types/node`, `@types/react`, `@types/react-dom` | `24.13.3`, `19.2.18`, `19.2.5` | MIT |

### Browser E2E Harness (`tools/e2e/browser/package.json`)

| Dependency | Version (`package-lock.json`) | Licence |
| --- | --- | --- |
| `@playwright/test` | `1.64.0` | Apache-2.0 |
