# Recordings of `agy`

Made by the spike (M0) on 2026-10-13 with `agy` 1.3.4 in the stand-in runtime image, and scrubbed by `spike/scrub.py` before they were committed. The adapter's tests replay them (design 12.5). `spike/answers.md` says how each was made; the item is in brackets.

| File | What it holds |
| --- | --- |
| `normal-run-flash-low.jsonl` | A turn with two tools on `gemini-3.8-flash-low`, prompt as stream-json on standard input (52, 61) |
| `normal-run-claude-sonnet-low.jsonl` | The same turn on `claude-sonnet-5-5-low`, a model of the other family (49) |
| `run-command-8s.jsonl` | A turn that runs an 8-second command, as the spool received it (12) |
| `subagent-helper.jsonl` | A turn that starts a helper: `step_type: subagent` with `subagent_info` (15) |
| `mcp-tool-call.jsonl` | A call of a tool of an HTTP MCP server with a bearer, through `call_mcp_tool` (10) |
| `no-tools-call.jsonl` | A call under a settings file that allows no tool (79) |
| `effort-conflicts-with-name.jsonl` | `--effort high` with a name that ends in `low`: refused (48) |
| `refused-tool-headless.txt` | A tool refused in print mode; the run still ends with 0 (9, 52) |
| `no-login-result.jsonl` | The result after 60 seconds with no login (11) |
| `refused-login-models.txt`, `refused-login-run-result.jsonl` | A login that is present but refused: `agy models`, and a run (11) |
| `concurrent-renewal-failed-stderr.txt` | One of ten copies that renewed at once (2) |
| `limit-reached-result.jsonl`, `limit-reached-stderr.txt` | A run that reached the five-hour limit: exit code 3, `AGY_ERROR` (50) |
| `models.txt` | `agy models` on the operator's account (47) |
| `models-gemini-4.txt` | The list `agy models` gave the login check on dave on 2026-10-16, once Gemini 4 was offered: the names and display names as Crew stored them, in the form of `models.txt`; the order of its lines is not the recorded one. The baseline's lists name these models (design 0.42) |
| `usage.txt`, `usage-json.json`, `usage-stream-json.jsonl` | `agy -p /usage` as text, JSON, and stream-json (50) |
| `usage-after-limit.txt` | `/usage` with the Gemini group at its limit (50) |
| `credits.txt` | `agy -p /credits` with no credits (54) |
| `skills-list.txt` | `agy -p /skills` with marker skills in three places (33) |
