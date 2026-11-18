# Vendored third-party skills — provenance manifest

Skills in the first table are vendored verbatim from the open skills.sh ecosystem
(SKILL.md standard). Before a skill is vendored or kept, its text is read for steps that
send code or project data to an external service (a "register", "publish" or telemetry
step); such a step is scoped to public or OSS work only, or the skill is not kept: a skill that sends a private project's code out is never safe to run. Update by re-fetching from the pinned source and bumping the entry
here; never edit vendored content in place (fork it into a differently-named skill
instead). The second table lists the one fork that predates that rule. The third table lists
vendored catalogs: a subtree of third-party skills inside a harness skill, reached through that
skill and never installed as skills themselves.

| Skill | Source | Pin | License | Vendored | Update via |
|-------|--------|-----|---------|----------|------------|
| codebase-design | github.com/mattpocock/skills (`skills/engineering/codebase-design`) | 6654f6b | MIT | 2026-08-31 | `npx skills@<version> add mattpocock/skills -s codebase-design` |
| find-skills | github.com/vercel-labs/skills (`skills/find-skills`) | 435076e | MIT | 2026-08-31 | `npx skills@<version> add vercel-labs/skills -s find-skills` |
| prompt-audit reference (one file, not a skill: `review-standards/references/prompt-audit/prompt-audit.md`, with `LICENSE.txt` beside it) | github.com/anthropics/skills (`skills/claude-api/shared/prompt-audit.md`) | 8a1541c (sha256:c33200da…1878f2) | Apache-2.0 | 2026-09-29 | `curl -sL https://raw.githubusercontent.com/anthropics/skills/<sha>/skills/claude-api/shared/prompt-audit.md`, then bump the pin and digest here |

## Apache-2.0 skills — forks, kept under their upstream names

`gke-basics` is NOT verbatim: it was rewritten here from an upstream base and keeps the
upstream name. Its provenance was "not recorded when vendored" until the project review
of 2026-09-30 (security finding 4) traced it by diffing against upstream's history
(`git archive <commit> <path> | diff -r`). It declares its licence and publisher in its
own `SKILL.md` frontmatter, which is the authority for attribution. A re-fetch from the
pin is a diff to read, never a copy to apply over the local text. Two skills that used to
sit here are gone: `terraform` was rewritten clean-room for GCP on 2026-10-01 and is the harness's own skill with no upstream text left; and
`gcloud-auth-verification` by google (v1, Apache-2.0), was merged into `gcloud-setup` on
2026-09-13 — its three rules paraphrased under "Before asking the user to log in" — and
removed.

| Skill | Publisher / author | Derived from | Pin | Local changes | License |
|-------|--------------------|--------------|-----|---------------|---------|
| gke-basics | Google Cloud | github.com/google/skills (`skills/cloud/gke-basics`, the one skill it was before upstream split it into `gke-*` sub-skills at 5d9d054 on 2026-06-23) | 6f0b877 (2026-04-23; the tree is unchanged upstream through 2026-05-10) | `SKILL.md` only: the description rewritten with this harness's cross-references (`cloud-run-deploy`, `docker`), a "When NOT to use" line added, the closing pointer to the Developer Knowledge MCP replaced by the GKE docs URL; a Harness overrides paragraph in SKILL.md (gcloud over MCP, one project question, the two dangling asset paths named, the lab-only `deletion_protection`); `references/` and `assets/` verbatim | Apache-2.0 |

Apache-2.0 and MIT permit redistribution with attribution and a copy of the applicable
licence text. `gke-basics/LICENSE`, `codebase-design/LICENSE`, and `find-skills/LICENSE`
ship in those skill directories alongside `review-standards/references/prompt-audit/LICENSE.txt`
and `google-catalog/catalog/LICENSE-*`.

## Vendored catalogs

| Catalog | Source | Pin | License | Vendored | Update via |
|---------|--------|-----|---------|----------|------------|
| google-catalog/catalog | github.com/google/agents-cli (`skills/`, `extensions/langchain/template/.agents/skills`) and github.com/google/skills (`skills/cloud`, `skills/developers`); entry counts per source, exclusions and per-entry security notes in `google-catalog/catalog/MANIFEST.md` | 2c39459 and d5d9052 | Apache-2.0 | 2026-10-01 | A Crew ticket; see the next paragraph |

The refresh script, `google-catalog/scripts/refresh-catalog.sh`, writes `INDEX.md` and `CHECKSUMS`
matching the checksum verification contract used to audit and pin the catalog subtree byte for
byte at the upstream pins (short ids in the table above; full 40-digit commit ids in the script
and manifest, so git never resolves an ambiguous tag or branch name),

refuses what the harness cannot carry (a symlink, a Go file, a dotfile, a name in two source sets, an
entry without frontmatter, a directory name that is not a plain skill name) until the operator passes
`--exclude <entry>`, keeps every entry named under the manifest's `## Excluded` out and that section
and `## Notes` byte for byte, replaces only a destination that is a catalog (never `/`, the home
directory, the checkout or an ancestor of either, in any spelling; a non-empty destination must hold
`CHECKSUMS`, and only the directories that file names are removed), clones into a work directory
that is its own (never `/`, the home directory or the checkout, never nested with the destination;
one that already holds files must carry the script's `.refresh-catalog-work` marker or an
`origins.txt`), takes a source override only as an `https://` URL (no user information, no query string) or an
absolute path, never shows or records such a value (the manifest names the upstream repositories; the
pins identify the content; git's own output, tracing and credential prompts (terminal and askpass) are off for the script's calls; while git runs the value is still one of its arguments in the process list, so a token belongs in a credential helper) and says, whether the run succeeds or fails after the first clone, that git
keeps the value in the work clones, and writes
`INDEX.md` and
`CHECKSUMS` through the Go tests, which then pin the tree both ways (`TestCatalogMatchesChecksums`,
`TestCatalogIndexMatchesEntries`, `TestCatalogFilesAreCarryable`). Installed as global skills
these skills push the harness's own out of both CLIs' skill lists
(measured 2026-10-01: Claude Code shows descriptions for about 19,800 bytes of skills and bare names beyond that), so they stay behind the
`google-catalog` router. A refresh's diff gets a security review before it is committed.

Notes:

- `grill-me` (a launcher for `grilling`) and `skill-repair` (repairs a `skills`-CLI manifest the
  harness does not use) were pruned 2026-09-03 after the sandbox matrix: `/grilling` does the same
  job on both CLIs.
- `grilling` (github.com/mattpocock/skills at 6654f6b, MIT) is not in Crew's harness: a Crew stage is what it describes, so its content moves into the stage prompts (design 17.3).
- `find-skills` is verbatim again at `435076e` (restored 2026-09-28). In Crew, a skill a project takes from outside is recorded in that project's `.agents/skills/VENDORED.md` (the global context's § Project layout).
- The UIZZE UI skill (uizze.com, v1.2.13, formerly a row above) and the Python skill were removed on
  2026-10-01 by the operator's decision: the first duplicated `frontend` and needed a paid
  MCP, and its LICENSE file carried Apache-2.0 text under an MIT label; the stack has no Python.
- `codebase-design` (`references/DESIGN-IT-TWICE.md`) names a `CONTEXT.md`: that is upstream's
  sibling file, not a file this harness has or writes; the line is vendored verbatim, read it as
  "the project's own domain vocabulary (its `AGENTS.md`)".
- `mermaid-syntax` has been in this repository since its first commit with no recorded upstream;
  its frontmatter `metadata.version: 1.5.0` suggests one. It is edited here as the harness's own
  skill (the 2026-10-01 edit moved its examples to frontmatter `config:` and `packet`), so it is
  not vendored and carries no row; if an upstream is ever identified, fork it under another name
  rather than re-fetching over these edits.
- To add a skill to the base: a Crew ticket copies a directory holding a `SKILL.md` into
  `harness/skills/` as files, pinned to a commit (fetch it with `npx skills@<version> add <owner/repo> -s <skill>`
  into a scratch directory, pin the CLI's version and read its prompt; never `-y`), and adds a row above.
  The image build puts it in place for both CLIs (`crew-harnessbuild`). A skill only one project
  needs goes into that project's `.agents/skills/` instead (design 12.9).
