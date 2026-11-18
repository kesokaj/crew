## 12. The harness: the agent CLIs, content, and the run driver

### 12.1 Three layers, and no model API

Agents do not call a model API. Each agent run is a command-line agent, started headless inside its own sandbox and logged in with the operator's subscription (R125). There is one such CLI, `agy`, and no other is added (R203, R204).

| Layer | What it is | Where it lives |
| --- | --- | --- |
| Agent CLI | The agent loop, the model access, and the tools. Today `agy`, the Antigravity CLI. Others can be added (12.2). | The vendor. The runtime image of a release holds one version of it. |
| Harness | What an agent reads when a session starts: the global context, the skills, the subagents, and the rules. The same content for every CLI. | The folder `harness/` in Crew's source repository. It starts as a copy of styrman-harness and is Crew's own from then on. |
| Run driver | Starts a CLI for one subtask, gives it the prompt, enforces limits, and turns its output into events. | Crew: `crew-run` in the runtime image, and the driver in the server. |

All three are in one runtime image (R136). The image is part of the Crew release (16.1). When a CLI, the harness, or a tool changes, a new image is built and ships as a new release (12.8).

**One CLI.** In 0.26 you decided for `agy` only. In 0.30 you brought `claude` back, and asked for room for other terminal CLIs such as `codex` and `opencode`. In 0.42 you took `claude` out again: Crew is built for `agy` alone, and no other CLI is added. What Crew knows about `agy` is in its adapter (12.2), and nothing outside it names the CLI. Through `agy` Crew runs Gemini, Claude, and GPT models, and when one group of models is at its limit, the other takes the work it can (12.7).

**The harness in the image.**

- **One source.** `harness/` holds the context, the skills, the subagents, and the rules once, in Claude Code's format. That is the format styrman-harness keeps them in.
- **Built for `agy`.** The image build writes the harness out translated for `agy`, with its tool names, model names, and file layout. The translation is the code styrman-harness has today, moved into Crew's build.
- **Already in place.** The result is in the image's home skeleton, under `~/.gemini/config/`, with the skills where `agy` reads them (section 24, item 33). Every sandbox has it from the first second. Nothing is installed when a sandbox starts.
- **Per project.** A project's own skills are written into the run's home by the driver, in the place the run's CLI reads. Its overrides are in the prompt (11.2).

**What replaces `sh-agent`.** styrman-harness reaches a machine through its own program, `sh-agent`. Crew does not use it.

| `sh-agent` did | In Crew |
| --- | --- |
| `install`, `update`: put the harness in place for `agy` | The image build |
| `memory add`, `memory show` | The Crew MCP: `remember`, `search_memory`, and `get_memory`. The memory index is part of every prompt (9.3). |
| `search`, `index` | The Crew MCP: `search_tickets`, `search_docs`, `search_memory`, and `search_summaries` |
| `metrics ingest` | The event sink computes the same numbers from each run's events (10.2) |
| `dream`, `report`, and the retro skills | The improver and improvements (section 10) |
| `status` | The Versions screen (section 16) |

### 12.2 The agent CLIs, and what an adapter is

Everything that differs between CLIs is in one place, the CLI's adapter. The rest of Crew asks an adapter and never names a CLI.

| An adapter says | Used in |
| --- | --- |
| How the CLI is installed in the image, and how its version is fixed | 12.8 |
| How it is signed in, what that leaves behind as a file or a value, and how a login is checked | 12.6 |
| Which models it offers, with their efforts and families, and how that list is read | 12.3 |
| How one headless turn is started: the flags, the prompt, and the settings for full access inside the sandbox | 12.4 |
| How its output becomes run events, and what a result, a refused tool, a limit, and a missing login look like | 12.5 |
| How a conversation is continued, and which files hold it | 12.10 |
| Where it reads the harness, a project's own skills, and Crew's MCP server | 12.1, 12.9 |
| Which files in a repository it loads, and which of them are code | 13.8 |
| Its groups of models with their limits, how what is left is read, and how paid extra use is kept off | 12.7 |
| Which hosts it needs in Model only mode | 13.6 |

- An adapter is Go code in `internal/cli/<name>/`, a part of `crew-run`, a build target for the harness, a step in Setup, and recordings in `testdata/<name>/` that its tests replay.
- Which CLIs an installation uses is the setting `agents.clis`. Setup puts a CLI on it when you sign in to it the first time, and you can take one off. A CLI that is not on the list does not exist for this installation.
- A CLI on the list that has lost its sign-in is signed out for now. No run starts on it, and its runs move or wait (12.7).
- **Adding a CLI,** such as `codex` or `opencode`, is: its adapter, its step in Setup, its build target, its groups of models, an entry in the default list of each agent that should use it, its recordings, and a spike of its own for the questions of section 24. Nothing else in Crew changes. None is built now.
- **Software Factory focus (`agy` active, hardened multi-CLI seam in `internal/cli/adapter.go`, since 1.47 / 2.0):** Active execution remains locked strictly to `agy` (`cli.Names() == ["agy"]`, `agy acp --stdio --process-per-session`), and enabling any secondary CLI later requires explicit operator approval and `AGENTS.md` revision. Behind `internal/cli/adapter.go` (`ACPAdapter` / `RuntimeDescriptor`), Crew maintains a hardened adapter seam defining `IsolationDedicatedKataVM` (mandatory separate Kata microVM and credential home if `claude` or `pi` are ever added, preventing credential co-mingling under `Rule 8`), declarative `Capabilities` (`protocol`, `resume`, `systemPrompt`, `streamableHTTPMCP`, `usageTokens`), subscription-only `AuthSpec` (`0600` non-clobbered credential helper `WriteOAuthCredentialFile` with symlink refusal), and `MultiProviderForbiddenEnv` (`internal/cli/agy_config.go`, `Rule 11` zero API keys in sandboxes).

**The CLI today.** `agy` has a documented headless mode. The table is what the run driver uses. It is from the vendor's documentation and from what styrman-harness runs today. Where a source is unsure, the table says so, and the spike decides.

| Need | `agy` |
| --- | --- |
| Headless run | `agy acp --stdio --process-per-session` inside a project's warm microVM (12.4, since 1.37), or `agy -p` with `--mode accept-edits` for a single-call judge, summary, prompt rewrite, or login check |
| The prompt | Over JSON-RPC 2.0 `session/prompt` (`{"sessionId":..., "prompt":[{"type":"text","text":...}]}`) in `agy acp` (supporting both new turns and live mid-turn steering), or on standard input as one stream-json message `{"event":"user","message":{"content":...}}` with `-p "" --input-format stream-json` |
| Output | JSON-RPC 2.0 `session/update` notifications (`agent_message_chunk`, `agent_thought_chunk`, `tool_call`, `tool_call_update`, `usage_update`) and `PromptResponse` (`stopReason`, `usage`, `turnMetrics`) in `agy acp`, or `--output-format stream-json`. In `agy acp`, `RUNNING` (`pending`/`in_progress`) updates for the same `toolCallId` within a turn emit one `tool_start` step instead of counting lifecycle transitions multiple times, and `Agy.Usage` reads both snake_case and ACP camelCase token fields (`inputTokens`, `outputTokens`, `cachedReadTokens`, `cacheReadTokens`) across `PromptResponse.usage` and flat or nested `usage_update` notifications (since 1.46). |
| Events | `init`, `step_update`, `result` (and their `agy acp` `session/update` equivalents) |
| The result | `status`, `response`, `error`, `usage`, `num_turns`, `duration_seconds`, `conversation_id`. styrman-harness also reads `denied_actions`, which the vendor does not document. |
| Model and effort | `session/set_model` (`modelId`) in `agy acp`, or `--model <name>`, with a name from `agy models`. Most names end in the effort, as in `gemini-3.8-flash-high`. Crew passes the name as printed and does not use `--effort` (12.3). |
| The models the account can use | `agy models` |
| Full access inside the sandbox | `session/set_mode` (`modeId: "bypassPermissions"`) in `agy acp` plus `~/.gemini/antigravity-cli/settings.json`, written for the run: `permissions.allow` with `command(*)`, `read_file(*)`, `write_file(*)`, and `mcp(*)`, and the workspace in `trustedWorkspaces`, as styrman-harness writes it. A folder inside a trusted workspace is trusted too. With the token file, this file is all a fresh home needs: no first-run question is asked (section 24, items 9 and 61). Before every turn `PrepareRun` resets `.bashrc` and `.profile`, removes `modelProvider` and every provider, API-key, base-URL, or endpoint key (`agyProviderKeys`) from `settings.json` so a setting written during a turn cannot move `agy` off the subscription (since 1.26, CREW-23, register row 74), and sets `userSettings.remoteControlEnabled: false` while dropping `cliRemoteControlHostname` and `remoteControlHostname` in `~/.gemini/config/config.json` (keeping its other keys) so `agy`'s remote control stays off before every turn; `agy` names no default relay host in its binary or guide, and in Internet mode traffic is not proxied, so an agent that runs `agy --remote-control` or `agy remote-control start` during its turn in Internet mode is the risk that remains (since 1.26, CREW-22, register row 73, 13.6). |
| What it loads from the repository without asking | `.agents/hooks.json` and `.agents/mcp_config.json`: the spike saw the hook run and the server contacted (13.8) |
| Paid extra use | Off by default. The CLI's own setting `useG1Credits` is dropped from its settings file when it is false, so writing it as off shows nothing; the account's setting `use_ai_credits` is what the CLI takes at start. Setup and the login check read `agy -p /credits` ("Remaining credits"), and Crew writes `useG1Credits` as off all the same. With credits on and left, a run at its limit spends them and does not stop. |
| Limits | `session/cancel` at the driver's turn timeout in `agy acp`, or `--print-timeout`, set for every run as a duration such as `280s` (a bare number is refused; its default is 0, which waits for the turn without end), and the driver's own timeout |
| Continue the same conversation | `session/load` (`sessionId`, `cwd`, `mcpServers`) in `agy acp`, or `--conversation <id>` |
| Several turns in one process | `agy acp --stdio --process-per-session` multiplexes concurrent project sessions and multi-turn conversations in one warm ACP server per project microVM (since 1.37). `--input-format stream-json` is not used (12.10). |
| The id of the conversation | `sessionId` in `agy acp` (`NewSessionResponse` / `LoadSessionResponse`), or `conversation_id`, in `init`, in every `step_update`, and in `result` |
| Crew's MCP server | Per-session `NewSessionParams.mcpServers` (`type: "http"`, `url`, and `headers` with that session's bearer token) in `agy acp` (since 1.37), and `~/.gemini/config/mcp_config.json`: `serverUrl` and `headers`. The CLI writes each tool as a file under `~/.gemini/antigravity-cli/mcp/<server>/`, and the model calls it through one tool, `call_mcp_tool`. |
| Session files | `~/.gemini/antigravity-cli/conversations/<id>.db`. That one file is all a new sandbox needs to continue (section 24, items 38 and 45). The CLI also writes `conversation_summaries.db` and `brain/<id>/`, which are not needed. |
| What is left of the limits | `agy -p "/usage"` prints what is left and when it resets; with `--output-format json` the result holds `command.data.groups[].buckets[]` with `window`, `remaining_fraction`, and `reset_time` (12.7) |
| Subagents inside a run | `step_update` carries `subagent_info` with each subagent's own conversation id, so a helper can be told apart |
| Cost | Not reported. Token counts are best effort. |
| A project's own context | The CLI reads `AGENTS.md` from the workspace by itself |
| A call with no tools, for a judge, a summary, or a prompt rewrite | A settings file that allows nothing. To confirm in the spike. |
| No update inside a sandbox | `AGY_CLI_DISABLE_AUTO_UPDATE=true` |

**Quirks the driver must handle.** These are from styrman-harness's bug records and test logs, or from the vendor's documentation. The spike records each one again (section 24, items 52 and 70), because the CLIs change often.

| CLI | Quirk | What the driver does |
| --- | --- | --- |
| `agy` | One refused tool gives exit code 0 and only a line on standard error | The driver reads `status` in the result, `denied_actions` when it is there, and standard error |
| `agy` | The run does not end while standard input is open | The driver closes standard input after the prompt |
| `agy` | At a limit the run ends in the middle of its turn with exit code 3, a line `AGY_ERROR: {"status":"RESOURCE_EXHAUSTED","error_code":429,"retryable":true,...}` on standard error, and a result with `status: ERROR`, "Individual quota reached ... Resets in 2h26m13s.", and the usage of the work done before (section 24, item 50). | The driver records the quota event (12.7), keeps the work in progress, and reads the result. It takes the limit from `AGY_ERROR`'s `status`, the group from the run's model, and the reset from `/usage`, not from the text. |
| `agy` | A missing or refused login makes a run print a sign-in link and wait 60 seconds for a code, then end with exit code 1 and `authentication failed or timed out`. This still holds on 1.3.4, against the vendor's documentation. | The driver checks the login with `agy models` before the run, which fails in under a second (12.6) |
| `agy` | Global rules are cut at 24,000 bytes per file. All rules together have a budget of 20,000 tokens, and a rule file without valid frontmatter is dropped without a message. | The harness keeps its context under 22,000 bytes, and its build refuses a file `agy` would drop |
| `agy` | In print mode the main agent can end its turn while a subagent or a background command still runs | The stage prompt tells it to wait, and the driver does not take a result as final while a subagent is still reported as running |
| `agy` | When an MCP server cannot be reached or refuses the token, the run goes on without its tools and ends with 0. An agent left without the Crew MCP then tries to repair it: in the spike it edited its own MCP configuration, and built a port forwarder to reach the server by hand. | `crew-run` checks that the Crew MCP answers before it starts the CLI, and the driver stops a run whose CLI reports the Crew MCP as failed, with the reason `error` (12.4) |

### 12.3 Models and effort

For each agent, three things are settings: the CLI, the model, and how hard it thinks. A model belongs to a CLI, so choosing the model chooses the CLI. For the agents whose work it can measure, Crew chooses them by itself from its numbers and tells you. Your own choice overrides it (R127, R205).

**Crew reads the list from each CLI (R190).** Each CLI has a list of its own: `agy` prints it with `agy models` and shows it with `/models`. You said in 0.31 that these are the models Crew lists. Crew keeps no list of its own in the code.

| | |
| --- | --- |
| `agy` | `agy models` prints every model the signed-in account can use |
| When that list cannot be read | Crew keeps the last list it read, and tries again at the next check. `agy` has no short list: until its list was read once, no run starts on it. |
| A name you add | Any name the CLI takes, such as a full name to stay on one version (`agents.extra_models`). It is shown as added by you. |
| What the CLI really ran | When the CLI says the full name of the model it ran, Crew records it with the run. Results are kept under the full name, so a change behind a name shows as a new model. |
| Where it runs | In a login-check sandbox, which holds the login of that CLI and nothing else (13.3) |
| When | After every sign-in, once a day, when a release changes the version of a CLI, and when you press Refresh in the Models screen |
| What is stored | One row per CLI and name in the `models` table: the CLI, the name as it is passed, the model, the effort, the family, the limit group (12.7), when it was first and last seen, and whether it is still offered |
| How a name of `agy` is read | Most names end in the effort: `gemini-3.8-flash-high`, `gemini-3.1-pro-low`, `claude-opus-5-5-medium`. A last part of `fast`, `low`, `medium`, `high`, or `max` is the effort, and the rest is the model. So is any last part in which two or more names differ and are otherwise the same. A name that ends in anything else is a model with one fixed effort, shown without a level. |
| The family | From the name: `gemini-` is Gemini. `claude-`, `opus`, `sonnet`, `haiku`, and `fable` are Claude. `gpt-` is GPT. Anything else is its own family, which Crew does not know (`cli.Known` is true only for `gemini`, `claude`, and `gpt`, and a check on a model, or on a builder's model, of a family Crew does not know waits and shows in the inbox; since 1.26, CREW-28, register row 17). |
| What Crew passes | The name exactly as the CLI takes it. Crew never builds a name that is not in the list. |
| A new model | It appears in the Models screen, and you get news. Nothing starts using it by itself. |
| A model that is gone | It stays in the list, marked as no longer offered, so old runs still show what they used. A name a CLI refuses in a run is passed over for 15 minutes and then tried again; Refresh offers it again at once. A run's report is its own account (12.4), so it never takes a model away for every project. |

**What `agy` offers today.** This is from the vendor's documentation and from lists that users have published. It depends on the plan, and it changes. The spike reads the real list on your account (section 24, item 47).

| CLI | Family | Models | Effort |
| --- | --- | --- | --- |
| `agy` | Gemini | `gemini-4-argon` | low, medium, high |
| `agy` | Gemini | `gemini-3.8-flash`, `gemini-3.7-flash`, `gemini-3.6-flash` | low, medium, high |
| `agy` | Gemini | `gemini-3.1-pro` | low, high |
| `agy` | Claude | `claude-opus-5-5`, `claude-sonnet-5-5` | low, medium, high |
| `agy` | Other | `gpt-oss-120b` | medium only: the one name is `gpt-oss-120b-medium` |

- In `agy`, the Claude models need a paid Google AI Pro plan or higher, and are not offered during a trial.
- The vendor's page for the Enterprise plan of `agy` lists a fourth effort, `max`, for its Claude models. The lists users published for the consumer plans do not have it.
- The vendor of `agy` has announced that the older Claude 4.6 models and GPT-OSS are removed on November 2, 2026.
- **What the spike read on your accounts (0.33).** `agy models` lists ten names: `gemini-3.8-flash` at low, medium, and high, `claude-opus-5-5` and `claude-sonnet-5-5` at low, medium, and high, and `gpt-oss-120b-medium`. It lists no `gemini-3.1-pro`, and no Gemini 3.7 or 3.6 Flash. On 2026-10-16 it also lists `gemini-4-argon` at low, medium, and high (`testdata/agy/models-gemini-4.txt`).
- **What that means for the defaults below, on these accounts.** An entry whose model is not listed cannot run, and every entry with `gemini-3.1-pro` would be skipped. Since 0.42 the default lists name only models that `agy` lists. Since 0.41 the reviewer and the verifier have `agy` with `gemini-3.8-flash` as their last entry, as the judge had (parked question 100).

**Brackets and autonomous classification (since 1.22, R130; autonomous since 1.44, R190).** Every model a CLI lists may be used, and each belongs to a bracket by its kind (`pro`, `flash`, or `oss`). Install seeds `agents.brackets` from the baseline (`crew.yaml`), and on every `models.Record` and startup `FillRoles`, `syncModels` (`internal/models/brackets.go` and `internal/models/brackets_sync.go`) deterministically classifies offered CLI models into `agents.brackets` from their tokenized names and offered efforts while preserving any explicit operator bracket overrides or custom brackets in stored machine `settings`:

- `gpt-oss` or `gpt-oss-*` -> `oss`;
- whole tokens `flash`, `lite`, `mini`, `nano`, `haiku`, or `sonnet` -> `flash` (evaluated before `pro` so `-lite`/`-mini` variants stay `flash`, and token-matched so codenames such as `proton` do not false-match `pro`);
- whole tokens `opus`, `pro`, or `ultra`, or multi-effort (`>= 2` distinct non-empty efforts) `gemini-<major>-*` codenames -> `pro`;
- any offered model that matches none of these returns `""`, stays out of `agents.brackets` as a bracket of its own, and surfaces `nobracket/<cli>/<model>` in the inbox (`internal/inbox/inbox.go`, with `Since = min(first_seen)`) so you can place it.

| Bracket | Baseline models, with their group (12.7) | Roles whose primary slot uses it |
| --- | --- | --- |
| `pro` | `agy/gemini-4-carbon`, `agy/gemini-4-argon` (Gemini), `agy/claude-opus-5-5` (Claude and GPT) | `curator`, `planner`, `assistant`, `builder`, code-ticket `reviewer` and stage checks, `verifier`, `improver` |
| `flash` | `agy/gemini-3.8-flash` (Gemini), `agy/claude-sonnet-5-5` (Claude and GPT) | `tester`, `e2e`, `summary`, `prompt`, `judge:` checks, `docs`/`chore` ticket reviews (`Ask.Bracket = "flash"`), and smaller `builder` tasks (`part` subtasks and `docs`/`chore` tickets) |
| `oss` | `agy/gpt-oss-120b` (Claude and GPT) | operator-chosen pins, project settings, or `Set by you` |

- Within each `(family, bracket)`, Crew orders models deterministically (`CompareModels`) by `(major.minor version DESC, min(first_seen) DESC)`, breaking same-transaction fresh-install ties by baseline order in `crew.yaml` and then `entry DESC`, so higher versions (`3.8 > 3.7`, `4.x > 3.x`) and newer same-major codenames (`agy/gemini-4-carbon` ahead of `agy/gemini-4-argon`) promote automatically without downgrading if an older version is re-listed later.
- A bracket belongs to the model, not to the role: a run that cannot go on its model moves within that model's bracket when allowed by the `(family, bracket)` model rule (`allowedForAsk`, 12.7).
- The setting `agents.brackets`, the `(family, bracket)` role recipes below, and the comments of `agents.at_limit` and `quota.slow_down_at` are in `crew.yaml` and its listing in 14.1 (since 1.26, register row 85, CREW-32; tightened in 1.40 and 1.44).

**Every model can be browsed, and given to an agent (R190).** You said that you want all models browsable in Crew, and that different agents use different models. The Models screen is that place.

| | |
| --- | --- |
| The models | Every model the CLIs offer, grouped by CLI and then by family. For each: its efforts, its limit group, when it was first seen, and whether it is still offered. |
| One model | Opening a model shows which agents use it, by default and in which projects, the results measured with it for each agent and effort, and its latest runs. |
| The agents | Each role is an agent of its own, with a key of its own under `agents.roles`: the lead, the planning agent, the builder, the reviewer, the tester, the E2E tester, the verifier, the summary writer, the prompt writer, the judge, the improver, and the assistant, which answers in a conversation and in the chat of a ticket. The screen lists them with what each does, the CLI, the model, and the effort it runs on, and its next choices. |
| What Crew chose | Every agent with its state, Automatic or Set by you. What Crew changed, with the numbers, Go back, and Set it myself. For an agent you set yourself: what Crew would choose, with Use it. The comparisons that run (below). |
| Giving a model to an agent | In the same screen: choose the model, on the CLI you want, and the effort for an agent. That sets the machine's default for it (`agents.roles`). A project's own setting and a pin on a ticket still come first. |
| What cannot be chosen | A model no CLI lists, and a model of a CLI that is not signed in. A reviewer, a verifier, or a judge on the builder's family is shown as what the rule for checks will change. |

**Where a run's CLI, model, and effort come from.**

| Where | |
| --- | --- |
| 1. A pin on the ticket | Set in the task view: the model with its CLI, the effort, or both. It applies to the next subtask of that ticket. |
| 2. The project's setting for the role | Set in the project's Settings |
| 3. The machine's entry for the role, `agents.roles` | Install fills it from the baseline, with the table below. After that it is Crew's automatic choice, or yours when you set the agent yourself (below). |

- The model and the effort are resolved one by one through the same three levels. A ticket can pin only the effort and keep the model. The CLI comes with the model.
- If the model does not offer the effort that was asked for, Crew takes the nearest one it offers and records that.
- Every list in the app shows only what the signed-in CLIs offer now.

**The defaults and live `(family, bracket)` sync.** Each role has an ordered `(family, bracket)` recipe, seeded at install from the baseline (`crew.yaml` and `baseline/roles.yaml`) and kept ranked on `models.Record` and startup by `syncModels` for every role not set by you (`agents.auto.set_by_you` or an operator edit in `changes`) while `agents.auto.on` is `true`. Crew takes the first entry that can run: its CLI is signed in, and the model is offered. The rest of the list is the agent's next choices when the first cannot run (12.7).

| Agent | Ordered `(family, bracket)` recipe | Baseline CLI and model list | Effort |
| --- | --- | --- | --- |
| Curator (`curator`, replacing `lead` since 1.42) | `(gemini, pro)`, `(gemini, flash)` | `agy/gemini-4-carbon`, `agy/gemini-4-argon`, `agy/gemini-3.8-flash` | high |
| Planning agent (`planner`) | `(gemini, pro)`, `(gemini, flash)` | `agy/gemini-4-carbon`, `agy/gemini-4-argon`, `agy/gemini-3.8-flash` (interactive plan drafts and `breakdown` jobs alike; since 1.44) | high |
| Assistant, in a conversation and in the chat of a ticket | `(gemini, pro)`, `(gemini, flash)` | `agy/gemini-4-carbon`, `agy/gemini-4-argon`, `agy/gemini-3.8-flash` | high |
| Builder | `(gemini, pro)`, `(gemini, flash)` | `agy/gemini-4-carbon`, `agy/gemini-4-argon`, `agy/gemini-3.8-flash` (`part` subtasks and `docs`/`chore` tickets pass `Ask.Bracket = "flash"`, preferring Gemini `flash`; since 1.26) | high |
| Reviewer | `(claude, pro)`, `(gemini, pro)`, `(claude, flash)`, `(gemini, flash)` | `agy/claude-opus-5-5`, `agy/gemini-4-carbon`, `agy/gemini-4-argon`, `agy/claude-sonnet-5-5`, `agy/gemini-3.8-flash` (`docs`/`chore` ticket reviews pass `Ask.Bracket = "flash"`, selecting Anthropic `flash` with Gemini `flash` fallback, while code reviews and custom `stage` checks stay `pro`; since 1.44) | high |
| Verifier | `(claude, pro)`, `(gemini, pro)`, `(gemini, flash)` | `agy/claude-opus-5-5`, `agy/gemini-4-carbon`, `agy/gemini-4-argon`, `agy/gemini-3.8-flash` | high |
| Tester and E2E tester | `(gemini, flash)`, `(gemini, pro)` | `agy/gemini-3.8-flash`, `agy/gemini-4-carbon`, `agy/gemini-4-argon` | medium |
| Summary writer | `(gemini, flash)`, `(gemini, pro)` | `agy/gemini-3.8-flash`, `agy/gemini-4-carbon`, `agy/gemini-4-argon` | low |
| `judge:` checks | `(claude, flash)`, `(gemini, flash)`, `(gemini, pro)` | `agy/claude-sonnet-5-5`, `agy/gemini-3.8-flash`, `agy/gemini-4-carbon`, `agy/gemini-4-argon` | low |
| Prompt writer | `(gemini, flash)`, `(gemini, pro)` | `agy/gemini-3.8-flash`, `agy/gemini-4-carbon`, `agy/gemini-4-argon` | medium |
| Improver | `(gemini, pro)`, `(gemini, flash)` | `agy/gemini-4-carbon`, `agy/gemini-4-argon`, `agy/gemini-3.8-flash` | high |

I chose these. You confirmed in 0.31 that each CLI runs signed in as you, and that this is what you want. Since 0.32 they are where Crew's automatic choice starts (below).

- Every entry runs through `agy`, which offers both Claude and Gemini models (since 0.42).
- Automatic model eligibility is defined by `(family, bracket)` (`allowedForAsk` in `internal/models/resolve_checks.go`, wired across `Choose`, `RuleCannotBeMet`, and `Chooser`, since 1.44): Gemini models (`pro` and `flash`) are used for all non-check roles (`curator`, `planner` for both interactive plan drafts and breakdowns, `assistant`, `builder`, `tester`, `e2e`, `summary`, `prompt`, `improver`); Anthropic (`claude`) `pro` models are allowed on code-ticket `reviewer`, `verifier`, and custom `stage` checks; Anthropic (`claude`) `flash` models are allowed when a check runs in the `flash` bracket (`docs`/`chore` ticket reviews via `models.TicketBracket` and `judge:` checks); and any offered model may run when explicitly chosen by the operator (`FromTicket`, `FromProject`, `FromOperator`, `FromComparison`). Non-check roles (including `planner`) stay on Gemini and wait for the Gemini group at its limit rather than moving onto Claude.
- The prompt writer is one short call that you wait for, so it gets a fast model. You can give it Opus in the Models screen.
- Every classified model sits in `agents.brackets` (`pro`, `flash`, or `oss`). When the `3p` group is at its limit or close to it, a `pro` or `flash` check falls back within the same bracket to Gemini (12.7). When `agy` is signed out, no agent runs.
- You can change each role in the Models screen, which sets that agent to Set by you (or records an operator edit in `changes`, resolved as `FromOperator`; resetting a role or clearing `agents.roles` in `changes` clears that role's operator-edited and removed-entry state). `syncModels` respects `agents.auto.on: false`, `agents.auto.set_by_you`, operator role edits and operator entry removals in `changes`, custom operator entries, `Chooser`'s `retry_days` rollback blocklist (`model_choices` `kind = 'back'`), and active `watching`, `kept`, and `back` decisions until a strictly newer unblocked model (`min(first_seen) > decision.At`) appears.
- When `syncModels` promotes a newly offered model ahead of the active offered model on one of the five `ChosenRoles` (`builder`, `reviewer`, `tester`, `e2e`, `verifier`), it records a `model_choices` row (`kind = 'change'`, `reason = 'newer'`, `state = 'watching'`) with the replaced model's measured `FromStats` so `Chooser.watch` (`watch_runs: 20`, `retry_days: 30`) rolls back automatically if pass or escape metrics regress (with `worseFor` returning `false` when the replaced model had `0` measured units).

**A check uses another model than the build (R191).** You decided this in 0.27. A reviewer, a verifier, and a `judge:` check run on a model of another family than the builder whose work they check. The family counts, not the CLI or the group of models: Claude Opus and Claude Sonnet are one family, and GPT is another in the same group.

- When a check starts, Crew resolves its model as above. Then it compares the family with the family of the builder's last run on the ticket: the newest run of a build, a fix, or a part or a join by the builder. When that run has no model recorded, or the ticket has none, a review, a verification, a project's own stage's look, or a judge waits, as at a limit, with the reason on its card, since with no family to avoid it could run on the builder's; a read of it that fails starts nothing on that pass. A reviewer's analysis of a ticket a lead created checks no build, and starts as before (7.6) (since 1.14: the security review of 1.13's change, second round, finding 3, and the operator's popup answer of session 81, "Wait as atLimit").
- The rule is kept a second time where every run passes, at its start and at Crew's move of it at a limit (12.7), whatever the pipeline asked for: a run of a reviewer, a verifier, a judge, or a project's own stage's agent does not start on the family of the builder whose work it checks while the rule holds, unless no entry of the role's list of another family is listed or every listed entry of another family is at its group's limit (12.7, since 1.26), which is when Crew's own choice runs it there. When the check's own model, or its builder's model, is of a family Crew does not know (`!cli.Known`, any family other than `gemini`, `claude`, and `gpt`), the check refuses to start with `ErrCheckFamily` (`"of a family Crew does not know"`), naming the model, before comparing families and before `RuleCannotBeMet` can let it run; neither `Choose` nor `RuleCannotBeMet` treats an unknown-family entry as another family, and the inbox shows an item of kind `crew_cannot` naming the model while the stuck line holds (since 1.26, CREW-28, register row 17). Builders and roles that are not checks still run on a model of an unknown family. The builder is the ticket's builder's last run, as above; for an eval's reviewer, its eval run's builder's run; for an eval case's fit check, the project's builder as Crew chooses it now. A run is a check by its role or by its kind (a review, a verification, a project's own stage's look, a judge, an eval's review, a fit check), whatever role its caller names, and it is judged by what its CLI runs: the name it is given, or its model, and a check that names no model, or a name of another family than its model's, does not start. A run of a check's kind names what its kind does (a fit check no ticket, an eval's review its branch and no ticket, a ticket's check its ticket and no eval), and its ticket is its project's, or it does not start. A builder's run with no model recorded, or an eval run with no builder's run, starts no check, nor does a check that names no ticket and no eval; a ticket with no builder's run has nothing built to check only before anything of the builder's is on it, in Planning, Backlog, or Analysis with no subtask of the builder's, as for a reviewer's analysis, and otherwise its check waits. What it refuses waits as at a limit: a ticket's check with its reason on its card, an eval's suite for a model, and a fit check's case to be checked; a read that fails starts nothing. The news (`check_rule_not_met`) and the inbox notice (`crew_cannot`, 7.8) that the rule could not be met are recorded with the run by this guard, for a ticket's check at its start and at a move, where until 1.15 a move lost it; an eval's run sends none, as before. The operator's own continue of a check onto another model passes it too, as the rule moves the operator's pin, and is refused with the reason; a project that switches the rule off lets it run there. So does every continue of a check on its own model, in a new microVM or in its own, since the builder may have run again since; its news is sent only for a continue onto another model. The guard is `internal/runs/checkguard.go`, protected (16.8), and a read of the signed-in CLIs that fails is an error in every choice of a model, never no CLI signed in; the driver's own test of a CLI's sign-in before a run still reads it as signed out, and the run waits (since 1.15: the security review of 1.14's change, finding 1, and the operator's popup answers of session 82, "runs/checkguard.go", "Wait as at a limit", "Yes, sent by the guard" and "Fix here", and after the change's review and security review "Fix: check what runs", "Fix: role or kind, fail closed", "Guard every model change", "Fix: allowed only before a build" and "Fix the design text", and after their second round "Fix: shape by kind + ticket's project", "Fix: every continue of a check", "Fix: Name of the Model's family" and "Fix: any builder's subtask waits").
- If they are the same, Crew takes the first entry of another family from the role's default list when that family has room, and the run records that the rule changed it.
- If no CLI in `agents.clis` offers a model of another family, or if every listed model of another family is unavailable because its group is at its limit or out of quota (`RuleCannotBeMet` with `atLimit`), the check falls back to a model of the same bracket in the builder's family that has room (`Off: true`, `ModelFrom: FromLimit`, since 1.26, the operator's rule of session 103) instead of waiting, unless the check's model was set by hand (`FromTicket`, `FromProject`, `FromOperator`). The run record, the `check_rule_not_met` signal, and an inbox item (`crew_cannot`, 7.8) say that the rule could not be met and why. Crew does not fall back to a weaker model of the same family.
- If no model of the bracket in either family can run now, because its CLI is signed out or every group is at its limit, or if the check's model was set by hand on a group at its limit, the check waits (12.7).
- A fresh sandbox, a new conversation, its own prompt, and no transcript of the builder still apply (12.10). The checks, which are commands, stay the stronger evidence.
- A tester and an E2E tester run commands. The rule does not apply to them.
- The rule is the setting `agents.checks_use_another_family`. It is on, and a project can switch it off.

**What else holds.**

- **The choice is recorded.** Every subtask and every run records the CLI and its version, the model, the effort, the name that was passed, the full name the CLI said it ran, and where they came from: the ticket, the project, your setting for the machine, Crew's automatic choice, a try, a comparison, the check rule, or a move at a limit. The task view shows it (20.2).
- **Results are measured per CLI, model, and effort.** For each role and kind of work, Crew keeps the share that passed the first time, the fix rounds, and the time; since 0.53, no cost. The kind of work is the ticket's kind and the main language of the files it touches. The numbers hold no project content (10.6).
- **A model changes by Crew's choice, or by yours.** When the numbers show that another entry or another effort does a role better, Crew changes to it and tells you (below).
- **A pin** applies to the next subtask that starts. A running subtask keeps what it started with.
- **When a model is gone.** A ticket's pin or a project's setting that names it is ignored, the machine's entry for that agent applies, which is Crew's choice or yours, and you get news. When every entry on a role's default list is gone, runs of that role wait and the inbox gets an item of the kind "what Crew could not do by itself" (7.8). Crew never passes a name that is not in the list.
- **When the list cannot be read.** Crew keeps the last list and tries again at the next check. After two failures you get news. A stored list is never replaced by the short one.
- **Subagents inside a session** take their model from the harness's agent files. Those name a tier, not a model: `opus`, `sonnet`, or `haiku`, as in Claude Code's format, and the translation for `agy` maps them to `pro` and `flash`, as styrman-harness does. That line is a model setting too: no improvement changes it, and a release that changes one waits for Install (16.3).
- **Crew chooses again.** In 0.25 Crew picked the CLI for each run from the results it had measured. 0.26 made it a setting, and 0.31 a recommendation. In 0.32 you decided for the automatic choice, with your own choice above it (below).

**Crew chooses the CLI and the model, and you can override (R205).** You decided this in 0.32: the choice is automatic, Crew tells you what it chose and why, and your own choice wins. In 0.31 Crew only recommended.

| | |
| --- | --- |
| Two states for each agent | **Automatic** is the default: Crew chooses the entry and the effort. **Set by you**: you chose one in the Models screen. Crew then leaves that agent alone, keeps measuring, and shows what it would choose as a recommendation. One button puts the agent back to Automatic. |
| Which agents | The ones whose work Crew can measure: the builder, the reviewer, the tester, the E2E tester, and the verifier. The lead agents, the assistant, the summary writer, the judge, and the prompt writer follow their list: the first entry that can run. Crew has no measure yet that says one model plans or judges better than another (section 28, row J9). |
| Where it starts | With the agent's list from the baseline, in the table above. Its first entry runs. |
| What is measured | For each agent on each entry it ran on, and each effort: what counts as passed for that agent, the defects that escaped it, the share of its runs that asked for a fix, the fix rounds, and the time. An entry is a CLI with a model. What decides a change is the results alone: the passed share, the escaped defects, and the share of fixes asked for (since 0.53, no cost). The fix rounds and the time are shown. The table further down says what the numbers are for each agent. |
| Trying the others | In Automatic, one run in ten of an agent is a try: it goes to another entry of its list, or to a lower effort (`agents.auto.explore_share`). That is how Crew gets numbers for the others. The run records that it was a try. The details are in a table further down. |
| When Crew changes | When another entry or another effort has at least 20 finished runs for the agent on this machine (`agents.auto.min_runs`), and so has the one it runs on, and the other did the work better. Crew then puts it first in the agent's list by itself. |
| Better | For the builder: its share that passed the first time is at least 8 points higher (`better_points`), and no more defects escaped it. For an agent that checks it is defined below. Since 0.53 "as well, for less" is gone, and with it `cheaper_share`: results alone decide. |
| What these rules are | Plain rules, built in M8, and not the eval's "not worse" (10.5). With 20 runs a difference of 8 points can still be chance. So Crew watches what it changed, and goes back when it was wrong (below). |
| What you see | One line in the news, and a card in the Models screen: the agent, what it ran on, what it runs on now, the numbers with how many runs they rest on, and when. Each card has **Go back** and **Set it myself**. For the builder the card says when the new entry is of the family the checks run on, and which entries the reviewer and the verifier then use by the rule for checks. |
| Your own choice | Choosing a model or an effort for an agent in the Models screen sets that agent to Set by you. A project's setting and a pin on a ticket come first, as before, and Crew never changes them. |
| A recommendation | For an agent that is Set by you, a card shows what Crew would choose, with **Use it**, which takes that entry and leaves the agent Set by you, **Back to Automatic**, and **Not now**, which hides the card for 30 days (`hide_days`). |
| A comparison | You can get numbers faster. You start it in the Models screen: one agent and two entries. Every second run of that agent then goes to the other entry, until each has 20 runs (`agents.auto.compare_runs`) or you stop it. The rule for checks still holds, and so do pins. It is for the five agents that are chosen automatically. |
| A comparison or a try at a limit | While the other entry cannot run, because its group is at a limit or its CLI is signed out, runs go to the agent's own entry and do not count as runs of the comparison. A run that moved at a limit counts in the numbers of the entry it really ran on (12.7), and not as a run of a comparison. |
| When a comparison ends | Its numbers are judged by the same rules. For an Automatic agent Crew changes if they say so and the 7 days allow it. For an agent that is Set by you there is a recommendation. |
| When Crew looks | When a run of one of the five agents ends, the backend counts it and applies these rules (15.2). |

**What the numbers are, for each agent.**

| Agent | What counts as passed | What can change it |
| --- | --- | --- |
| Builder | The command checks of its ticket passed at the first request for a move. Crew's own check sandbox says so, not a model (13.9). | Better |
| Reviewer and verifier | Nothing counts as passed. A checker that passes everything would look best. Crew counts the defects that escaped it, and the share of its runs that asked for a fix. | Better: fewer defects escaped, about as many fixes asked |
| Tester and E2E tester | The same as for the reviewer: the defects that escaped it, and the share of its runs that asked for a fix. | Better: fewer defects escaped, about as many fixes asked |

- **Better, for an agent that checks,** means (since 0.53): fewer defects escaped it, at least 8 points (`better_points`) fewer of its runs, so one bug in twenty runs, which an agent's report can make, is noise (from the security review of 0.53), and its share of runs that asked for a fix is within 5 points (`same_points`) of the entry it runs on, above or below. So a model that passes everything, or fails everything, is never chosen, and neither a lenient model nor a strict one is taken for a better one.
- **A defect that escaped** is a bug that Analysis approved, in a ticket the agent had passed, where that ticket is confirmed: the lead's analysis of the bug's fix ticket, which another agent than the reporter makes, found the defect in that ticket's change, Done before the report, or the operator named it (since 0.54, the operator's choice of report 9). No agent that checks confirms one. A report alone does not count, nor the ticket a report names, so an agent cannot lower another model's numbers by reporting bugs. A run counts one escape at most, however many bugs name the ticket it passed (since 0.53, from round 2 of the security review of 0.53).
- **The window.** The numbers are taken over the last 90 days (`agents.auto.window_days`), for each agent and entry, over all kinds of work. Crew keeps and shows them for each kind of work too, and does not choose per kind in the first version.
- **An alias that moves.** An entry's numbers belong to the full name the CLI said it ran (above). When a name the CLI takes points at a newer model, its numbers start again.
- **A run that moved** at a limit counts for the entry it really ran on (12.7).
- **What the numbers rest on.** A builder's passed share comes from Crew's own checks. The fix rounds come from other models, and the rule for checks gives each builder entry another family as its reviewer. So every card says which family did the checks for each of its two entries. This is one reason for the brakes below.
- **Until M12 there were no bug records,** so "no more defects escaped" had nothing to count, and Crew changed only the builder, by its passed share and its cost. The agents that check joined in M12 (section 23): a defect that escaped is a bug whose `in_ticket` the agent's run passed, once Analysis approved its fix (since 0.46) and confirmed that ticket (since 0.54).

**A try, in detail.**

| | |
| --- | --- |
| The unit | For the builder it is a ticket: every build of one ticket uses the same entry, so a fix round continues its conversation (12.10). For an agent that checks it is one run. So for the builder every count in these rules is a count of tickets: the 20 of `min_runs`, `watch_runs`, and `compare_runs`, the one in ten, and "every second" in a comparison. |
| What is tried | In turn: another entry of the agent's list at the agent's effort, and the agent's own entry at the next lower effort. That is how efforts get runs. |
| When there is no try | On a ticket with a pin. In a project that set the agent itself. While a comparison for that agent runs. During the watched runs after a change. For an entry that Crew or you went back from in the last 30 days. While the other entry cannot run, or its group is above `quota.slow_down_at` (12.7). |
| The builder and the checks | A builder try on the family the checks run on happens only when another family can run the checks now. The rule for checks then moves the reviewer for that ticket, and the run records it. |
| An agent that is Set by you | Has no tries. What Crew would choose for it comes from runs that happened anyway: at a limit, under a pin, and in a comparison. With `agents.auto.on: false` there are no tries at all. |

**How fast this is.** Slow, on purpose. With one in ten, and two things tried in turn, another entry has its 20 only after about 400 tickets of the builder, or 400 runs of an agent that checks. On one machine that can take longer than the 90 days of the window, and then nothing changes. Most changes will come from the runs that had to move at a limit, and from a comparison, which needs 40. If this proves too slow, `explore_share` and `window_days` are the two settings to raise.

**The brakes on an automatic change.**

- **Not too often.** At most one change for an agent in 7 days (`agents.auto.min_days_between`).
- **Crew watches what it changed.** After a change it looks at the next 20 runs on the new entry (`watch_runs`). Worse is the mirror of the rule that made the change. After "better" for the builder: the watched runs are at least 8 points below what the replaced entry had. For an agent that checks: more defects escaped, or its share of runs that asked for a fix is more than 5 points away from what the replaced entry had, above or below. A watch of a change made before 0.53 for "as well, for less" keeps its own mirror: more than 5 points below, or more defects escaped. Then Crew goes back by itself, says so in the news, and neither chooses nor tries that entry again for 30 days (`retry_days`). Going back is not held by the 7 days, and the 7 days start again.
- **Go back, pressed by you,** does the same: the earlier entry runs again, the agent stays Automatic, and that entry is neither chosen nor tried for 30 days.
- **The rule for checks comes first.** Crew chooses no entry for a reviewer or a verifier that is on the builder's family. For the builder it chooses an entry of the family the checks run on only when another family can run the checks.
- **Only what can run.** Crew chooses among the models that a signed-in CLI lists. It never passes a name that is not in the list.
- **The next run.** A change applies to the next run that starts. A running run keeps what it started with.
- **Your choice always wins.** A pin on a ticket, a project's setting, and an agent that is Set by you are never changed by Crew. Two rules that are also yours can make one run use another entry, and the run records it: the rule for checks, when your choice for a reviewer is on the builder's family, and a limit or a lost sign-in, when your entry cannot run (12.7).
- **A switch.** `agents.auto.on: false` sets every agent to Set by you. Crew then only recommends, as in 0.31. Pause all holds automatic changes too.
- **The numbers of the rules are protected.** Everything under `agents.auto` is a protected setting, like the models themselves: no improvement and no release changes it (10.4, 16.8). Only you do.
- **No improvement and no release changes which entry or effort an agent runs on.** The improver's patches may not touch a model or an effort (10.4). A release whose baseline has new entries for an agent adds them after the entries that are there, where Crew can try them. The tier line of a harness agent file is code: a release that changes one waits for your Install (16.3).
- **Few runs, plain words.** With one developer the numbers are few, and a model does not behave the same way twice (10.6). Every card shows its counts.
- **Only this machine's numbers.** An installation chooses from its own runs. Nothing is taken from another installation (16.12).
- **No agent is involved.** The backend computes the choice from the numbers.

### 12.4 The run driver

One subtask is one run. The driver has a part in the backend and a small program, `crew-run`, inside the sandbox.

1. **Prepare.** The backend makes the run's home as an empty folder on the machine and writes one thing into it: the control folder `.crew/`, with the files in the table below.
2. **Boot.** The backend starts the run's microVM (section 13) with that folder as the home. The image's start script fills the home from the home skeleton, which already holds the harness, as styrman-crew's does. Then the microVM reports ready.
3. **Start.** The backend starts `crew-run turn` in the microVM as the user `agent`. It is started detached, so it does not depend on a connection that stays open. `crew-run` first pulls the run's credentials from the credential broker and checks that the CLI is signed in (12.6). If it is not, the run ends at once with the reason `login`, before any work is done. Then it writes the CLI's settings and the MCP configuration with the run's token, checks that the Crew MCP answers with that token (if not, the run ends with the reason `error` before the CLI starts), as the adapter of the run's CLI says, clones the run's branch from the git endpoint into the workspace (11.2), and adds the project's own skills from `main`.
4. **Run.** `crew-run` starts the run's CLI in its own process group with the flags in 12.2. The prompt is a file, because the copied way to run a command in a sandbox cannot close standard input. `crew-run` writes the CLI's stream-json output to the turn's `events.jsonl`, and standard error beside it. That file is a spool, not a store. It touches the heartbeat file every 10 seconds. At the timeout it stops the whole process group.
5. **Follow.** The run's home is a directory on the machine that is mounted into the microVM. The backend reads `events.jsonl` from there as it grows, turns each line into a run event (12.5), and passes it to the event sink, which writes it to the database. No stream between the backend and the sandbox has to stay open. When the backend or the database is away for a moment, the spool keeps what was printed, and the backend reads on from the last event it stored. So a restart or an update loses nothing.
6. **More turns.** A failed check in the goal loop (11.3), or a comment from the operator, is one more turn in the same conversation. In the first version each turn is a new call of `crew-run turn` in the same sandbox, with a folder of its own, and the CLI is told to continue the conversation: `--conversation` for `agy`. How a conversation is stored and continued, in the same sandbox or a later one, is in 12.10. A turn keeps the workspace the turn before left, except in a ticket's chat (11.9): there `crew-run` fetches the ticket's branch, main, and the branches of the ticket's subtasks again before every turn, resets the read-only work tree to the branch, or to the ticket's merge commit once it is Done, and removes what the turn before left; then it keeps out the files the CLI loads by itself and takes main's instructions, as every turn does, so a branch that adds them between two turns is not loaded (13.8, rule 13). A ref that changed between two turns, such as a ticket that got its branch or ended, is a new run with the new ref.
7. **End.** `crew-run` writes the turn's `exit.json`: the exit code and a reason from the closed list below. An end of `crew-run`'s own also names its step, and its error goes apart, for the server's log: the server writes the step's sentence itself (13.9). The turn's result is the last `result` the CLI printed before its process ended. If the CLI reports the Crew MCP as failed during the turn, the driver stops it with the reason `error`, because an agent without those tools works around them (12.2). The backend saves the CLI's own conversation files in the database (12.10) and collects the pushed commits. If the CLI renewed its login during the run, the backend may keep the newer one, after the test in 12.6. It then removes the microVM and the run's home.

**The control folder.** It is `/home/agent/.crew/` in the sandbox. These names are the whole interface between the backend and `crew-run`.

| File | Written by | What it holds |
| --- | --- | --- |
| `run.json` | The backend, before the boot | The ids of the run, its subtask, and its project, the CLI, the model's name, the effort, the id of the conversation at the CLI when one is continued, whether the call has tools, the limits, and the addresses of the broker, the git endpoint, and the Crew MCP |
| `token` | The backend, before the boot | The run's token |
| `turn-0001/prompt.txt` | The backend, before each turn | The prompt of that turn |
| `turn-0001/events.jsonl`, `turn-0001/stderr.log` | `crew-run` | What the CLI printed in that turn |
| `turn-0001/exit.json` | `crew-run`, last | The exit code and the reason; for an end of `crew-run`'s own its step and its error, and a mark when the work in progress was not kept (13.9) |
| `heartbeat` | `crew-run` | Touched every 10 seconds while a turn runs |
| `stop` | The backend | Asks the running turn to stop, and holds the reason. `crew-run` reads it every second, sends the CLI the interrupt, and after 20 seconds stops its process group. |

- `crew-run` has commands: `turn`, since M2 `login-check` and `creds` (which the login check and Setup use without a run, 12.6), and since 1.37 `acp-daemon` (starts `agy acp --stdio --process-per-session` inside a project microVM and listens on `/run/crew/acp.sock` with mode `0600`), `acp-turn` (prepares `/home/agent/worktrees/<run-id>` under a cross-process `syscall.Flock` on `/home/agent/repo.git/crew.lock`, unlinks the per-run `.crew/runs/<run-id>/token` and `ctrl.key` before running `setup()` or `agy`, scopes `creds.env` to `.crew/runs/<run-id>/creds.env`, resets `.bashrc`, `.profile`, `settings.json` with `"enableTelemetry": false` and `"useG1Credits": false` and `agyProviderKeys` including `"gcp"` stripped, and `config.json` via `PrepareRun` both before and after `setup()`, authenticates its session on `/run/crew/acp.sock` via `crew/authSession`, and drives `session/new` or `session/load`, `session/set_model`, `session/set_mode`, and `session/prompt` into the turn's spool), `acp-steer` (delivers a live mid-turn `session/prompt` steer authenticated with `CREW_CTRL_KEY`), and `acp-cancel` (sends `session/cancel` and `session/close` authenticated with `CREW_CTRL_KEY`).
- A turn that is still running after the server restarted is found by its heartbeat and its missing `exit.json`. Whether a detached command in a microVM outlives a restart of the server and of Docker is measured in M1, after styrman-crew is removed (section 24, item 59).

After the run the event sink computes the shape of the session from its events: tools, command verbs, tokens, timings, and errors, with the ticket and the stage result. These feed the signals in 10.2.

**Why a run stops, and what follows.**

| Reason | Set by | What follows | Counts as a stop |
| --- | --- | --- | --- |
| `ok` | `crew-run` | The goal loop (11.3) | No |
| `timeout`, `silent`, `turns`, `rounds`: the time limit, the watchdog, the turn limit, the goal rounds. `budget`, the run's budget, only on a run stopped before 0.53, which the app names "its budget, before 0.53" | `crew-run` or the backend | Retried once in a new sandbox, continuing the conversation | Yes |
| `error`, `refused`: also a run whose events cannot be stored | `crew-run` or the backend | Retried once | Yes |
| `sandbox`: the microVM died or did not start | The backend | Retried once | Yes |
| `setup`: the project's setup command failed, before the CLI started | `crew-run` | Retried once. Two failures in a row in the project, or two check runs in a row of one open ticket (13.5): Crew runs the setup on `main` in a check sandbox. If it fails there too, the fix ticket is created (13.5). If it passes there, the ticket's own branch broke the setup, and the failures count as stops of that ticket (since 0.47). | No, unless the setup passes on `main` |
| `login`, `quota` | `crew-run` | The subtask continues on a model of its bracket in another group that can run, in a new sandbox (12.7). If there is none, or `agents.at_limit` is `wait`, it waits, and continues when you signed in or the limit reset. | No |
| `model`: the CLI refused the name of the model | `crew-run` | The name is passed over for 15 minutes, and the subtask continues on its agent's next choice, as at a limit (12.3) | No |
| `operator`, `restart`: Stop, Stop all, a restart of the server or the machine that a run did not survive | The backend | The subtask waits, and continues afterwards | No |

- "A run stopped twice" (7.7) means the same subtask stopped twice for a reason that counts.
- What follows a stop starts nothing while the subtask's project is paused or its ticket is on hold (15.2): a retry, a continue after a sign-in, a limit's reset, a next choice, or after Stop all's Resume, waits as it is, is not counted as one of Crew's own continues, and goes on at the monitor's next round after Resume, or once the hold is let go (since 0.63).
- The turn limit counts the tool steps of a run, which is what grows when an agent goes in circles.
- The watchdog's "same action" is the same tool with the same arguments. Its "usual time" is the middle value of the last 20 runs of that role and stage in the project, or the baseline's number while there are fewer.

**What Crew takes back from a run's home is not trusted.** The agent can become root in its sandbox, and the home is a directory on the machine. Crew collects the spool, `exit.json`, the CLI's own files, and the login by their fixed names, only as regular files, never through a link, and under a size limit. Before a turn after the first starts, the harness removes from its folder, inside the home, an `exit.json`, a spool and a standard error that an agent wrote there ahead, which would end the turn as soon as the server looks, with its words, while `crew-run` and `agy` ran on with nothing following them; one it cannot remove fails the turn's start (since 1.01; the security review of 1.00, finding 2, and the operator's popup answer of session 68, "Clear it in Continue"). The agent's own commands during the turn, and a process it leaves running, can still write the turn's `exit.json`, which ends the turn so; checks still decide a ticket's move. In the first version only the disk of the sandbox's own Docker has a fixed size, 20 GB by default, as in styrman-crew. The rest of a run's home and its workspace are bounded by the disk guard, which stops new runs before the disk is full (16.10). A fixed size for the whole home is a question for the spike (section 24, item 60).

`crew-run` is a small static program in the runtime image. It holds no secrets and makes no decisions about the board.

### 12.5 The interface to the backend

The backend talks to the harness only through this interface (R5). It is in Go, like the parts copied from styrman-crew (section 17).

```go
type Harness interface {
    StartRun(ctx context.Context, spec RunSpec) (RunHandle, error) // a microVM and the first call of the CLI
    Continue(ctx context.Context, runID, text string) error        // another turn in the same conversation
    Stop(ctx context.Context, runID string) error                  // ends the run and removes its microVM
    Events(runID string) <-chan RunEvent
    Exec(ctx context.Context, runID string, cmd []string) (ExecResult, error) // in that run's microVM
    // Added in M3, for 12.4: a stop that keeps the work in progress, and following a run again after a restart.
    Interrupt(ctx context.Context, runID, reason string) error
    Resume(ctx context.Context, r Resumed) error // from the last stored line of the turn's spool
    Alive(ctx context.Context, runID string) bool
    ExecFresh(ctx context.Context, spec CheckSpec) ([]ExecResult, error)      // in a new microVM with no agent
}
```

- `RunSpec`: project, ticket, subtask, branch, the CLI, the name of the model as that CLI takes it, the effort, the prompt, the limits, the network mode, the size of the microVM, and the conversation to continue, with the way to continue it (12.10).
- `RunEvent`: one normalized event with a type (`message`, `tool_call`, `tool_result`, `status`, `usage`, `quota`, `error`), a time, and a payload. The trajectory holds these events and the CLI's original lines.

| Run event | From `agy` |
| --- | --- |
| `status: started` | `init` |
| `message` | `step_update` with text |
| `tool_call`, `tool_result` | `step_update` with `step_type: tool` |
| `usage` | `result.usage` |
| `quota` | A failure at a limit, such as `RESOURCE_EXHAUSTED` |
| `status: ended` | `result` |

The interface is the same for every CLI. What differs is in the adapter (12.2): how a turn is started, and how the CLI's lines become these events. Two implementations: the adapter of `agy`, and `FakeHarness`, which replays recorded `events.jsonl` files so the orchestrator, the monitor, and the UI are tested without a model or a microVM.

### 12.6 Login and credentials

styrman-crew already solves signing in once and having every sandbox signed in, and it works well for you today. Crew copies that part. Crew adds two things: it checks the login of each CLI, and it lets you sign in again from the app (R134, R135).

**Setup at first start.**

1. The first time Crew is opened, it shows Setup instead of the projects.
2. Crew starts a setup microVM. It is a small sandbox that exists only for signing in. No project and no agent runs in it.
3. The operator signs in there, in a terminal in the app. Each tool prints a sign-in link. The operator approves in their own browser and pastes the code back. The table below lists what can be set up. `agy` is needed. The rest is set up when the projects need it.
4. Crew collects the logins from the setup microVM into the database, encrypted (15.3). It then removes the microVM.
5. A CLI you did not sign in to is not used (`agents.clis`, 12.2). Until one CLI is signed in, no run starts at all.

This is styrman-crew's setup, moved to the first screen.

**Setup is outside the projects (R183).** Every credential is set up once, for the machine, before any project exists. No project has credentials of its own.

| Credential | Set up with | Kept as | Used for |
| --- | --- | --- | --- |
| Git identity | A name and a mail address | Two values | The author of every commit: yours in a terminal, and an agent's in a run |
| Git SSH key | `ssh-keygen` in the setup microVM, or a pasted key. Setup shows the public half, to add to your GitHub account. | A file | All git traffic with GitHub, by Crew. It stays with the server. |
| `gh` | `gh auth login`, with a code in the browser | A file | What a key cannot do: repositories, pull requests, and release pages (6.6). It stays with the server. |
| GitHub App | Created once on GitHub, in your own account, with one permission: reading contents. Setup links to the page and takes the key file it gives you. | A file | Making a temporary token for each sandbox |
| gcloud | `gcloud auth login`, and `gcloud auth application-default login` | Files | The project terminal, which safety rule 8 allows to hold it; as built no sandbox gets it, and since 0.79 no run and no conversation |
| `agy` | Its sign-in, with your personal Google account | A file, `~/.gemini/antigravity-cli/antigravity-oauth-token`, exactly as styrman-crew keeps it. It holds an access token, a refresh token, and an expiry. | The CLI |
| Mail (since 0.71) | A form in Setup: the address of a Google account and an app password of it (18.4), behind a fresh sign-in | A value, `mail` | Sending Crew's mail (18). It stays with the server: no sandbox gets it, the setup microVM not either, and no answer of the API shows the password. |

"Kept as" is the form a credential has when it is used. All of them are stored in the database, encrypted (15.3). A sandbox gets a credential in one of the three ways styrman-crew's broker already has: a value in the environment, a file in the home, and a git credential for HTTPS, which is how the temporary token is used.

**How a sandbox gets its credentials.**

- A credential broker listens only on each sandbox's own network. Who is asking is decided by which network the request comes from.
- `crew-run` asks the broker each time a sandbox starts, before the CLI starts. The credentials arrive as values in the environment and as files in the home. In styrman-crew a login shell does this. A headless run has no login shell, so `crew-run` does it itself.
- The broker answers only a request that comes from the sandbox's own network and carries the run's token. The network alone is not enough.
- **They are copied in, not linked.** The database holds the one current copy. Each sandbox gets its own copy when it starts, so no sandbox shares a file with another, and the copy is removed with the sandbox's home.
- Every run sandbox gets the same set. Nothing has to be chosen per project. No run and no conversation gets gcloud (since 0.79, AGENTS.md safety rule 8; `sandbox.credentials` and `conversations.credentials` are retired settings, which a document saved, imported or read has taken out, and schema step 78 took out of the stored ones).
- A credential that is renewed in Setup reaches every sandbox that starts afterwards.
- The `agy` login a sandbox gets has a fresh access token: the login check renews the stored file, so sandboxes that start together do not all renew one refresh token at the same moment. In the spike one of ten copies that did so failed its first call with "authentication required" (section 24, item 2).

| Sandbox | What the broker gives it |
| --- | --- |
| A run of a project's agent | The login of the one CLI it runs, the git identity, and a temporary GitHub token that can only read. No gcloud: the broker's grant of a run gives none, whatever its sandbox's row says (since 0.79; M20 plan D20, section 26, From 0.78 to 0.79). |
| The project terminal | The login of every CLI that is signed in, the git identity, and the read-only GitHub token (13.11). As built no gcloud, which safety rule 8 would allow it |
| The improver | The login of its CLI only. It sees every project, and it needs nothing else. |
| A `judge:` check, a summary writer, a prompt writer, a login check | The login of its CLI only |
| A conversation with the operator | What a run of its project gets. About no project: the login of its CLI and the git identity, no GitHub token (11.8). No gcloud (since 0.79). |
| The chat of a ticket | The login of its CLI, and the read-only GitHub token for the project's repositories. No gcloud (11.9). |
| A check that runs commands | The read-only GitHub token for the project's repositories, because a setup may fetch private packages. Nothing else. |
| A hosted project | None of them. A hosted project gets its own settings and secrets (19.3). |

**What a sandbox holds, and what that costs.** An agent with internet access can use, or send out, whatever its sandbox holds (13.8). That is the login of one CLI and a GitHub token that can only read and stops working after an hour; since 0.79 no gcloud. It can no longer push to GitHub. One thing limits the rest:

| Limit | What it does |
| --- | --- |
| gcloud for development | Sign gcloud in with an account that is meant for development, not one that can reach production. The login is not bound to one cloud project (see below). Since 0.79 it reaches no run and no conversation. |

**The temporary GitHub token (R189).** You decided this in 0.25, after asking whether a token would be better and whether Crew creates one and removes it.

**What a sandbox needs from GitHub** is little. Its own repository comes from Crew on the machine, and its branch goes back there. Crew pushes to GitHub. What is left is reading your other private repositories, when a project depends on one, and what `gh` can show.

| | The SSH key | The `gh` login | A token you make by hand | A temporary token Crew makes |
| --- | --- | --- | --- | --- |
| What it reaches | Every repository of your account, to read and write | The same, and GitHub's functions | The repositories and the permissions you pick | The repositories and the permissions Crew asks for, each time |
| Can it be made read-only | No | No | Yes | Yes |
| How long it works | Until you remove it | Until you sign out | Until the date you pick | One hour |
| What you do | Add the public half once | Sign in once, with a code | Create it on GitHub, paste it in Setup, and renew it | Create a private GitHub App once, and give Crew its key file |
| GitHub's registry, for release images | No | Expected to work with the packages scope. Checked in the spike (section 24, item 32). | No. GitHub's packages do not accept fine-grained tokens. | Not needed. Sandboxes publish nothing. |

- **Crew itself keeps the key and `gh`.** A token would do the same work with more to look after.
- **Crew cannot make a personal token for you.** GitHub lets only a person create one, on its website. A token made by hand is therefore one long-lived value, the same in every sandbox.
- **A temporary SSH key does not help.** GitHub's deploy keys can be created and removed by Crew, but each opens one repository only. That would be the project's own, which a sandbox already gets from Crew.
- **A temporary token is what GitHub offers for this.** A GitHub App can ask for an installation token at any time. Crew names the repositories and the permission, which is reading contents, and GitHub returns a token that stops working after one hour.

**How it works.**

1. In Setup you create a private GitHub App in your own account, with one permission: reading contents. You install it on your account, for all repositories, and give Crew its key file. This is done once, and nothing is renewed.
2. When a sandbox starts, the credential broker asks GitHub for a token that names the repositories it may read: the project's own, and those listed in the project's setting `sandbox.github_read`. A conversation about no project names no repository and gets no token: GitHub would read an empty list as all of them. The broker copies the token in as `GH_TOKEN`. `git` uses it over HTTPS through its credential helper, as in styrman-crew, and `gh` uses it too.
3. A run lasts at most 60 minutes by default, so one token covers it. A conversation gets a new one with each turn. A sandbox that is still running after 50 minutes, such as a project terminal or a run with a longer limit, gets a new token from the broker. When the sandbox ends, Crew asks GitHub to revoke the token.
4. The SSH key and the `gh` login stay with the server. No sandbox that runs an agent, a check, or a project gets them. The setup microVM is the one exception, for the credentials made there: they are made or pasted there, the server collects them, and the microVM is removed. The mail account (0.71) is entered in a form of Setup and never reaches it. No agent and no project ever runs in it.
5. A token that leaks can read, for at most an hour. It can never push.
6. If GitHub gives no token, the run starts without one. It works on its own repository as before, and reading another private repository fails with a clear error.

**The longest sign-in each CLI has.** You said in 0.31: use the longest-lived sign-in that `agy` has.

| CLI | What Crew uses | How long it lasts | What keeps it working |
| --- | --- | --- | --- |
| `agy` | The token file, with its refresh token | The CLI documents no long-lived token of this kind for a consumer plan. The file renews its own access for as long as Google accepts its refresh token. In styrman-crew you signed in again almost never. | The login check makes one real request a day with the stored login, and a renewed file is stored (below). So the sign-in is used every day, also when no project runs. |

- What ends a sign-in of `agy` early is Google's to decide, such as a changed password or a review of the account. The spike asks what is known (section 24, item 78).
- When the sign-in does end, every project waits until you sign in again (below).

**On another machine.** Each installation has its own Setup, and its sign-ins stay on that machine (16.12). Each operator installs and authenticates with their own `agy` account in Setup; no pre-built runtime images or credentials are shared across independent installations.

- On a machine of your own you sign in with your own dedicated accounts (`section 12.6`, `section 24`). Two machines on the same subscription share its limits.
- On a machine that is not yours, whose subscriptions sign in is for you to decide for that machine. When a client's people use that Crew, the sign-ins should be the client's own. When only you use it, it is your own work on another machine.

**Checking the logins.** In styrman-crew you signed in to `claude` again about once a week, and to `agy` almost never. When a login stops working, you find out because an agent fails. Crew finds out first.

| Check | When | How |
| --- | --- | --- |
| Before a run | Every run, before the CLI starts | `crew-run` asks the adapter of the run's CLI. For `agy`, `agy models` ends with 0: it fails in under a second when the login is refused, and costs no quota (section 24, item 11).. |
| During a run | A CLI stops with a login error | The driver records the reason `login`. The conversation and the pushed work are kept. |
| On a schedule | Every 30 minutes for each CLI, and before the first run after a quiet period | A small check microVM gets the stored login of one CLI and runs the same check. Once a day it also makes one tiny real request, because a login can be present and still refused, and it reads the list of models (12.3). For `agy` that request also keeps the sign-in in use, and a renewed token file is stored after the test below. |

- One failed check is tried again. Two in a row mark that CLI as signed out. The same holds for a run that ends with the reason `login` before any work was done: it is tried once more before the CLI counts as signed out.
- The Agent CLIs screen shows each CLI's state, when it was last checked, and what the last check said.

**When a CLI is signed out.**

- Its runs stop and keep their conversations, the same way as at a subscription limit (12.7). No run starts on it.
- The inbox gets one item: sign in to that CLI again. Its mail is sent at once.
- When no CLI is signed in, every project waits.

**Signing in again.**

1. The operator opens the item. Crew starts the setup microVM with only that CLI's step.
2. The CLI prints its link. The operator approves and pastes the code.
3. Crew collects the new login, runs the check, and removes the microVM.
4. The stopped runs continue in new microVMs with the new login, and waiting runs start.

This is styrman-crew's flow for renewing one credential, started from the inbox instead of from Settings.

**Keeping the login fresh.** When a run on `agy` ends, Crew looks at the login file in the run's home. If `agy` renewed it during the run, Crew may save the newer one. Otherwise the database keeps an older copy, and later sandboxes start from that. With many short runs this could be why a login stops working. This is tried in the spike (section 24, item 2).

A run's home is written by an agent that may have been tricked, so the file is not trusted:

- Crew takes it by its fixed name, only as a regular file, never through a link, and under a size limit.
- Before it replaces the stored login, Crew tests it in a login-check sandbox and compares the Google account with the one you signed in with in Setup. The account is read from Google, with `tokeninfo` and the file's access token, never from the file's own `id_token` without its signature checked: the file may have been edited (section 24, item 56). Another account is refused, and you get an inbox item.
- A renewed file keeps the same refresh token: Google does not rotate it on use, so a copy that renews does not make the stored one stale (section 24, item 2).
- The daily login check renews and stores the file in any case, since 0.31. If the spike shows that this is enough, runs no longer write it back at all.

**What the terms allow, and what Crew depends on.** I am not a lawyer, and you should read the terms of the vendor yourself.

**The sign-in of `agy` is styrman-crew's, copied.** You decided this in 0.28, when I asked whether another way to sign in should be prepared.

- You sign in once in the setup microVM. `agy` writes its token file there, and Crew takes that file, keeps it encrypted, and writes it into the home of each sandbox that may have it.
- The file holds a refresh token, so each sandbox renews its own access by itself. styrman-crew takes a renewed file back, and so does Crew, after the test above.
- No keyring is used. styrman-crew first seeded a keyring, found that this build of `agy` never reads one, and removed that way (its decisions ADR-038 and ADR-040). The vendor's documentation still describes a keyring for a desktop, so the spike confirms the file on the current version (section 24, item 2).
- No API key and no Google Cloud login is prepared. If the account is stopped, no agent runs until you signed in with an account that works.
- `ANTHROPIC_API_KEY` is never set in a sandbox. Crew uses no model API.

**What the vendor says.**


- The vendor's FAQ says headless `agy -p` is intended for scripting and pipelines. It also says that wrapping `agy` in an application is not supported under consumer plans and can lead to suspension.
- The vendor's terms of service list as a breach "using third party software, tools, or services to access the Service". Crew is such software.
- `agy` has two other ways to sign in, which are under other terms: an API key, and a Google Cloud login on the Enterprise plan. You ruled out APIs for the agents, and in 0.28 you decided that Crew signs in the way styrman-crew does and prepares nothing else.
- You have decided to use it: it works well for you today.
- **Crew depends on one account.** If the account of `agy` is suspended or its plan changes, no agent runs until it works again, or until you sign in with another (since 0.42). Everything Crew has saved stays, and work continues where it stopped.

**After a run (R169).** The run's home is removed with the sandbox, after the check for a renewed login above. The CLI's own conversation files are saved in the database first (12.10). No credential is among them.

**What a login can do.** An `agy` login grants more than the CLI: the spike found the scope `https://www.googleapis.com/auth/cloud-platform` on its token, beside `aicode`, `cclog`, `experimentsandconfigs`, and the profile. That token can call any Google Cloud API the account has rights to, in every project of that account. Mail and Drive are not among its scopes (section 24, item 27). In 0.33 the plan called for a dedicated Google account used for `agy` only, whereas the 0.35 verification kept the operator's Google account (also used by gcloud); Crew strongly recommends using a dedicated Google account with no sensitive cloud project access for `agy`, because an `agy` token still carries that scope into sandboxes such as the improver or a judge. Setup says so.

**What gcloud in a sandbox is.** You decided that a sandbox gets the gcloud login and its application default credentials. Said plainly: they are your own Google login, as files. They do not run out, they are not bound to one cloud project, and they are not taken back when the sandbox ends. A tricked agent with internet access can copy them. I asked whether you wanted a smaller way, a one-hour token for one service account. You said no, in 0.28: Crew is an extension of the developer, and working with your own logins is the point. So this stayed, and the cost was accepted. In 0.79 you chose AGENTS.md safety rule 8 over it: a run gets the login of `agy` and nothing more of your logins, so no run and no conversation gets gcloud, and the settings that left it out are retired. gcloud stays in Setup.

### 12.7 Quotas

With subscriptions, cost is not what limits the work. The subscriptions' limits are, and every run of a CLI on the machine shares that CLI's limits (R130).

**What the limits are.** `agy` has one subscription, with limits for each group of models. A limit on one group leaves the other running.

| CLI | Groups of models | Limits |
| --- | --- | --- |
| `agy` | Two: Gemini models (`gemini`), and Claude and GPT models (`3p`) | Each group has a five-hour limit and a weekly limit. Plans below Pro refill once a week only. |

The vendor publishes no numbers for parallel agents.

**How Crew sees them.**

| CLI | Before a limit | At a limit |
| --- | --- | --- |
| `agy` | `agy -p "/usage"` prints what is left of each limit and when it resets, and has a JSON form. Crew reads it in the login-check sandbox every 30 minutes, and again after a run failed at a limit. | A run that fails at its limit marks the group of the model it used |

When neither way gives a reset time, Crew tries the group again every 15 minutes.

**Paid extra use stays off.**

- `agy` can spend paid credits when a limit is reached, and then it does not stop. Crew writes that setting as off for every run (12.2), so a limit stops the work and costs nothing.

**The quota guard.** The orchestrator keeps one state for each group of each CLI: free, close to the limit, or limited until a time. While a group is limited, no new subtask starts on it. Runs on the other groups carry on. Waiting subtasks are shown as waiting for that group's limit, with the reset time.

**When its group cannot run, or is close to its limit, a run moves within its bracket.** You decided this in 0.28 for a limit: move if possible, or wait for the reset. Since 0.30 the same holds for a CLI that is signed out, and with `agy` the only CLI, such a run waits. Since 1.22 it is your rule (session 92, R130): before a run starts, Crew reads the quota of its model's group, and a run never waits while a model of its bracket has room.

- A run whose group is at its limit, or above `quota.slow_down_at` (0.8) of its five-hour or weekly window, takes a model of the same bracket (12.3) in another group that has room and is below that share when allowed by the `(family, bracket)` model rule (`allowedForAsk`, 12.3, since 1.44): the first such entry on the agent's list, else the first allowed model of the bracket that `agy` lists (since 1.26, CREW-32, register row 85; tightened in 1.40 and 1.44 so only check roles move between groups, while all non-check roles including `planner` stay on Gemini). The effort stays, or the nearest one that model offers. When no allowed model in another group has room, the run keeps its own group as before: one run at a time above `slow_down_at` (Pacing, below), and it waits at the limit (the operator's popup answer of session 92, "quota.slow_down_at, 0.8").
- **A check prefers another family, and falls back to the builder's family with a notice when the other family is at its limit or above `quota.slow_down_at`.** When another family has room, a reviewer, a verifier, a project's own stage's agent, or a judge never moves onto the family the builder ran on. When the other family is at its limit, out of quota, or at or above `quota.slow_down_at` while the builder's family has room in the bracket (`RuleCannotBeMet`, 12.3, since 1.26; extended to `slow_down_at` in 1.46), the check falls back to a model of the same bracket in the builder's family that has room (`Off: true`, `ModelFrom: FromLimit`) instead of draining the scarce check family or waiting (keeping the runnable cross-family model with `Off: false` only when the builder's family has no model with room either), unless its model was set by hand (`FromTicket`, `FromProject`, `FromOperator`), and Crew records the `check_rule_not_met` signal and an inbox item (`crew_cannot`, 7.8) explaining why. A project's own stage's agent, which runs on the reviewer's model (7.4), takes the reviewer's next choice with the reviewer's pin, where until 1.14 it looked for a role of its own, found no list, and waited for its own CLI (the operator's popup answer of session 81, "Fix here"). At a limit a ticket's run is held to the builder's family by the kinds its start is: a review, a verification, a project's own stage's look and a judge; a reviewer's analysis of a ticket a lead created moves to its next choice, where until 1.14 it waited for its CLI (the reviews of 1.14's change, "Fix: one predicate + rows"). The move itself passes the guard of 12.3 that every run passes (`models.TicketBracket` keeping `docs`/`chore` reviews in `flash` and `stage` checks in `pro`), by the name the CLI is given as well as the model, which refuses a next choice on the builder's family unless the rule cannot be met, and then records the news and the inbox notice with the new run (since 1.15; the fallback and inbox notice since 1.26).
- **With the defaults, the Claude and GPT (`3p`) group at its limit or close to it (`>= quota.slow_down_at`):** code-ticket `reviewer`, `verifier`, and custom `stage` checks fall back within `pro` to the highest-ranked offered Gemini `pro` model when Gemini has room (`Off: true`), and `docs`/`chore` ticket reviews and `judge:` checks fall back within `flash` to the highest-ranked offered Gemini `flash` model when Gemini has room (`Off: true`), with the `check_rule_not_met` signal and inbox item saying why (since 1.26; flash check reviewing since 1.44; `slow_down_at` fallback since 1.46). `curator`, `planner`, `assistant`, `builder`, `tester`, `e2e`, `summary`, `prompt`, and `improver` already run on Gemini and are not touched.
- **With the defaults, the Gemini group at its limit or close to it:** non-check roles (`curator`, `planner`, `builder`, `tester`, `e2e`, `summary`, `prompt`, `assistant`, `improver`) stay on Gemini (falling back along their Gemini entries at a per-model limit, and waiting for the Gemini group's reset when the whole Gemini group is at its limit rather than moving onto Claude; since 1.40, extended to `planner` in 1.44). When the Gemini group is at its limit, a review of what an operator-pinned Claude builder built falls back to the same-bracket Claude model (`Off: true`) instead of waiting, and the `check_rule_not_met` signal and inbox item say why (since 1.26).
- **With the defaults, `agy` signed out:** no agent runs until you signed in again.
- **What a move costs.** The other group's limit is used up faster. A conversation that moves to another model may be rebuilt from its turns (12.10). The run records that it moved, and results are measured under what really ran.
- **A pin, a project's setting, and an agent you set by hand** are never moved (safety rule 15; since 1.22, built in 1.26, CREW-32). Such a run keeps its group as the pacing allows, and at the limit it waits; the reason and the inbox (`crew_cannot`) name the model of its bracket that has room, which you can pin. Until 1.22 such a run took the role's default list. A model that is gone is still passed over as 12.3 says.
- **Switching it off.** `agents.at_limit` is `move`. Set to `wait`, every run waits for its own CLI and group, as in 0.27. A project can set its own, in its `agents` settings (14.1).
- **When nothing can run,** because every group is at its limit or the CLI is signed out, every project waits for the first reset or sign-in.
- **Running subtasks.** A subtask that is stopped by a limit keeps its conversation. If a model of its bracket in another group can run (and its model was not set by hand), it continues on that in a new sandbox, and keeps it until it ends. Otherwise it continues from where it stopped when the limit resets, or sooner when the quota guard no longer holds its last run's group or model limit and Crew's own login check has read that group's windows after the subtask began to wait; a run's own report never ends a wait early (12.4). New runs go back to the first choice after the reset.

**Pacing.** The lead slots and the stage limits already bound how many agents run. The quota guard can lower the lead slots of a CLI, and hold back runs on a group, while a group is close to its limit.

**No money is counted** (since 0.53, the operator's choice). Crew has no budget, no price list, and no estimate of a cost. `agy` reports no cost, and nothing is billed per run. A run records its tokens in, out, and read from the cache, its turns, and its time; `agy` counts its thinking inside the tokens out, and its cache reads apart from the tokens in. The work is bounded by a run's turns and minutes, a ticket's fix rounds, the tickets approved with the OK and the share agents may add to them (7.7), and the subscriptions' windows above.

### 12.8 One runtime image, kept current

Everything an agent run depends on is in the runtime image, and the image is fixed by the Crew release it belongs to (16.1).

| Part | Fixed how |
| --- | --- |
| `agy` | By version, as the tools are (since 0.56): `versions.lock` names `AGY_VERSION`, the tarball `AGY_URL`, and its `AGY_SHA512`, which the vendor's release manifest gives; it is the manifest the vendor's install script reads, and the address names the version. Since 0.57 the build is given that tarball and downloads none: Crew's server, for a release it builds, and `crew-ctl` on the machine, for a build by hand, download it from the vendor's address alone (the adapter's whole pattern, with `AGY_VERSION` in it, read from the commit's `versions.lock`), over https with no redirect followed and nothing that names the installation, at most 512 MiB, and keep it only with the pin's sha512; the runtime image's build gets it as its build context `agy`, checks its sha512 again, and fails when `agy --version`, which the build runs only as `nobody`, is another; the install script is not run. A build given no tarball fails at its copy (in 0.56 the image downloaded it in the build's sandbox, through `storage.googleapis.com` on `sandbox.sources`). Before 0.56 the image took the newest on the day it was built, by that script, since the design took the vendor to offer no numbered download; the probe of a Sources sandbox (2026-10-22) showed the script reads a manifest with a version, a versioned address, and a checksum. The release records the version too (`release.json` `pins.agy`), and the image is kept, so a release always runs the same `agy`. It does not update itself in a sandbox: Crew sets `AGY_CLI_DISABLE_AUTO_UPDATE=true`, which the vendor documents. |
| Harness | `harness/` at the release's commit, built into the image for `agy` (12.1). |
| `crew-run` | Built with the release. |
| Tools | Go, Node, and the base image by version, as in styrman-crew. Docker, `gh`, gcloud, kubectl, and Chromium come from their package sources, at the version `versions.lock` names where the source lets a version be asked for. Where it does not, the release records what it got (16.12). |

Every run record names the Crew version, and that fixes all of the above. So a change in results can be traced to the release that changed a part.

**Keeping it current.**

1. Once a day the server looks for new versions (since 0.51; as follows since 0.56, the operator's answers of the twelfth session), at the hour of the backup, in the running server only, and when the operator presses Look now on Versions (`POST /crew/pins/look`), one look at a time. It reads `versions.lock` on `main` of Crew's project, so a bump that merged and is not released yet is not found again; since 0.58, where Crew's project has a remote, it also reads `versions.lock` on the remote's main while that follows `main`, only to hold back a finding the remote pins already: a paused project's `main` is not fetched by the pipeline and lags what the operator pushed (on dave it stood behind the agy pin). Every value a ticket names still comes from `main` and the publishers, never from a commit Crew has not taken. The look fetches the remote's main while Crew's project does not run, into the project's remote ref, as the pipeline's fetch does (it records the fetch on the project), and merges nothing; a running project's is the pipeline's to fetch, and the look reads the ref as it stands. A remote that does not answer, has no main or no `versions.lock`, or does not follow `main`, or whose main the operator refused (6.6; since 0.62), changes nothing, and the look's first line says so, naming no address; and what the publishers offer, from the server itself, with no secret and no sandbox: go.dev (the newest stable Go, and its linux-amd64 archive's sha256), nodejs.org (the newest of the pinned line, and its linux-x64 tarball's sha256), the npm registry (`chrome-devtools-mcp`'s version and integrity), the index digest of each pinned image's tag (Docker Hub with its anonymous token, and mcr; by a HEAD, which Docker Hub counts as no pull), `agy`'s manifest (the table above), and gh's apt index. It does not read the other apt pins (Docker's packages, gcloud, kubectl), the release assets (tofu, yq, hadolint, Caddy, Kata), or `PLAYWRIGHT_IMAGE`'s tag, whose version follows its npm package. Each value must have its whole shape (a version of digits and dots, a hex checksum of its length, an npm integrity, a `sha256:` digest, `agy`'s address by the vendor's pattern), so none holds a quote, a space, a `$`, or a line break, and a version comes with its checksum or not at all. A version has two to four parts of at most nine digits, none with a leading zero. Each answer is read up to 4 MiB in 30 seconds, and the publishers together within five minutes, https only, from the publisher's own address, with no redirect followed and no header that names the installation. A source that fails or answers out of shape gives nothing, and the others go on. A Go or Node version counts only when its image's tag, the pin's with the version replaced, is there, so no ticket is made before it can be done. Each look is kept with a line per source in Crew's words (`pin_looks`, the newest 30), and Versions shows the newest, and says when it is older than two days: the server makes no look when it starts, since an install would then make tickets the operator did not ask for, so a day it was down at the hour is not looked at. That is the logic of styrman-crew's `bump-pins` script, written again in Go against `versions.lock`; Crew keeps no copy of the script.
2. A new version becomes a ticket on Crew's own board (16.2), such as "Update Go to 1.27.4, from go.dev", a title that names the publisher, as an images' ticket names each image, so the release's notes show the operator each pin a publisher moved: the backend's (`backend · daily look`), of an agent's origin, so Analysis reads it and it cannot skip Review, and made also while Crew's project is paused, where it waits in Backlog. Its text gives each line of `versions.lock` to set and the files `tools/check-versions.py` lists for them, in Crew's own sentences; its checks are a `grep` of each line in `versions.lock`, and `check-versions.py`, which must keep passing. A tool and version gets one ticket, ever (since 0.56; before, a second was refused only while one was open): its key, such as `go@1.27.4`, is written in the transaction that adds the ticket (`pin_tickets`, schema step 66), so a closed or discarded ticket is not made again, and two looks at once make one; a newer version is a new key, and a Crew's project made again starts with none. The image digests that moved are one ticket, with a key each. No ticket is made while an open one of the look changes one of its lines: it waits for a later look. A ticket the board refuses costs its line, and the look goes on and is kept. A Go ticket moves `GO_IMAGE` to the new tag, and a Node ticket `WEB_NODE_IMAGE`; the Caddy images keep their own Go. With no Crew's project, or one without `versions.lock` on `main`, nothing is made, and the look's line says why.
3. The ticket is worked like any other, and the release is built and gated as 16.2 says: the gate runs one real ticket in the candidate's runtime image.
4. The result is a release candidate like any other. It changes what is under `image/` or `versions.lock`, which is code, so it gives notice and waits for Install (16.3, 16.4).

Nothing changes inside a running sandbox. A run keeps the image it started with.

**The first image.** It takes its contents from the latest styrman-crew (commit `5c9e6fb`, 2026-10-06) and the latest styrman-harness (commit `43faca0`, version 2.7.0).

### 12.9 Skills and context: the base, and what a project adds

styrman-harness is the base: a global context, skills, subagents, and a rule (17.3). Crew keeps that shape and adds a layer per project (R180).

| Layer | Holds | Where it lives | How it reaches a run |
| --- | --- | --- | --- |
| Base | The global context, the skills, the subagents, and the rules | `harness/` in Crew's source repository, plus operator customizations in `crew_harness_files` (since 1.35) | Built into the runtime image; operator customizations in `crew_harness_files` are overlaid into `/home/agent/.gemini/config/` when a non-judge run starts (`CustomHarnessSkeleton`), and global `baseline/` customizations apply through `BaselineText` without rebuilding the runtime image |
| Project | The project's own context, skills, and rules | In the project's repository on `main`: `AGENTS.md`, `.agents/skills/`, and `.agents/rules/`. This is where styrman-harness already puts them, and it is the one place for every CLI. | Sideloaded. When a sandbox starts, `crew-run` takes them from `main` and puts them beside the base, in the place the run's CLI reads, and refreshes them before each turn (`PrepareRun`). No image is rebuilt. |

**When an agent misses a skill (R181).**

| Step | What happens |
| --- | --- |
| 1. Ask | The agent calls `request_skill`: what it could not do well, where that showed, and what kind of skill would help. It does not wait. It carries on with what it has. |
| 2. A ticket | Crew creates a ticket of the kind `chore`, marked as a skill ticket, in the project's backlog. Several requests for the same thing become one ticket. |
| 3. Find or write | The builder of that ticket looks first in the base and in Crew's skill library. Then outside, among the open skills that styrman-harness already draws on, with its `find-skills` skill. If nothing fits, it writes the skill. |
| 4. Check | The ticket goes through the stages like any other. The reviewer reads the skill as what it is, instructions: does it stay inside the project, does it send code or data anywhere, does it try to loosen a rule. A skill from outside is pinned to a commit, and its source, licence, and pin are recorded in `.agents/skills/VENDORED.md`, as styrman-harness does. |
| 5. Loaded | When the ticket is Done, the skill is on `main`. Every run of the project that starts afterwards gets it. |
| 6. For every project | When the same skill was added in several projects, the improver proposes it for the base (10.4). It passes the generic check, goes into Crew's skill library, and ships in the runtime image with a release. |

- **You add one the same way** as any other change: with a ticket, with a push to `main`, or on the project's Settings screen, which lists the base skills, the project's skills, and the project's context.
- **Context** works like a skill. The project's `AGENTS.md` is written from the plan with the first documentation ticket, and changed by tickets after that.
- **Every run reads main's layer** (since 0.46). Before every turn `crew-run` writes main's copies of the files its CLI reads as instructions over the checkout, also for a builder whose branch is older than a skill merged since, unless the ticket's `may_change` names them. Then the branch's own copies are what the builder changes.
- **Several requests for the same thing** are one ticket: a request whose words overlap those of an earlier request of an open skill ticket by 0.5 or more (Jaccard) is added to it as a record.
- **The Settings screen and `/crew` Baseline tab edit the layers directly** (since 1.35): `/crew?tab=baseline` views, edits, adds, and resets global `baseline/` and `harness/` files (`GET/PUT/DELETE /api/crew/baseline/file`), and `/p/$project/settings` views base skills/rules/subagents, views/edits/deletes `AGENTS.md`, `.agents/skills/<name>/SKILL.md`, and `.agents/rules/<name>.md` directly as commits on `main` (`GET/PUT/DELETE /api/projects/{p}/skills/file`), or asks an agent to find or write a skill (`POST /api/projects/{p}/skills/request`). Since 1.37, saving or deleting a global or project skill also live-syncs the translated skill tree into `<CREW_DATA_DIR>/projects/<id>/home/.gemini/config/skills/` so a project's warm `agy acp` server loads the updated skills on the next session without rebuilding the runtime image.
- **A skill cannot change what Crew enforces.** Limits, checks, and what waits for the operator are in the backend, not in a prompt.
- **The rule for instruction files holds** (13.8). Only a ticket that names these files may change them, and a run that judges a ticket takes them from `main`.
- **Size.** Every run reads the whole context, so it has a size limit. A skill has none: a CLI reads its description, and the body only when it uses it.
- **Nothing crosses projects.** A project's layer stays in that project. Crew's skill library holds only skills that passed the generic check (10.5).

### 12.10 How a conversation is stored and continued

A headless run of a CLI runs once and ends. An agent must still know what it did before: a builder that comes back for a fix, a lead that looks at a ticket again, the operator's next message. Crew makes that a property of the system, not of the CLI (R185).

**A conversation** is one thread of turns in Crew. Every run belongs to exactly one, and so does every message the operator exchanges with an agent. It runs on one CLI at a time. It has Crew's own id, and the id its CLI gave it. When it moves to another CLI, it is rebuilt there and gets a new id at that CLI (the third way below).

The id `agy` gives a conversation is `conversation_id`. It is in `init`, in every `step_update`, and in `result`.

**It is stored three times, all in the database, and each copy has its own job.**

| Copy | Holds | Kept | For |
| --- | --- | --- | --- |
| `conversations` and `turns` | Every turn: who spoke, the text, the tool calls in short, the tokens, and the time. A summary Crew writes itself every `conversations.summary_every_turns` turns (20), for a run's conversation, the operator's about a project, and a ticket's chat; one about no project has none, and is rebuilt from its last turns. | For good | Showing and searching it, and rebuilding it |
| `events`, the trajectory (8.3) | Every event the CLI printed, complete | For good | Reading exactly what happened, running feedback on it, and making eval cases |
| `cli_files`: the CLI's own files of the conversation, as it wrote them in its home, redacted and sealed with the key file for their conversation and path (since 0.78) | What the CLI needs to continue | `keep_cli_files_days` (7) after a run's last turn, the machine's setting since 0.65; `conversations.keep_cli_files_days` (30) after the last turn of a conversation with the operator or of a ticket's chat (since 0.75) | Continuing with the CLI's own memory of it |

The turns are written while the turn happens, from the events, so they do not depend on the CLI's files. `crew-run` hands the CLI's own files to the backend after every turn, so a sandbox that dies loses at most the turn it was in. The run's home is removed with the sandbox. A conversation with the operator also keeps its work folder (11.8): after each turn the harness writes it, through the home's root and without following a link, into a file beside the home that the sandbox cannot reach; the backend takes the credentials' values out of each file with the run's own redactor, seals it with the key file for its conversation, stores it in `work_folders` when it changed, and writes it into the next sandbox's home before it starts. The CLI's own files are sealed the same way, for their conversation and path (since 0.78), and put back all or none: a turn whose files cannot be sealed stores none and removes the conversation's files of the turns before, a turn that keeps files removes the conversation's files of any other path, and a file that does not open, as one sealed with another key file, puts none back; either way the next sandbox is rebuilt from the turns, once when that turn keeps its file. A turn that leaves no file, as one that stopped for the login before it began or whose file could not be read, keeps those of the turn before.

**It is continued in one of three ways.** Crew takes the first that is possible.

| Way | When | How | What the agent remembers |
| --- | --- | --- | --- |
| 1. A new call in the same sandbox | The sandbox is still running: the goal loop of a run (11.3), a project's warm microVM (`crew-proj-<project>`, since 1.37), and a conversation with the operator while it is not idle | `session/load` (`sessionId`, `cwd`, `mcpServers`) on the warm `agy acp` server (or `agy -p --conversation <id>`). The CLI's files are still in its home (`~/.gemini/antigravity-cli/conversations/<id>.db`). | Everything |
| 2. A new process, the same files | The sandbox is gone: a fix round, a run that stopped at a limit or a sign-in, an update of Crew, the operator's next message after a pause | A new sandbox. Crew writes the CLI's files from the database into its home, in the place the CLI reads them (with `STEP_TYPE_ERROR` steps removed from `agy`'s `trajectorySummaries` in `<id>.db` so an earlier turn's quota error is not replayed as the new turn's own, and keeping a continued read-only run's base from `r.base_ref` so an unpushed branch continues from `main`; since 1.26, CREW-27, CREW-31). Then the same call as in the first way. | Everything the CLI kept, with its own compaction |
| 3. Rebuilt from the turns | The CLI's files are no longer kept, do not open with the key file (since 0.78), a newer version of the CLI cannot read them, or the next turn runs on another CLI. `agy` continues a conversation on another of its models, also of another family, (section 24, items 51, 58, and 69) | A new conversation whose prompt begins with "The conversation so far": Crew's summary of the older turns, then the last turns in full | What the summary and the last turns hold. The turn is marked as rebuilt, so the lower fidelity can be seen. |

**Which runs share a conversation.**

| Runs | Conversation | Why |
| --- | --- | --- |
| A builder's first build of a ticket, its goal-loop rounds, and its fix rounds | One, continued | The builder must remember what it built and why |
| Each parallel subtask | One each | They work on different branches |
| The lead on one ticket: its analysis, and looking at it again | One, continued | The second look builds on the first |
| The reviewer, the tester, the E2E tester, and the verifier | A new one every time | A check must not remember its last verdict as its own. It gets the earlier verdicts as records. |
| The lead on a project: breakdown, housekeeping, the final report | A new one each time | They start from the records, which are complete |
| A conversation with the operator (11.8) | One, for as long as it exists | |

**Between conversations, awareness comes from records, not from transcripts.** A reviewer knows what the builder did because the note, the diff, and the earlier reviews are in its prompt (8.6), and it can ask the Crew MCP for the rest. Memory, decisions, and summaries carry what a project learned from one ticket to the next. No agent that works on a ticket is given another agent's transcript. The assistant in a ticket's chat reads the trajectories of that ticket to answer the operator, and it works on nothing (11.9).

- **Long conversations.** Each CLI compacts its own context when it fills. The turns in the database stay complete.
- **Use.** Crew counts the use of a run from what each turn reports, and takes the difference where a CLI reports the total of the whole conversation. `agy` reports no cost, and its `usage`, `num_turns`, and `duration_seconds` for the whole conversation, also across microVMs (section 24, items 39 and 69).
- **An interrupted turn.** A run that was stopped in the middle of a turn continues with a message from Crew that says what happened: the limit, the sign-in, or the update.
- **What is verified in the spike:** that each CLI continues from a new sandbox when its files are put back from the database, and how much a rebuilt conversation loses (section 24, items 37 to 40, and 69).

### 12.11 Skills Registry: tiers, static vetting, VENDORED.md lock, and live ACP sync (M25 to M26, since 1.47)

The **Skills Registry** (`skills_registry` and `skill_installs` tables in `StateDB` paired with git-tracked `SKILL.md` bundles) unifies discovery, security vetting, version pinning, per-factory activation, and runtime telemetry across every skill tier (`R221`):

| Tier | Source &amp; Path | Governance &amp; Activation |
| --- | --- | --- |
| **1. Base Harness Skills** | `harness/skills/<name>/SKILL.md` + `crew_harness_files` (`12.9`) | Built into Crew's runtime image or overlaid globally from `/crew?tab=baseline`. Always read-only to project branches. |
| **2. Cross-Factory Shared Skills** | Promoted into `Knowledge API` (`9.11`, `R218`) after `internal/redact` scrub and generic check (`10.5`) | Discoverable across all factories on the installation without sharing sandboxes or credentials; installing into another factory mints a trackable `Tasks API` item (`R217`). |
| **3. Project-Local &amp; Dream-Synthesized Skills** | `.agents/skills/<name>/SKILL.md` on a factory's `main` (hand-authored or synthesized from `procedural` Dreamstore entries by `Memory API` Dreaming `10.8`) | Requires operator approval via ticket review; versioned on `main` under rule `13.8`. |
| **4. Vendored External Skills** | External repositories queried by `request_skill` (`12.9`, deduplicated via Jaccard `>= 0.5`) or operator catalog search | Staged in quarantine -> statically AST-scanned for prompt injection, secret/network exfiltration, symlink escape (`12.9`), or rule loosening -> SHA-pinned with source URL, commit SHA, and licence in `.agents/skills/VENDORED.md` (enforced by `skills_vendored` `7.1`) -> merged only on explicit operator `Allow` (`13.8`). |

- **Live sync to warm `agy` microVMs (`12.9`):** Activating, upgrading, or disabling a skill on `main` immediately mirrors the translated skill tree into `<CREW_DATA_DIR>/projects/<id>/home/.gemini/config/skills/` so warm `agy acp --stdio --process-per-session` sessions pick up changes on the very next turn without rebuilding containers.
- **Registry telemetry:** Tracks `validity_score` (`Memory API`), first-pass check win delta, estimated prompt-index byte/token footprint, scan verdict (`PASS` / `BLOCKED`), and per-factory install state (`installed_factories[]`) across the installation.
