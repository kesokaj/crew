# Report of the spike (M0)

Final report of 2026-10-13. The answers, with the commands and the lines of output that show them, are in `answers.md`. The design is at version 0.33, which holds what the spike changed.

## How the spike ran

styrman-crew was uninstalled from dave at level 3 on 2026-10-12, before M0 began, with its project data. With the operator's approval the spike ran on dave as it was:

- its containers started from a stand-in for styrman-crew's runtime image, built from `5c9e6fb` by `build-standin.sh`, with `sh-agent` v2.7.0 built from styrman-harness `43faca0` in place of a download with a token. The image holds `agy` 1.3.4 and `claude` 2.1.287;
- the logins were the ones dave had: a copy of the operator's `agy` token file, and only the current access token of `claude`, as `CLAUDE_CODE_OAUTH_TOKEN`, because the `claude` login on dave is the one the building agent runs on. No `claude setup-token` was made;
- no firewall kept the spike's containers from the local network; the operator accepted that, and item 29 moved to M1;
- paid extra use was off in both accounts: `hasExtraUsageEnabled: false` and `overageStatus: rejected` for `claude`, `Remaining credits 0` for `agy`.

## Items

| Item | Answer |
| --- | --- |
| 1 | **No.** Ten `agy` runs used up the Gemini five-hour window in 33 minutes |
| 2 | Copies keep one refresh token; ten renewing at once made one fail once. Hours not reached |
| 3 | `agy` honours `HTTPS_PROXY`; six hosts |
| 5 | About 10 s from `docker run` to the first event; the sandbox is ready in 4 s |
| 6 | Docker's address pool stops at 29 sandboxes; idle a small one takes 190 MB, a large 800 MB; KSM shares nothing |
| 7 | Yes: `AGY_CLI_DISABLE_AUTO_UPDATE` stops the update |
| 8 | Yes, in the same sandbox and in a new one |
| 9 | Yes: `command(*)` settings, nothing refused |
| 10 | Yes; a failed MCP only removes its tools, and the agent then works around it |
| 11 | `agy models` is the check: 2.5 s, or exit 1 in 0.6 s when refused; a run waits 60 s |
| 12 | Yes: through the shared folder in about 30 ms, nothing lost |
| 13 | Advisory half: none reachable from a sandbox on the Go runtime. Kata 4 half: M1 |
| 14 | 0.9 MB per run-hour (`agy`), 4.6 MB (`claude`) |
| 15 | Yes: `step_type: subagent` with `subagent_info` |
| 16 | Moved to M1: needs Crew's own build target |
| 17 | As item 2 |
| 18 | A tmux terminal in a run's microVM does not disturb it, and keeps its session |
| 26 | A working run: 370 to 650 MB, a few percent of CPU |
| 27 | **The `agy` login has the `cloud-platform` scope** |
| 28 | Confirmed: workspace hooks and MCP load in `agy` without a question |
| 29 | Moved to M1: no firewall on dave until Crew's |
| 33 | Skills are read from `~/.gemini/antigravity-cli/skills/`, `~/.gemini/config/skills/`, and `.agents/skills/` |
| 35 | `agy` carries about one run without a break; `claude` about six |
| 37 | Yes, from 1.2.14 to 1.3.4 |
| 38 | `~/.gemini/antigravity-cli/conversations/<id>.db` |
| 39 | Usage is for the whole conversation; one open process answers a second turn in 1 to 2.4 s |
| 41 | Confirmed |
| 45 | One file: `conversations/<id>.db` |
| 47 | Ten names; **no `gemini-3.1-pro`** on the operator's account |
| 48 | A conflicting `--effort` is refused before any request |
| 49 | Claude models through `agy` behave the same |
| 50 | `/usage` in JSON works; at the limit: exit code 3, `AGY_ERROR` with `RESOURCE_EXHAUSTED` |
| 51 | Yes, on another model |
| 52 | Both prompt forms work; both together are refused with exit code 2 |
| 53 | Process 1's output is an append-only copy outside the sandbox |
| 54 | Yes: at the limit the run stops and spends nothing |
| 56 | `tokeninfo` names the account; the file's `id_token` is not proof |
| 57 | 12 cores with 16 threads, 62 GiB, 1 TB |
| 58 | Yes, on the other family inside `agy` |
| 60 | A loop image inside the guest bounds accidents, not an agent; a hard bound needs quotas on the machine |
| 61 | The token file and a settings file are enough |
| 62 | None of the operator's secrets or ids appear in what the CLIs print |
| 63 | dave's versions agree with `versions.lock`, which now has its M0 checksums |
| 65 | `claude` honours `HTTPS_PROXY`; two hosts |
| 66 | Nothing of the project's own loads, `CLAUDE.md` included |
| 67 | **Part.** `claude auth status` does not check the token. The one-year token was not made: it needs the operator at a browser |
| 68 | What is left can be read: `rate_limit_event` on every turn. The message at a limit was not seen: the limit was not run into, because the building agent runs on the same subscription |
| 69 | Yes, in a new sandbox, on another model, and with a newer CLI; `--session-id` works |
| 70 | The quirks hold; a background subagent gives two results; `DISABLE_AUTOUPDATER` could not be shown either way |
| 71 | Yes, through `--mcp-config` |
| 72 | Five at once, 144 calls, all `ok` |
| 73 | Yes on 2.1.287 |
| 74 | Yes: the harness in the home, a skill in `~/.claude/skills/`, and `--append-system-prompt-file` |
| 75 | The `initialize` answer is the list; a refused name gives `model_not_found` |
| 76 | **Not tested.** `spike/capture-setup-token.py` is ready and tested with a fake token; it needs the operator at a browser |
| 77 | Two `api_retry` with 401, then a result with `authentication_failed` |
| 78 | **Not tested in M0:** how long a sign-in lasts is seen over weeks |
| 79 | About 7.5 s (`claude`) and 8 to 10 s (`agy`), with the sandbox's start |
| 80 | Yes, with no model request |

## What changed in the design (0.33)

Section 26 lists it. In short: dave's real size; the models on the operator's accounts, and parked question 100 on the checking agents; the limits of `claude` read from `rate_limit_event`; the login checks, fresh `agy` logins for each sandbox, and one retry; an `agy`-only Google account, because the `agy` login opens Google Cloud; the Crew MCP checked before a run and a run stopped when it fails; a turn's last result; `agy` with 1 lead slot, the way out of section 24 for items 1 and 35; the rows of 12.2 for `agy` 1.3.4 and `claude` 2.1.287; KSM and Docker's address pool; the hosts for Model only mode; 17.6 after styrman-crew's uninstall; parked question 101 answered. Items 16 and 29 moved to M1.

The ways out of section 24 that are now needed: items 1 and 35 (the number of runs follows what the spike found). Not needed: items 2 and 17 (no keyring; the token file works), 7, 10, 12 and 53, 66, 73, 75 and 80, 79.

## What is still unanswered

- Items 67 and 76: the one-year token of `claude setup-token`, ten sandboxes on it, and the capture of the token. They need the operator at a browser; Setup in M2 is where they are built, so they can be done then.
- Item 68's message at a `claude` limit: recorded the first time a real limit is met.
- Items 2 and 17 over hours, and item 78: seen once Crew runs.
- `DISABLE_AUTOUPDATER` (item 70): no update was seen with or without it.
- Item 13's Kata 4 half, items 16, 29, 34, 59, and 64: M1.

## Tokens and containers

- **Removed:** every `crew-spike-…` container and network (about 85 sandboxes over the day), and every copy of a login under `spike/raw/` (a scan for the refresh token and the access token finds none).
- **Left on dave:** the image `crew-spike-runtime:5c9e6fb` (4.4 GB) with its build cache, kept for M1's comparison of the harness (item 16); `spike/raw/scrub-values.txt`, which the scrub check needs; the raw outputs in `spike/raw/`, which git ignores; and `.env.filled`, mode 600, which git ignores.
- **To revoke:** nothing has to be. No `claude setup-token` was made, and the `claude` access token that sandboxes got expires by itself within hours; no refresh token of `claude` left dave's own home. The `agy` token file was copied into throwaway sandboxes that are all removed; it is the operator's own sign-in. When `agy` moves to an account used only for `agy` (12.6, 0.33), remove Antigravity's access from the personal Google account (its security settings, "third-party access"); that revokes the refresh token the copies held. Whether `agy`'s own sign-out revokes it was not tested.

## Can M1 start

Yes. Before it, or during it:

1. `CREW_APPS_DOMAIN` in `.env.filled` shares a registered domain with `CREW_HOSTNAME`, which 19.4 forbids. Only hosting (M19) needs it; M1 can install without it.
2. Step 2 of 17.6 is moot (the data is gone), and steps 3 and 5 are done. M1 starts with Crew's own host setup.
3. A Google account used only for `agy` is needed at Setup (M2), not in M1.
4. Parked question 100 (a Gemini Flash entry for the checking agents) is open; its default holds until then.
