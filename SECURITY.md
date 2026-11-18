# Security Policy

## Supported Versions

Security fixes land on `main` and ship as versioned Crew releases built from [versions.lock](versions.lock). Only the latest release on the `2.x` line receives security updates.

## Reporting a Vulnerability

Please report security vulnerabilities privately via [GitHub Private Vulnerability Reporting](https://github.com/kesokaj/crew/security/advisories/new).

Do **not** open a public GitHub issue or pull request containing exploit details, credentials, or reproduction payloads against a live host.

When reporting a vulnerability, include:

- Component and file path (or container service / sandbox boundary) affected
- Steps or a minimal test case to reproduce the issue
- Assessed impact against Crew's isolation model

## Security Architecture

Crew runs agent tasks, check commands, and hosted applications inside isolated Kata microVMs and unprivileged containers configured under these invariants:

- **Workspace & network isolation:** Per-project warm Kata microVMs and throwaway check sandboxes run under non-root `agent` without Docker socket access (`crew-server` and `crew-updater` alone access the Docker socket). Sandboxes are restricted by the `CREW-*` firewall chains on `sdb-`, `mdl-`, `src-`, `app-`, and `apo-` bridges.
- **Server-controlled state transitions:** Ticket transitions, canonical `main` merges, PostgreSQL writes, and release gates run strictly on `crew-server` and `crew-updater` (never from agent-side claims).
- **Scoped credentials & secret redaction:** GitHub/SSH/App credentials remain on the server; sandboxes receive short-lived project-scoped read-only GitHub tokens and isolated agent logins. Secrets and encrypted stores reside outside the database keyfile boundary under `/etc/crew/` with `AES-GCM` encryption and outbound `internal/redact` screening.
- **Unprivileged host separation:** See [AGENTS.md](AGENTS.md) (`Rules that keep the operator safe`) and [docs/install-and-uninstall.md](docs/install-and-uninstall.md) for the complete operational boundary.
