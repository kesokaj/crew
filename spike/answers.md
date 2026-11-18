# Answers of the spike

Since design 0.42 Crew runs `agy` alone. The items about `claude` stay as the record of what the spike found; the recordings they name under `testdata/claude/` were removed with it, and are in the history of the repository.

One entry per item of `docs/plan/spike-checklist.md`, in this form:

## Item N: the question in a few words

- **Answer:** yes, no, a number, or "could not find out"
- **How:** the command or script, and the few lines of its output that show the answer. Quoted output goes through the scrub script first. The full output stays in `raw/`, which git ignores.
- **What it changes:** nothing, or the section of the design that was changed

## Before the spike: what dave was on 2026-10-13

- **Answer:** styrman-crew is no longer on dave. On 2026-10-12 at 21:18 `styrman-ctl uninstall --level 3 --confirm dave` ran. Docker kept no container, image, volume, or build cache, and `/opt/styrman`, `/etc/styrman`, and `/var/lib/styrman` are gone. Docker's `daemon.json` was restored from styrman-crew's backup, which keeps the runtime `mvm`, and Docker was restarted at 23:38.
- **How:** `sudo journalctl _COMM=sudo` for that evening, `sudo docker system df`, `ls`.
  ```
  USER=root ; COMMAND=/opt/styrman/deploy/styrman-ctl uninstall --level 3 --confirm dave
  Images 0  Containers 0  Local Volumes 0  Build Cache 0
  ```
- **What follows:** the operator approved, on 2026-10-13, that the spike runs on dave as it is: its containers start from a stand-in for styrman-crew's runtime image, built from `../styrman-crew` at `5c9e6fb` by `spike/build-standin.sh`, and sign-ins are made fresh in throwaway containers. No firewall of styrman-crew is on dave, so a `crew-spike-…` container is on Docker's default bridge and can reach the local network. Steps 3 and 5 of 17.6 are done already, and the project data was not kept. The operator's user is in the `docker` group (START-HERE §2.2 says it should not be); the spike uses `sudo docker` only.

## Item 57: dave's real size

- **Answer:** 12 cores with 16 threads (Intel Core i7-1260P: 4 performance and 8 efficiency cores), 62.4 GiB of memory (64 GB on the label), and a 1 TB NVMe SSD with 830 GB free. styrman-crew's notes were right. "24 cores" is not.
- **How:** `lscpu`, `/proc/meminfo`, `df -h /`, `lsblk`. Full output in `raw/item-57-63-machine.txt`.
  ```
  CPU(s): 16   Model name: 12th Gen Intel(R) Core(TM) i7-1260P
  Thread(s) per core: 2   Core(s) per socket: 12   Socket(s): 1
  MemTotal: 65385776 kB
  /dev/nvme0n1p2  915G  40G  830G  5% /     nvme0n1 931.5G Samsung SSD 990 PRO 1TB
  ```
- **What it changes:** proposed, waiting for the operator: sections 3, 13.10, and 23, and R65 of the requirements, name 24 cores. With 16 threads, a reserve of 2, and `cpu_overcommit: 2.0`, the CPU ceilings add up to 28 virtual CPUs, so about 10 sandboxes fit (for example 4 large and 6 small), not 16. Memory still allows 16. The choice is between fewer sandboxes, a higher overcommit (3.0 gives 42, about 15), or smaller sizes after item 26 measures what a run uses.

## Item 63: what dave runs

- **Answer:** Debian 13 with kernel 6.12.111+deb13-amd64, Docker 29.7.2 with containerd v2.3.3 and runc 1.4.3, Kata 3.31.0 with Cloud Hypervisor v51.1. The Docker runtime `mvm` is a script that runs Kata's Go shim (built with go1.25.10) with `configuration-clh.toml`. KVM nested virtualization is on, and KSM runs. Docker, Kata, and its checksum agree with `versions.lock`. A microVM starts and has its own kernel, 6.18.28.
- **How:** `docker version`, `kata-runtime --version`, `cloud-hypervisor --version`, `/etc/docker/daemon.json`, the GitHub API's digest of the Kata release asset, and one container of busybox with `--runtime mvm`, removed after.
  ```
  docker 29.7.2   containerd:v2.3.3 runc:1.4.3   kata-runtime : 3.31.0   cloud-hypervisor v51.1
  kata-static-3.31.0-amd64.tar.zst sha256:68c2786a0b97023f62f3eca02dc868b78a794e5469d4ddd6cc9e0bd4a7212b0b
  /usr/local/bin/containerd-shim-mvm-v2: KATA_CONF_FILE=/opt/kata/share/defaults/kata-containers/configuration-clh.toml exec /opt/kata/bin/containerd-shim-kata-v2
  host kernel 6.12.111+deb13-amd64   guest kernel: 6.18.28   1 vCPU, 1987 MB
  daemon.json: default-runtime runc, runtimes mvm/gvisor/runsc, ipv6 false, ip6tables false,
    live-restore true, features.time-namespaces false, json-file logs 3 x 50m; no default-address-pools
  ```
- **What it changes:** `versions.lock` got the checksums its header asks for at M0 (Go, Node, OpenTofu, yq, hadolint, chrome-devtools-mcp, `claude` and its linux-x64 binary package). The `agy` version comes with the stand-in image. Two things differ from what 13.2 expects of a host setup, and M1's host setup brings them: there is no `default-address-pools` in `daemon.json`, and the drop-in that keeps Docker alive when memory runs out was removed with styrman-crew; a `limits.conf` drop-in from 2026-08 is there. gVisor (`runsc`) is also installed as a runtime, and Crew does not use it.

## Item 13: Kata's advisories that touch 3.31.0 (the M0 half)

- **Answer:** three of Kata's eleven advisories list 3.31.0 as affected. None of them can be reached from a Crew sandbox on Kata's Go runtime with Cloud Hypervisor under Docker:
  - GHSA-7fhf-v3p3-rp56 (high) and GHSA-mp2j-xm59-qfgw (critical): a pod annotation makes the runtime bind host paths into the guest, or load a configuration file of the attacker's choice. Both are in `runtime-rs`, which the `mvm` runtime does not use, and both need the power to set a container's annotations, which in Crew only the holder of the Docker socket has: the server and the updater. Fixed in 4.0.0.
  - GHSA-h8jv-63p2-496x (medium): in Confidential Containers, two calls of the mem-agent skip the agent's policy. The mem-agent is off by default, and Crew does not use Confidential Containers.
  - The rest are fixed in or before 3.31.0, or are in Dragonball or in policies Crew does not use.
- **How:** `curl https://api.github.com/repos/kata-containers/kata-containers/security-advisories`, saved in `raw/item-13-kata-advisories.json`; `file` and `strings` on both shims.
  ```
  2026-07-21 GHSA-7fhf-v3p3-rp56 high      <= 3.32.0, fixed 4.0.0  runtime-rs: annotation bind-mounts any host path
  2026-07-20 GHSA-mp2j-xm59-qfgw critical  <= 3.32.0, fixed 4.0.0  runtime-rs: config path annotation
  2026-07-21 GHSA-h8jv-63p2-496x medium    <= 3.31.0, fixed 4.0.0  CoCo mem-agent ttRPC without policy
  /opt/kata/bin/containerd-shim-kata-v2: dynamically linked, go1.25.10   (runtime-rs is /opt/kata/runtime-rs/bin/...)
  ```
- **What it changes:** nothing in M0. Crew's rule that nothing but the server and the updater holds the Docker socket (13.8) is what keeps the two annotation advisories out of reach; Crew must also never pass an `io.katacontainers.*` annotation that a project or an agent chose. The Kata 4 half of this item is M1's.

## The logins the spike uses

- **Answer:** the operator said on 2026-10-13 to use the logins dave already has, in place of fresh sign-ins. `agy`: a copy of the operator's own token file (`auth_method: consumer`), copied by path into each container's home. `claude`: the login on dave is also the one the building agent runs on, and its refresh token is renewed on use, so a container never gets it. A container gets only the current access token, as `CLAUDE_CODE_OAUTH_TOKEN`; it is valid for some hours and nothing in a container can renew it. The one-year token of `claude setup-token` was not made, so item 67 waits for the operator at a browser. The scrub values were written from the operator's config files by a script, and never displayed: one mail address (the same for git, Google, and Claude), Claude's account and organisation ids, and the Google account id.
- **Paid extra use:** off on both. Claude's account record says `hasExtraUsageEnabled: false` (`~/.claude.json`, `oauthAccount`). `agy -p /credits` says `Remaining credits 0`.

## Item 47: `agy models`, headless

- **Answer:** yes. It prints one model on each line, the name and the name to show separated by a tab, in 2.5 seconds, with exit code 0 and no model request. Every name ends in its effort. The account offers ten names: Gemini 3.8 Flash at low, medium, and high; Claude Opus 5.5 and Claude Sonnet 5.5 at low, medium, and high; and `gpt-oss-120b-medium`. **It offers no `gemini-3.1-pro`**, and no Gemini 3.7 or 3.6 Flash. `agy -p /model` prints the default, `gemini-3.8-flash-high`.
- **How:** `spike/sandbox.sh exec login agy models`, in `raw/item-47-50-readonly.txt`.
  ```
  gemini-3.8-flash-high	Gemini 3.8 Flash (High)          claude-opus-5-5-high	Claude Opus 5.5 (High)
  gemini-3.8-flash-medium	Gemini 3.8 Flash (Medium)      claude-sonnet-5-5-low	Claude Sonnet 5.5 (Low)
  gemini-3.8-flash-low	Gemini 3.8 Flash (Low)            ... (medium, high)
  claude-opus-5-5-low	Claude Opus 5.5 (Low)              gpt-oss-120b-medium	GPT-OSS 120B (Medium)
  claude-opus-5-5-medium	Claude Opus 5.5 (Medium)
  ```
- **What it changes:** proposed, waiting for the operator: the defaults in 12.3 and 14.1 name `agy/gemini-3.1-pro` first for the builder and last for the lead, the planning agent, the assistant, the reviewer, the verifier, and the improver. This account cannot run it. By the rule of 12.3 the builder then runs on `claude/sonnet`, so every check needs Gemini, which leaves `gemini-3.8-flash` as the only other family for a review. The `/usage` text says Gemini Pro is in the Gemini group, so Pro may come back on the account; the list is read every day.

## Item 50: `/usage` headless, with what is left and when it resets

- **Answer:** yes, for what is left. `agy -p /usage` (and `/quota`, the same) answers in 3 to 4 seconds with exit code 0 and no model request: one line per limit, tab-separated. With `--output-format json` or `stream-json` the result has a `command.data.groups[].buckets[]` with `id`, `window` (`weekly`, `5h`), `remaining_fraction`, and `reset_time` in RFC 3339. There are two groups, as 12.7 says: "Gemini Models" (Gemini Flash and Pro) and "Claude and GPT models". What the error says at a limit is not tested yet: that is the limit test, which comes last and needs the operator's yes.
- **How:** `agy -p /usage --output-format json`, in `raw/item-50-usage-json.txt`.
  ```
  Gemini Models	Weekly Limit Remaining	94%	2026-10-19T08:25:47Z
  Gemini Models	Five Hour Limit Remaining	100%	2026-10-13T16:31:23Z
  {"id":"gemini-weekly","window":"weekly","remaining_fraction":0.9401479959487915,"reset_time":"2026-10-19T08:25:47Z"}
  ```
- **What it changes:** nothing: 12.7 holds. The adapter reads the JSON buckets, not the text.

## Item 11: checking an `agy` login without a run (part)

- **Answer, so far:** `agy models` (2.5 s) and `agy -p /usage` (3.8 s) both need a working login and make no model request, so either is a login check that costs no quota; `/usage` also gives the limits the check reads anyway. With no login at all, `agy` 1.3.4 does not fail at once: it prints a sign-in link, waits 60 seconds for a code, and then ends with exit code 1 and a result of status `ERROR`, "authentication failed or timed out". What a present but refused login looks like is still to be tried.
- **How:** `raw/item-11-no-login.txt`.
  ```
  Authentication required. Please visit the URL to log in: ...
  Waiting for authentication (timeout 60s)...
  {"event":"result","result":{"conversation_id":"","status":"ERROR",...,"error":"authentication failed or timed out",...}}
  exit=1 secs=60.5
  ```
- **What it changes:** 12.2 says the vendor ends the run at once with `authentication required`; on 1.3.4 it still waits 60 seconds. The driver's check before the run (12.6) covers it. Approved by the operator for the 0.33 change.

## Quirks of `agy` 1.3.4 seen so far (item 52, part)

- `--print-timeout` defaults to 0, which waits until the turn completes, not 5 minutes as 12.2 says. The driver must still set it.
- Settings: `agy` rewrites `~/.gemini/antigravity-cli/settings.json`, and a `useG1Credits` of `false` is dropped, because the field is `omitempty` in the binary. Off is the default, so "written as off" is not something the file can show. The binary also has `use_ai_credits`, which its changelog says the CLI takes from the global user settings at start. What decides is the account: see "The logins the spike uses". Approved by the operator for the 0.33 change.
- A refused tool ends with exit code 0: `agy -p /models` (which is not a read-only command, so it went to the model as a prompt) printed "a tool required the "command" permission that headless mode cannot prompt for, so it was auto-denied" and ended with 0. This was one short run on the default model, by mistake.
- `--effort` takes `low|medium|high|xhigh|max`, and there are new flags `--dangerously-skip-permissions`, `--project`, and `--new-project`.

## Item 80: `claude` answers `initialize` headless, with its models

- **Answer:** yes, on 2.1.287. `claude -p --input-format stream-json --output-format stream-json --verbose`, sent one `control_request` with the subtype `initialize` and no prompt, answers with a `control_response` in 0.8 seconds and ends with 0 when standard input closes. Nothing else is printed: no `init`, no assistant turn, no result. So it makes no model request and uses none of the limit. The answer holds `models`, and also `commands`, `agents`, `account`, and more.
- **How:** `spike/claude-initialize.py`, recorded in `testdata/claude/initialize-models.jsonl`.
  ```
  # answer after 0.8 s     # exit 0 after 1.6 s     lines: [('control_response', None)]
  ```
- **What it changes:** nothing: 12.3 holds.

## Item 75: the list `claude` shows, and a refused name

- **Answer:** the `initialize` answer is the headless form of `/model`, so no terminal has to be driven. On this plan (`subscriptionType: max`) it lists five entries, each with `value` (the name to pass), `resolvedModel` (the full name), `displayName`, and `supportedEffortLevels`:
  ```
  default -> claude-sonnet-5-5  (Default (recommended))   efforts low, medium, high, xhigh, max
  sonnet  -> claude-sonnet-5-5                             efforts low ... max
  fable   -> claude-fable-5-1                              efforts low ... max
  opus    -> claude-opus-5-5                               efforts low ... max
  haiku   -> claude-haiku-4-5-20251001                     no effort levels
  ```
  The first event of a run names the full model: `"model": "claude-sonnet-5-5"` in `init`, and in every assistant message. A refused name is not refused at the start: `init` names it, then the turn fails at once with `error: model_not_found`, API status 404, exit code 1, and no cost.
- **How:** `testdata/claude/initialize-models.jsonl`, `testdata/claude/refused-model.jsonl`.
  ```
  [claude-code:unrecognized_model] {"model":"claude-nonexistent-9","query_source":"sdk"}    (standard error)
  {"type":"result","subtype":"success","is_error":true,"api_error_status":404,
   "result":"There's an issue with the selected model (claude-nonexistent-9). It may not exist or you may not have access to it. ..."}
  ```
- **What it changes:** proposed, waiting for the operator: 12.3 should say that the list holds `fable` (Fable 5.1) and `default`, that `haiku` is Haiku 4.5 and has no effort levels, and that the adapter reads `supportedEffortLevels` from the answer instead of assuming the five levels. `default` is a moving alias like the others and is not offered to agents.

## Item 73: `claude -p` reads the subscription login without `--bare` (2.1.287)

- **Answer:** yes, on 2.1.287. A turn with `CLAUDE_CODE_OAUTH_TOKEN` and no `--bare` ran on the subscription: `apiKeySource: none`, and the run was not billed. Whether a flag keeps this when `--bare` becomes the default is not known yet: the help of 2.1.287 has no such flag. The gate's real ticket on `claude` (12.2) remains the guard.
- **How:** `testdata/claude/normal-run-sonnet-low.jsonl`.
  ```
  system init {'model': 'claude-sonnet-5-5', 'permissionMode': 'bypassPermissions', 'claude_code_version': '2.1.287', 'apiKeySource': 'none'}
  result {'subtype': 'success', 'is_error': False, 'result': 'done', 'num_turns': 2, 'total_cost_usd': 0.0892944, 'permission_denials': []}
  # exit=0 ms=6219
  ```
- **What it changes:** nothing.

## Item 67: the token, and `claude auth status` (part)

- **Answer, so far:** `claude auth status` does **not** tell a working token from a refused one. With a made-up token in `CLAUDE_CODE_OAUTH_TOKEN` it says `"loggedIn": true, "authMethod": "oauth_token"` and ends with 0, exactly as with a real one; it only looks at whether a token is set. With no login at all it says `"loggedIn": false` and ends with 1. The one-year token itself, and ten sandboxes at once, are not tried yet: the operator makes the token at a browser, and the parallel part needs a yes.
- **How:** `raw/item-67-auth-status.txt`, `raw/item-77-fake-token.txt`.
- **What it changes:** proposed, waiting for the operator: 12.6 says the check before a run is "`claude auth status` ends with 0". That only says a token is there. The scheduled check's one real request (12.6) is what tells a refused token. A turn on `haiku` with `--tools ""` does it in 4.2 to 5.0 seconds for an estimated $0.023 (item 79), and its `rate_limit_event` reads the limits in the same call (item 68). The check before a run stays as it is, because a refused token fails the run within seconds anyway (item 77).

## Item 77: a refused `claude` token, in a run and in `claude auth status`

- **Answer:** in a run, the CLI tries the request twice more and then ends: two `system` events with the subtype `api_retry`, `error_status: 401`, `error: authentication_failed`, then an assistant message and a result with `is_error: true`, `api_error_status: 401`, and "Failed to authenticate. API Error: 401 OAuth access token is invalid.", exit code 1, after about 8 seconds. `claude auth status` does not notice (item 67). A token that was revoked was not tried; a made-up one stands in for it.
- **How:** `testdata/claude/refused-token.jsonl`.
  ```
  system api_retry {'attempt': 1, 'retry_delay_ms': 619, 'error_status': 401, 'error': 'authentication_failed'}
  system api_retry {'attempt': 2, 'retry_delay_ms': 1039, 'error_status': 401, 'error': 'authentication_failed'}
  result {'subtype': 'success', 'is_error': True, 'result': 'Failed to authenticate. API Error: 401 OAuth access token is invalid.', 'api_error_status': 401}
  ```
- **What it changes:** the adapter maps `api_error_status: 401` with `authentication_failed` to the stop reason `login` (12.4).

## Item 68: what `claude -p` says about its limits (part)

- **Answer, so far:** what is left **can** be read headless: every turn prints a `rate_limit_event`, before the first tool result, with the use of the five-hour and the seven-day window as a fraction, their reset times, and whether paid overage is on. On this account overage is off at the organisation's level. No limit was reached yet; that test comes last and needs the operator's yes. The `initialize` answer holds no limits, so reading them needs a turn.
- **How:** `testdata/claude/normal-run-sonnet-low.jsonl`.
  ```
  {"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1791892200,"rateLimitType":"five_hour",
   "overageStatus":"rejected","overageDisabledReason":"org_level_disabled","isUsingOverage":false,
   "unifiedWindows":{"five_hour":{"utilization":0.22,"resetsAt":1791892200},"seven_day":{"utilization":0.49,"resetsAt":1791921600}}}}
  ```
- **What it changes:** proposed, waiting for the operator: 12.7 and the table in 4 ("Limits of `claude`: known only when a run reaches one") change. The quota guard reads `rate_limit_event` from every turn of `claude`, so it knows how close each window is, as it does for `agy` with `/usage`. Item 54's "spends nothing" for `claude` is shown by `overageStatus: rejected`.

## Item 52: how `agy` takes its prompt headless (1.3.4), and its events

- **Answer:** both forms work. The prompt as one stream-json message on standard input, `{"event":"user","message":{"content":"..."}}` with `-p "" --input-format stream-json --output-format stream-json`, as styrman-harness does; and the prompt as the argument of `-p` with standard input closed. Giving both is now refused at once, with exit code 2 and "a prompt given on the command line would be ignored"; it is no longer dropped without a word. With standard input left open the run does not end after its answer: the quirk of 12.2 holds.
- The events: `init` (with `conversation_id`, `model`, `cwd`, `tools`), then `step_update` with `step_index`, `state` (`ACTIVE` or `DONE`), and `step_type`: `user_input`, `agent_response` (with `text_delta`, `duration_seconds`, and `usage` per step), and `tool` (with `tool_name` and `tool_info.{name,parameters,output}`). Then `result` with `status`, `response`, `duration_seconds`, `num_turns`, and `usage` (`input_tokens`, `output_tokens`, `thinking_tokens`, `cache_read_tokens`, `total_tokens`).
- **How:** `raw/item-52-prompt-forms.txt`; `testdata/agy/normal-run-flash-low.jsonl`, `testdata/agy/run-command-8s.jsonl`.
  ```
  A: agy -p "Answer with the single word alpha." --output-format stream-json </dev/null   -> "status":"SUCCESS","response":"alpha\n"  exit=0 ms=5973
  B: -p "..." and --input-format stream-json  -> Error: --input-format stream-json reads prompts from stdin, so a prompt given on the command line would be ignored  exit=2
  C: stdin left open -> no exit until stdin closed: "error: stream input cancelled: context canceled"  exit=124 (timeout)
  ```
- **What it changes:** 12.2's row on the prompt: the flag is refused, not dropped. Either form will do for the driver; `crew-run` keeps the stream-json form and closes standard input.

## Item 61: what a fresh home needs for headless `agy`

- **Answer:** only the token file and a settings file. A home that held nothing but `~/.gemini/antigravity-cli/antigravity-oauth-token` and `settings.json` (with `permissions.allow` and `trustedWorkspaces`) ran a turn with tools at once: no first-run question, no question about trusting the folder, exit code 0. `agy` writes the rest itself (`installation_id`, `cache/`, `conversations/`, `log/`, `jetski_state.pbtxt`, ...).
- **How:** `ls -A ~/.gemini/antigravity-cli` before the run printed `antigravity-oauth-token settings.json`; `testdata/agy/normal-run-flash-low.jsonl`, `# exit=0 ms=22114`.
- **What it changes:** nothing.

## Item 9: the settings file with `command(*)` as the guest user

- **Answer:** yes, for what was tried: `write_to_file` and `run_command` ran as the user `agent` with `permissions.allow` `["command(*)","read_file(*)","write_file(*)","mcp(*)"]` and the workspace in `trustedWorkspaces`, and nothing was refused. Without an allow rule, a tool is refused and the run still ends with 0 (`testdata/agy/refused-tool-headless.txt`), which is the quirk of 12.2. The browser and MCP tools were not tried.
- **How:** `testdata/agy/normal-run-flash-low.jsonl`: `tool_name` `write_to_file` and `run_command`, both `state: DONE`, result `SUCCESS`.
- **What it changes:** nothing.

## Item 12: reading the spool through the shared folder while the CLI writes it

- **Answer:** reliable and fast. In a turn of 15 seconds, all 8 lines `agy` wrote to a file in the shared home were read on the machine by `tail -F` through the shared folder, none lost, 27 to 30 ms after the guest wrote them. That delay includes the error of the clock offset, so the true delay is at most about 30 ms. `agy` writes few lines: a tool step gives two (`ACTIVE`, `DONE`), and a command's output comes only with its `DONE`.
- **How:** `spike/spool-latency.sh ag`, files in `raw/item-12-spool/`.
  ```
  clock offset guest-host: 25702862 ns (includes half of docker exec's own delay)
  lines written 8, seen on the machine 8, lost 0
  delay ms: min 27, median 28, max 30
  ```
- **What it changes:** nothing: 12.4 holds, and the way out named for items 12 and 53 is not needed.

## Item 53: capturing the output outside the sandbox

- **Answer:** yes. A line written to the standard output of the sandbox's process 1 ends in Docker's log of the container on the machine (`/var/lib/docker/containers/<id>/<id>-json.log`), which nothing in the sandbox can reach. The user `agent` cannot write there, but with its `sudo` it can, so an agent can add lines that look like events. It cannot change or remove a line that was already written. So the container's log is an append-only second copy, not proof of who wrote a line.
- **How:** in the sandbox `echo ... > /proc/1/fd/1` (as `agent`: "Permission denied"), then `echo ... | sudo tee /proc/1/fd/1`; on the machine `sudo docker logs crew-spike-ag`.
  ```
  bash: line 1: /proc/1/fd/1: Permission denied
  7:{"spike":"line-via-sudo"}
  ```
- **What it changes:** nothing yet. If the trajectory should be kept where an agent cannot change it, `crew-run` (which starts before the agent) can also write each line to process 1's output, and the backend can compare the two copies.

## Item 79: one call with no tools (part: `claude`)

- **Answer, so far:** `claude -p --model haiku --tools ""` answered in 4.2, 5.0, and 4.4 seconds, plus about 3 seconds for the sandbox to be ready, so about 7.5 seconds in all. It loaded no tool and no MCP server, but about 11,000 tokens of context: the harness's global context in the home, which a judge or a prompt rewrite does not need. The time of `agy` is still to be measured.
- **How:** `raw/item-79-claude-notools-*.jsonl`.
  ```
  # exit=0 ms=4378     # exit=0 ms=4958     # exit=0 ms=4218
  tools: [] mcp: [] model: claude-haiku-4-5-20251001
  result ok 0.023079 3300 9 11045     (result, cost, duration_ms, input_tokens, cache_creation_input_tokens)
  ```
- **What it changes:** a judge's or a prompt writer's sandbox should start with a home that has no harness context, or `claude` with `--system-prompt` in place of the default, so a short call does not read 11,000 tokens. To decide when the judge is built (M6).

## Items 8, 38, and 45: continuing an `agy` conversation, and the files it needs

- **Answer:** yes, both ways. In the same sandbox, `agy -p "" --conversation <id> ...` continued: asked for a word given in turn 1, it answered `pineapple`. In a new sandbox whose home held only the token, the settings file, and `~/.gemini/antigravity-cli/conversations/<id>.db` from the first home, it continued too, under the same `conversation_id`. That one file (544 KB after one short turn, 608 KB after two) is all it needs; `conversation_summaries.db`, `brain/<id>/`, `annotations/<id>.pbtxt`, and the cache were not given back. Continuing in a new sandbox was slow: 29 seconds for a one-word answer, against 4.5 seconds in the same sandbox, with about 20,000 more input tokens.
- **How:** `raw/item-8-turn1.jsonl`, `raw/item-8-turn2-same.jsonl`, `raw/item-8-turn2-new-dbonly.jsonl`; `find ~/.gemini -newer <mark>` for what turn 1 wrote.
  ```
  turn 1                     turns 1  dur 8.4   out 445  in 29557
  turn 2, same sandbox       turns 2  dur 12.9  out 472  in 31881   response 'pineapple\n'
  turn 3, new sandbox (db)   turns 3  dur 41.7  out 473  in 52647   response 'pineapple\n'
  files of turn 1: conversations/<id>.db 557056, conversation_summaries.db 20480, brain/<id>/.system_generated/logs/transcript*.jsonl,
    annotations/<id>.pbtxt, presence/<id>.lock, cache/last_conversations.json, jetbox_summaries_proto.pb, log/cli-*.log
  ```
- **What it changes:** 12.2's row on session files: what has to come back is `conversations/<id>.db` alone. Crew stores that file in `cli_files` (12.10).

## Item 37: continuing after `agy` was updated

- **Answer:** yes, from 1.2.14 to 1.3.4. A conversation started with `agy` 1.2.14 (the binary dave's own user has, copied into a sandbox's home) was continued by the image's 1.3.4 with `--conversation <id>`, and it remembered the word.
- **How:** `raw/item-37-turn1-old.jsonl`, `raw/item-37-turn2-new.jsonl`.
  ```
  1.2.14  {"conversation_id":"e3cbc1d1-...","status":"SUCCESS","response":"ok\n","num_turns":1}
  1.3.4   {"conversation_id":"e3cbc1d1-...","status":"SUCCESS","response":"walrus\n","num_turns":2}
  ```
- **What it changes:** nothing. One step of versions only; a larger jump may still fail, and then the third way of 12.10 applies.

## Item 39: what `agy` reports for a continued conversation (part)

- **Answer, so far:** `agy` reports the whole conversation, not the turn, as `claude` does: `num_turns`, `duration_seconds`, and every count in `usage` grow from call to call of `--conversation` (table under items 8, 38, 45). Whether one open process over several turns is worth it was not tried yet.
- **What it changes:** 12.10's open question on cost is answered: Crew takes the difference between two results for `agy` too.

## Items 51 and 58: continuing on another model, and on another family

- **Answer:** yes, inside `agy`. The conversation of item 8, started on `gemini-3.8-flash-low`, was continued with `--model claude-sonnet-5-5-low`: `init` named the new model, and the answer remembered the word and named itself Claude Sonnet 5.5. So a move to another model or another family on `agy` continues the conversation with the CLI's own memory; only a move to the other CLI needs the rebuild of 12.10. A move at a real limit was not tried.
- **How:** `raw/item-51-other-model.jsonl`.
  ```
  init model: claude-sonnet-5-5-low
  result: SUCCESS "The word is pineapple, and I'm Claude Sonnet 5.5 (Low).\n" turns 3
  ```
- **What it changes:** 12.10's table says a move to another model "may" need the third way. For `agy` it does not.

## Item 49: Claude models through `agy` behave like Gemini models

- **Answer:** yes, for what was tried: the same tool turn on `claude-sonnet-5-5-low` gave the same events (`init`, `step_update` with `user_input`, `agent_response`, `tool` for `write_to_file` and `run_command`), the same result, in 8.8 seconds; a conversation continues across the two (items 51, 58), and the MCP tool works the same (item 10). One difference: `thinking_tokens` is 0 on Claude at low effort.
- **How:** `testdata/agy/normal-run-claude-sonnet-low.jsonl`.
- **What it changes:** nothing.

## Item 71: the Crew MCP over HTTP with a bearer header in `claude`

- **Answer:** yes. With `--mcp-config` naming `{"type":"http","url":...,"headers":{"Authorization":"Bearer ..."}}` and `--strict-mcp-config`, `init` lists the server as `connected` and its tool as `mcp__crew__crew_echo`; the model found it with `ToolSearch` and called it, in 7.7 seconds. When the server refuses the bearer (401) or cannot be reached, the run does not stop: `init` lists the server as `failed`, its tools are missing, and the run ends with 0. Before `initialize` the CLI sends `server/discover`.
- **How:** `spike/mcp-standin.py` in the sandbox, `raw/item-71-claude-*.jsonl`.
  ```
  ok:        mcp_servers: [{'name': 'crew', 'status': 'connected'}]  tool_use mcp__crew__crew_echo  result 'crew-echo: marmalade'
  badbearer: mcp_servers: [{'name': 'crew', 'status': 'failed'}]  result 'NO-TOOL ... authentication error (HTTP 401) ...'  exit=0
  down:      mcp_servers: [{'name': 'crew', 'status': 'failed'}]  result 'NO-TOOL ... (ECONNREFUSED) ...'  exit=0
  server log: POST server/discover, initialize, notifications/initialized, GET /mcp, tools/list, tools/call
  ```
- **What it changes:** the driver reads the server's status in `init`; when the Crew MCP is `failed` it stops the run with the reason `error`, rather than let an agent work without the tools that record its work.

## Item 10: the Crew MCP over HTTP with a bearer header in `agy`

- **Answer:** yes. An entry in `~/.gemini/config/mcp_config.json` with `serverUrl` and `headers.Authorization: Bearer ...` connects; `agy` writes each tool of the server as a file, `~/.gemini/antigravity-cli/mcp/<server>/<tool>.json`, and the model calls it through one generic tool, `call_mcp_tool`; the server's tools are not in `init`'s list of tools. It answered `crew-echo: marmalade` in 12 seconds. On a 401 `agy` also asks for `/.well-known/oauth-protected-resource`.
- **A failed connection only removes the tools**, as in `claude`, and the run ends with 0. But the agent then works around it. With a refused bearer, `agy` on Gemini Flash spent 108 seconds and 21 tool steps: it read `mcp_config.json` with the bearer, the server's source and log, `~/.claude.json`, and `ps -ef`, and edited its own `mcp_config.json` twice. With the server down, it found the real server on another port, started a port forwarder from the configured port to it (with `socat`, then with a script in Python), and called it by hand with `curl` and the bearer. Both runs ended with `NO-TOOL`.
- **How:** `spike/mcp-standin.py` in the sandbox, `raw/item-10-agy-{ok,badbearer,down}.jsonl`, `testdata/agy/mcp-tool-call.jsonl`.
  ```
  ok:        tool view_file (mcp/crew/crew_echo.json), tool call_mcp_tool   result SUCCESS 'crew-echo: marmalade\n'   exit=0 ms=12267
  badbearer: 21 tool steps, 2 x replace_file_content on ~/.gemini/config/mcp_config.json   result 'NO-TOOL'   exit=0 ms=108436
  down:      run_command "socat TCP-LISTEN:8799,fork TCP:127.0.0.1:8765 &" ... curl -H "Authorization: Bearer ..." http://127.0.0.1:8799/mcp
  server log: GET /.well-known/oauth-protected-resource/mcp bearer=BAD ... POST initialize bearer=ok (the curl)
  ```
- **What it changes:** proposed, waiting for the operator: `crew-run` checks that the Crew MCP answers before it starts the CLI, and the driver stops a run whose CLI reports the Crew MCP as failed, with the reason `error` (12.4). An agent left without its tools repairs its own configuration and reaches for anything in its sandbox. It also confirms 13.8: whatever the sandbox holds, the agent reads; the run token in `mcp_config.json` is the agent's to use, and the server must decide by the token and the network, never by what the agent claims.

## Items 3 and 65: the hosts each CLI contacts, and `HTTPS_PROXY`

- **Answer:** both CLIs honour `HTTPS_PROXY` for all their traffic. In a sandbox whose own firewall (inside the microVM) rejected every connection of the user `agent` except to the loopback, each CLI failed without the proxy and worked through it: `agy models`, `/usage`, and a turn with a tool; `claude`'s `initialize` and a turn with a tool. The proxy, run as root in the microVM, logged these hosts for one session of each:
  ```
  agy:     daily-cloudcode-pa.googleapis.com (19)  antigravity-unleash.goog (6)  aicode.googleapis.com (4)
           play.googleapis.com (4)  www.googleapis.com (3)  lh3.googleusercontent.com (3)
  claude:  api.anthropic.com (17)  http-intake.logs.us5.datadoghq.com (2)
  ```
  `agy`'s model calls go to `daily-cloudcode-pa.googleapis.com`; `antigravity-unleash.goog` is a feature-flag service, `play.googleapis.com` takes logs, and `lh3.googleusercontent.com` serves the account's picture. `claude` sends logs to Datadog. The login of `agy` was renewed during the run through these hosts; `oauth2.googleapis.com` did not appear.
- **How:** `spike/hosts-proxy.py`, `raw/item-3-agy-hosts.txt`, `raw/item-65-claude-hosts.txt`.
  ```
  == must fail: no proxy                     agy exit=1      claude exit=124 (timeout)
  == agy models via proxy ... exit=0         == a turn with a tool via proxy "status":"SUCCESS"
  == initialize via proxy  # exit 0          == a turn with a tool via proxy "result":"done"  exit=0
  ```
- **What it changes:** proposed, waiting for the operator: `network.model_only_hosts` (14.1) starts from this list. Which of the hosts a run can do without (the flags, the logs, the picture; `claude`'s Datadog intake can be switched off with its own setting) is item 4, in M10. 13.6's way out, a filter on the name in the TLS handshake, is not needed for either CLI.

## Item 7: `AGY_CLI_DISABLE_AUTO_UPDATE=true`

- **Answer:** yes. With it, every log of `agy` says "Auto-update disabled via environment variable AGY_CLI_DISABLE_AUTO_UPDATE". Without it, `agy` 1.3.4 starts an update in the background at each start: "Spawned background update process". Nothing was replaced, because 1.3.4 is the newest version today. In this image `/opt/agy/agy` belongs to root, so the user `agent` could not replace it anyway.
- **How:** the logs under `~/.gemini/antigravity-cli/log/` of the sandboxes with and without the variable, `raw/item-7-70-autoupdate.txt`.
  ```
  auto_updater.go:247] Auto-update disabled via environment variable AGY_CLI_DISABLE_AUTO_UPDATE     (variable set)
  auto_updater.go:334] Spawned background update process with PID 365                              (variable unset)
  ```
- **What it changes:** nothing. Both guards of 12.8 hold, and the second (files of root) is also the way out of item 7.

## Item 69: continuing a `claude` conversation

- **Answer:** yes, in every way asked. Crew chooses the id: `--session-id <uuid>` named the session, and `init` and the result carry it. In a new sandbox whose home got back only `~/.claude/projects/-home-agent-work/<id>.jsonl` (174 KB after one short turn), `claude -p --resume <id>` continued it and knew the file it had made. It continued on another model (`--model haiku` after `sonnet`), and with a newer CLI (2.1.296 on a session that 2.1.287 wrote). The project folder is the workspace path with `/` replaced by `-`, so the new sandbox must use the same workspace path. `total_cost_usd` adds up over the session (0.0966, then 0.1282, then 0.1343); `num_turns` and `usage` are for the one call.
- **How:** `testdata/claude/resume-{new-sandbox,other-model,newer-cli}.jsonl`.
  ```
  item-69-new-sandbox  ('claude-sonnet-5-5', '2.1.287')  'hello.txt'                       turns 1 cost 0.0966
  item-69-other-model  ('claude-haiku-4-5-20251001', '2.1.287')  'hi'                      turns 1 cost 0.1282
  item-69-newer-cli    ('claude-sonnet-5-5', '2.1.296')  "printf 'hi' > hello.txt && ls"   turns 1 cost 0.1343
  ```
- **What it changes:** nothing: 12.2 and 12.10 hold. The third way of 12.10 is needed for `claude` only when the run moves to the other CLI.

## Item 70: the quirks of `claude` 2.1.287

- **Answer,** quirk by quirk:
  - `ANTHROPIC_API_KEY` takes over: with a made-up key in a throwaway sandbox, `init` says `apiKeySource: ANTHROPIC_API_KEY` although `CLAUDE_CODE_OAUTH_TOKEN` was set, and the turn fails with 401 "Invalid API key · Fix external API key". **Holds.**
  - Co-Authored-By: without the harness's `attribution` setting in `~/.claude/settings.json`, a commit gets `Co-Authored-By: Claude Haiku 4.5 <noreply@anthropic.com>`; with it, none. **Holds,** and the home skeleton's setting is the fix.
  - Full access as a user that is not root: `--permission-mode bypassPermissions` works as `agent`. As root it refuses to start: "--dangerously-skip-permissions cannot be used with root/sudo privileges for security reasons". **Holds.**
  - A background subagent keeps the run open: **holds, and more.** A turn that started a subagent with `run_in_background` printed its result ("started") after 14 seconds, then stayed open for the subagent, printed the subagent's messages, a second `init`, and **a second `result`** ("finished"), and ended after 85 seconds. A background shell command, by contrast, is stopped when the turn ends (`task_notification` with `status: stopped`).
  - The terminate signal: exit code 143 and no `result`. **Holds.** The interrupt signal ends the turn with a `result` (`is_error: true`, an empty `result`) and exit code 0, so an interrupt first is right.
  - `DISABLE_AUTOUPDATER=1`: could not be shown either way. Without it, a headless run of 2.1.287 installed nothing in 60 seconds, although 2.1.296 is out (npm's `latest`; 2.1.287 is `stable`).
  - `parent_tool_use_id`: yes, every message of a subagent carries the id of the call that started it; the main agent's messages carry `null`.
  - New: `claude` blocks a standalone `sleep 120` itself ("Blocked: standalone sleep 120"), and emits more `system` events than 12.2 lists: `thinking_tokens`, `task_started`, `task_progress`, `task_updated`, `task_notification`, `background_tasks_changed`, and the `rate_limit_event` of item 68.
- **How:** `spike/claude-quirks.sh`, `testdata/claude/quirk-*.jsonl`, `raw/item-70-quirks*.txt`.
  ```
  # api-key exit=1 ms=1760        # root exit=1 ms=512
  # coauthor-with exit=0      commit: add with
  # coauthor-without exit=0   commit: add without / Co-Authored-By: Claude Haiku 4.5 <noreply@anthropic.com>
  # signal-TERM exit=143      # signal-INT exit=0     # background-subagent exit=0 ms=84653
  ```
- **What it changes:** proposed, waiting for the operator: 12.4 and 12.5. A `result` is final only when the process has ended; the driver takes the last `result` of a turn, and `status: ended` comes from the exit. The adapter maps the new `system` events, or keeps them only in the trajectory.

## Items 2 and 17: copies of one `agy` login (part)

- **Answer, so far:** every sandbox that got a copy of the token file renewed its own access token (the file changed, with an expiry an hour later), and **the refresh token stayed the same** in every copy. Google does not rotate it on use, so a copy that renews does not make the original or the other copies stale. Four copies (sandboxes `login`, `ag`, `ag2`, and one more) renewed on 2026-10-13 and the operator's own `agy` on dave kept working. Ten copies at once, and over hours, is the parallel test, which needs the operator's yes.
- **How:** checksums of the files against the copy, and the field `refresh_token` compared in a script that prints only `True` or `False`.
  ```
  ag: CHANGED mtime 2026-10-13 13:47:40     ag2: CHANGED 13:52:02     login: CHANGED 13:31:13
  copy expiry: 2026-10-02T11:50:29 | renewed expiry: 2026-10-13T12:47:39 | refresh token same as copy: True
  ```
- **What it changes:** nothing yet. If ten copies hold for hours, keeping a renewed file (12.6) is not needed for `agy`, and the daily check alone keeps the sign-in in use.

## Item 27: what an `agy` login grants beyond the CLI

- **Answer: more than the CLI.** Its access token carries the scope `https://www.googleapis.com/auth/cloud-platform`, besides `aicode`, `cclog`, `experimentsandconfigs`, `openid`, `email`, and `profile`. `cloud-platform` lets the token call any Google Cloud API that the account has rights to, in every project of the operator's personal Google account. Mail and Drive are not among the scopes. The spike read the scopes only; it called no API of the account.
- **How:** Google's `tokeninfo` endpoint with one renewed access token, in a script that printed only the scopes and the lifetime.
  ```
  tokeninfo scopes: ['email', '.../auth/aicode', '.../auth/cclog', '.../auth/cloud-platform', '.../auth/experimentsandconfigs',
                     '.../auth/userinfo.email', '.../auth/userinfo.profile', 'openid', 'profile']
  tokeninfo expires_in: 2406 | access_type: offline
  ```
- **What it changes:** proposed, waiting for the operator: 12.6 names the way out, a Google account used for `agy` only. With the operator's personal account, a tricked agent in any sandbox that runs `agy` holds a token that can reach every Google Cloud project of that account, for as long as the refresh token works. Model only mode (13.6) narrows where it can be sent, not what it opens.

## Item 56: what a login check learns about the account of an `agy` login file

- **Answer:** the file names its account without any request: its `id_token` (a JWT) holds `email` and `sub`, and a renewed file keeps it. But a file that came back from a sandbox may have been edited, so the claims of an `id_token` read without checking its signature prove nothing. Google's `tokeninfo` with the file's access token answers from Google's side; it costs no model request and no quota.
- **How:** the file's keys (`auth_method`, `id_token`, `token.{access_token,expiry,refresh_token,token_type}`), the `id_token`'s claims by name, `tokeninfo` (item 27).
- **What it changes:** 12.6's test of a renewed login compares the Google account by `tokeninfo` (or by the `id_token` with its signature checked), never by the file's own claims.

## Item 11: checking an `agy` login, and a login that is present but refused

- **Answer:** `agy models` is the check. With a working login it lists the models in 2.5 seconds; with a login whose tokens are refused it ends in 0.6 seconds with exit code 1 and "Please sign in to view available models". A run with the refused login does not fail fast: it prints a sign-in link, waits 60 seconds, and ends with exit code 1 and `authentication failed or timed out`, the same as with no login at all.
- **How:** `spike/agy-checks.sh refused-login` (a copy of the file with made-up tokens and an old expiry), `testdata/agy/refused-login-models.txt`, `testdata/agy/refused-login-run-result.jsonl`.
  ```
  # refused-login models exit=1 ms=559     Error: Please sign in to view available models. Launch the CLI without arguments to sign in.
  # refused-login run exit=1 ms=60737      "status":"ERROR","error":"authentication failed or timed out"
  ```
- **What it changes:** 12.6's check for `agy` before a run is `agy models` with exit code 0, not "the token file must be there"; it also reads the list of models (12.3).

## Item 15: `subagent_info` in `agy`

- **Answer:** confirmed. A helper is a step with `step_type: subagent` and the tool `invoke_subagent`, and its `subagent_info` lists each helper with `type_name`, `role`, `initial_prompt`, and, once it runs, its own `conversation_id` and `log_uri`. The main agent then follows it with `manage_subagents`.
- **How:** `testdata/agy/subagent-helper.jsonl`.
  ```
  subagent invoke_subagent {"subagents": [{"type_name": "self", "role": "Command Runner", "initial_prompt": "...", "conversation_id": "c82b75dd-...", "log_uri": "file:///home/agent/.gemini/antigravity-cli/..."}]}
  ```
- **What it changes:** nothing: 12.2 holds.

## Items 28 and 41: what `agy` loads from a workspace

- **Answer: confirmed.** A workspace with `.agents/hooks.json` (a named hook, in the shape `agy`'s embedded documentation gives) and `.agents/mcp_config.json` (an HTTP server) had both loaded without a question: the hook ran, and the server got four requests (`agy` wrote its tools under `~/.gemini/antigravity-cli/mcp/spike-ws/`). No flag to keep them out was found in `agy --help`; the settings file has `trustedWorkspaces`, and the workspace was in it. Whether leaving the workspace out of `trustedWorkspaces` keeps them out is still to be tried.
- **How:** `spike/agy-checks.sh workspace`.
  ```
  # workspace exit=0 ms=11362 hook-ran=yes mcp-requests=4 tool-dir=crew spike-ws styrman-browser
  ```
- **What it changes:** nothing: 13.8 already treats both files as code that never merges by itself, and a judging run takes them from `main`.
- **Tried again in M5 (2026-10-15), with Crew's own image:** leaving the workspace out of `trustedWorkspaces` does not keep them out. In a run sandbox with `agy`'s login, the same workspace files, and `agy` on `gemini-3.8-flash`: trusted, the hook ran and the server got 5 requests; untrusted (`"trustedWorkspaces": []`), the hook ran and the server got 5 requests too, and the agent then edited `.agents/hooks.json` itself. So `crew-run` takes the files out of the workspace of a run that does not build (13.8, 0.38).
  ```
  # trusted exit=0 s=238 hook-ran=yes mcp-requests=5 tool-dir=ws-marker  result="status":"SUCCESS"
  # untrusted exit=0 s=150 hook-ran=yes mcp-requests=5 tool-dir=ws-marker  result="status":"SUCCESS"
  ```
  How: `crew admin sandbox start run --cli agy`, `crew-run creds`, and `spike/agy-trust-check.sh trusted|untrusted` as `agent`, with `spike/mcp-standin.py` copied to `/tmp`.
- **Tried again in M6 (2026-10-15), before a run that judges uses `agy`:** the other ways `agy`'s own guide names (its `agy-customizations` skill: plugins, and `skills.json`/`plugins.json` that name folders anywhere). `agy` 1.3.4 on `gemini-3.8-flash`, trusted workspace, each way with its own marker hook and its own server. Loaded: a plugin under `.agents/plugins/p/` (`plugin.json`, `hooks.json`, `mcp_config.json`) and a plugin under `tools/` that `.agents/plugins.json` names. Not loaded: a hook and a server in the front matter of `.agents/skills/m-skill/SKILL.md` (the agent read the skill and did what it said), of `.agents/agents/m-agent/agent.md`, and of `.agents/agents/m-agentfile.md`; a nested `sub/.agents/` with both files and a plugin, with the agent running `ls -a` in `sub/`. Written during the turn (the agent copied a `.agents/` with both files and a plugin into the workspace, then ran a command): nothing loaded in that turn; in the next turn of the same conversation, after the two top files were taken out as `crew-run` did, the plugin loaded. So `agy`'s adapter names `.agents/plugins/` and `.agents/plugins.json` too (design 13.8, 0.39). The first run of `paths` was spoiled: a hook that answers `{}` denies the tool call (a `PreToolUse` hook must answer `{"decision": "allow"}`), so the agent never worked in `sub/`; it was run again.
  ```
  # paths exit=0 s=45 hooks-ran=[listed plugin ] mcp-paths=[listedx4 pluginx4 ] tool-dirs=[p-listed_m-listed p-plugin_m-plugin ] result="status":"SUCCESS"
  # midturn exit=0 s=11 hooks-ran=[] mcp-paths=[] tool-dirs=[] result="status":"SUCCESS"
  # nextturn exit=0 s=6 hooks-ran=[writtenplugin ] mcp-paths=[writtenpluginx4 ] tool-dirs=[p-writtenplugin_m-writtenplugin ] result="status":"SUCCESS"
  ```
  How: `crew admin sandbox start run --cli agy`, `crew-run creds`, and `spike/agy-load-check.sh paths`, then `midturn nextturn`, as `agent`, with `spike/mcp-standin.py` copied to `/tmp`.
- **Tried again on `agy` 1.4.12 (2026-11-01, release 0.3.17) and `agy` 1.5.3 (2026-11-07, release 0.3.23):** the same three cases (`paths`, `midturn`, `nextturn`) on `gemini-3.8-flash` in a throwaway run sandbox gave the exact same result: only the plugin under `.agents/plugins/` and the plugin named by `.agents/plugins.json` loaded in `paths`, nothing written during `midturn` loaded in the same turn, and the written plugin loaded in `nextturn` after the two top files were removed. So 13.8 holds unchanged on `agy` 1.4.12 and `agy` 1.5.3.
  ```
  # paths exit=0 s=17 hooks-ran=[listed plugin ] mcp-paths=[listedx4 pluginx4 ] tool-dirs=[p-listed_m-listed p-plugin_m-plugin ] result="status":"SUCCESS"
  # midturn exit=0 s=6 hooks-ran=[] mcp-paths=[] tool-dirs=[] result="status":"SUCCESS"
  # nextturn exit=0 s=6 hooks-ran=[writtenplugin ] mcp-paths=[writtenpluginx4 ] tool-dirs=[p-writtenplugin_m-writtenplugin ] result="status":"SUCCESS"
  # 1.5.3 (2026-11-07):
  # paths exit=0 s=18 hooks-ran=[listed plugin ] mcp-paths=[listedx4 pluginx4 ] tool-dirs=[p-listed_m-listed p-plugin_m-plugin ] result="status":"SUCCESS"
  # midturn exit=0 s=7 hooks-ran=[] mcp-paths=[] tool-dirs=[] result="status":"SUCCESS"
  # nextturn exit=0 s=5 hooks-ran=[writtenplugin ] mcp-paths=[writtenpluginx4 ] tool-dirs=[p-writtenplugin_m-writtenplugin ] result="status":"SUCCESS"
  ```
  How: `crew admin sandbox start run --cli agy`, `crew-run creds`, and `spike/agy-load-check.sh paths midturn nextturn` as `agent`, with `spike/mcp-standin.py` copied to `/tmp`.

## Item 33: where `agy` reads skills

- **Answer:** in all three places tried. `agy -p /skills` listed a marker skill from `~/.gemini/antigravity-cli/skills/`, one from `~/.gemini/config/skills/`, and one from the workspace's `.agents/skills/`. So `crew-run` can put a project's skills in `~/.gemini/config/skills/` beside the harness, or leave them in the checkout.
- **How:** `spike/agy-checks.sh skills`, `testdata/agy/skills-list.txt`.
  ```
  # skills exit=0 markers: spike-cli-dir spike-config-dir spike-workspace-dir
  ```
- **What it changes:** nothing: 12.1 and 12.9 hold.

## Item 48: `--effort` when the name already holds one

- **Answer:** a different effort is refused before any request: `--model gemini-3.8-flash-low --effort high` ends at once with exit code 1 and "--model gemini-3.8-flash-low conflicts with --effort=high". The same effort is accepted and changes nothing that matters.
- **How:** `testdata/agy/effort-conflicts-with-name.jsonl`.
- **What it changes:** nothing: Crew does not pass `--effort` to `agy` (12.3).

## Item 79: one call with no tools (`agy`)

- **Answer:** `agy` under a settings file that allows nothing (`toolPermission: request-review`, an empty allow list) answered in 6.2, 7.4, and 4.6 seconds, plus about 3 seconds for the sandbox, so 8 to 10 seconds in all; `claude` takes about 7.5 (above).
- **How:** `spike/agy-checks.sh notools`, `testdata/agy/no-tools-call.jsonl`.
- **What it changes:** nothing: a rewrite of a prompt is ready well inside the 45 seconds of 7.2, and item 79's way out is not needed.

## Item 66: what `claude -p` loads from a workspace with `--setting-sources user --strict-mcp-config`

- **Answer: nothing of the project's own.** A workspace with `CLAUDE.md` (a marker word), `.claude/settings.json` (an `env` block and two hooks), `.mcp.json` (an HTTP server), `.claude/skills/`, `.claude/agents/`, and `.agents/skills/` gave a run in which no hook ran, the `env` variable was not set, the server got no request and is not in `init`'s `mcp_servers`, no skill or agent of the workspace is in `init`, and the model did not know the marker word of `CLAUDE.md`. So `--setting-sources user` keeps out `CLAUDE.md` and the project's skills and agents as well, not only its settings. What still loads is what is in the home: the harness's global context, the skills in `~/.claude/skills/`, and the file of `--append-system-prompt-file`. A skill in the home that declares a hook in its frontmatter does run that hook when the skill is used.
- **How:** one turn on `haiku` in a workspace built for it, `testdata/claude/workspace-files-not-loaded.jsonl`; a second turn that used a skill with a hook.
  ```
  settings-hook-ran=no  mcp-requests=0  mcp_servers: []  spike skills: ['spike-home-project-skill']  spike agents: []
  result: 'The environment variable `$SPIKE_PROJECT_ENV` is not set ... I can't clearly identify "the project marker word" ... `| | OTTER | GANNET`'
  skill in ~/.claude/skills with a PreToolUse hook, used:  # exit=0 skill-hook-ran=yes
  ```
- **What it changes:** proposed, waiting for the operator: 12.2 and 13.8 say the flag keeps out the hooks and the `env` block. It keeps out all of the project's own files. That is what Crew wants for a judging run, and it means the project's context and skills reach `claude` only the way 12.9 already says: `AGENTS.md` through `--append-system-prompt-file`, and skills written by `crew-run` into `~/.claude/skills/`. Since a skill there can declare a hook that runs, 13.8's rule that such a skill is code stays.

## Item 74: the harness in the home, in a headless `claude` run

- **Answer:** yes. In the stand-in, `sh-agent install` puts the harness in `~/.claude/` at boot, and a headless run with `--setting-sources user` had its global context: asked for the last word of the `AGENTS.md` context, the model answered `GANNET`, the canary of styrman-harness's context. A skill that `crew-run` writes into `~/.claude/skills/` is listed in `init` and can be used. The file of `--append-system-prompt-file` reaches the model (its marker word `OTTER` came back).
- **How:** the same turn as item 66.
- **What it changes:** nothing. The image's own build target for `claude` (12.1) is tested again in M1, when it replaces `sh-agent install`.

## Item 16: the harness built at image build against `sh-agent install`

- **Answer:** yes, the same, for both CLIs (checked at M1 on 2026-10-13). Over the unmodified styrman-harness `43faca0`, `crew-harnessbuild` and `sh-agent install` (v2.7.0, built from `43faca0`, in the stand-in image) wrote the same 1,790 files under `.claude/` and `.gemini/config/` (agy's skills included, in `.gemini/config/skills/`), with the same bytes and modes, and the same directories, except three differences Crew makes on purpose: the first line of the context (`.claude/AGENTS.md`, `.gemini/config/rules/AGENTS.md`) and of `.claude/CLAUDE.md` is Crew's marker in place of sh-agent's; the context's inventory line names Crew's `harness/` and the Crew MCP's `list_skills` in place of `sh-agent list`; and Crew's skeleton also holds `.claude.json`, the first-run mark of 12.2's quirks table, which `sh-agent install` does not write. `.claude/settings.json` (attribution off) is byte for byte the same. Only sh-agent's own files were left out of the comparison: `.config/sh-agent/state.json` and `.local/bin/sh-agent`.
- **How:** `go build ./cmd/crew-harnessbuild` in the pinned Go image, then `crew-harnessbuild -src /ref -out /out/crew-home` with the reference mounted read-only; and `sudo docker run --rm --name crew-item16-install --user 1000:1000 -v …/shagent-home:/home16 -e HOME=/home16 --entrypoint /styrman/bin/sh-agent crew-spike-runtime:5c9e6fb install` into an empty home. Both containers were `--rm`; none was left. A script walked both trees and compared each path's mode and bytes.
  ```
  crew-harnessbuild: 36 skills, 8 agents, 1 rules; 897 files for claude, 894 for agy; context 15956 bytes, agy rules block 17412 of 22000
  1790 written, 0 unchanged, 0 removed, 0 kept — 1790 files managed
  files: crew 1791, sh-agent 1790 (+2 ignored: .config/sh-agent/state.json, .local/bin/sh-agent)
  identical (bytes and mode): 1787
  only in crew: ['.claude.json']    differ in the marker line only: ['.claude/CLAUDE.md']    differ in mode: []
  differ otherwise: ['.claude/AGENTS.md', '.gemini/config/rules/AGENTS.md']  (line 1, the marker; line 81, the inventory's sentence)
  directories only in sh-agent: ['.config', '.config/sh-agent', '.local', '.local/bin']
  ```
- **What it changes:** nothing: 12.1 and 12.8 hold. The runtime image builds `harness/` with `crew-harnessbuild` in a Go build stage, and items 33 and 74 still describe what each CLI reads. Crew's own `harness/` is the adapted copy of 17.3 (19 skills, 7 subagents, one rule), so its skeleton is smaller than this reference by design.

## Item 6: how many run microVMs fit on dave

- **Answer:** today the limit is not memory but Docker's address pools. With `daemon.json` as styrman-crew's uninstall left it (no `default-address-pools`), Docker gives out 29 networks besides its own, and every sandbox takes one, so start 28 of the test failed beside the two sandboxes already running: "all predefined address pools have been fully subnetted". Idle, a small sandbox without Docker (2 vCPUs, 2 GB) takes about 190 MB of the machine's memory, and a large one with its Docker daemon running (4 vCPUs, 6 GB) about 800 MB; both start in 2 to 5 seconds. So idle, about 220 small or 52 large would fit above the floor of 16 GB, far more than the strict count of 14.1 allows. What a working run uses is item 26. KSM is switched on (`run=1`) but shares nothing: it has scanned no page (`pages_scanned=0`), because the guest memory of Cloud Hypervisor is not marked as mergeable.
- **How:** `spike/fill-machine.sh small 30` and `large 27`, which start sandboxes one at a time, read `MemAvailable` after each has settled, and remove them all; `raw/item-6-fill-{small,large}.tsv`; then empty networks made until Docker refused.
  ```
  small: 27 started, then start 28 failed   used 5129768 kB for 27, 189991 kB each, start 2 s
  large: 27 started                         used 21587632 kB for 27, 799541 kB each, start 4-5 s
  networks created before refusal: 27 (plus existing ...)   Error response from daemon: all predefined address pools have been fully subnetted
  ksm: run=1 full_scans=0 pages_scanned=0 pages_shared=0 pages_sharing=0
  ```
- **What it changes:** proposed, waiting for the operator: 13.2 step 4 already gives `daemon.json` an address pool large enough; the spike shows it is required, not tuning: without it, 29 sandboxes is a hard limit. 13.2 step 6 and 13.10 say identical pages are shared between microVMs; on this setup they are not, so the memory count of 13.10 must not count on it, and the side channel it mentions is not open.

## Item 72: how many `claude` runs at once the plan carries (and item 68, the parallel part)

- **Answer:** five at once ran without a single refusal. Five sandboxes, each running `claude -p` on `sonnet` at `--effort medium` through small coding tasks one after another for 14 minutes, made 144 calls, all `ok`, 27 seconds each (median). The five-hour window went from 1 to 7 percent, and the seven-day window from 49 to 50 percent; the building agent's own session ran on the same subscription and counts in those numbers. So five such runs use about 0.43 percent of the five-hour window per minute, and could run for about 3.9 hours before the five-hour limit, or about 6 runs for the whole five hours; a full five-hour window is about one sixth of the weekly limit. The plan is Max (`subscriptionType: max`). The limit itself was not reached on purpose, because the building agent runs on the same subscription; its message is recorded the first time a real limit is met. Every `rate_limit_event` said `status: allowed` and `overageStatus: rejected`.
- **How:** `spike/parallel.sh claude 5 15 sonnet medium 0.85` (it stops every run at 85 percent of the five-hour window), `spike/par-analyse.py spike/raw/par-claude`.
  ```
  sandboxes 5, calls 144, wall time 13.9 min
  outcomes: ok 144
  seconds per call: median 27, min 19, max 61
  rate_limit_event: 165; five_hour lowest 0.01 highest 0.07; seven_day lowest 0.49 highest 0.5; statuses ['allowed']; overage ['rejected']
  estimated list-price cost 14.22 USD, input+output tokens 533227
  ```
- **What it changes:** nothing: 2 lead slots for `claude` (7.9) are well inside what the plan carries. The quota guard's `slow_down_at` of 0.8 (14.1) can now be applied to `claude` too, from `rate_limit_event` (12.7). These were short tasks; a long builder run uses more per minute, and item 26's numbers come from the `agy` test.

## Item 26 (part): what a `claude` run uses

- **Answer, so far:** little. In the five large sandboxes (4 vCPUs, 6 GB each), a guest used 372 MB of memory (median, at most 633 MB) and 2 percent of its CPU (median, 10 percent at the 90th percentile, at most 42). An agent spends most of its time waiting for the model. The machine's free memory stayed above 51 GB. Tasks that build a lot, or Docker inside the sandbox, use more: the `agy` test runs longer tasks.
- **How:** the samples of `spike/parallel.sh`, every 30 seconds, `spike/par-analyse.py`.
  ```
  guest memory used MB: median 372, max 633 (of 6 GB each)
  guest CPU busy % (of 4 vCPUs): median 2, p90 10, max 42
  machine's free memory MB: min 51210, max 55135
  ```

## Item 14 (part): the size of a trajectory

- **Answer, so far:** for `claude`, 4.6 MB of stream-json per hour of run time (5.3 MB over 1.15 run-hours). The `agy` test gives its number.

## Item 60: what can bound a run's whole home and workspace

- **Answer:** inside the sandbox, a workspace on a sparse ext4 image file in the home, mounted by the guest, is bounded: writing stopped at 906 MiB in a 1 GB image. It is the same way styrman-crew keeps the Docker of a sandbox at 20 GB. But it bounds accidents, not an agent: the user `agent` could make the image file larger with `truncate` (to 3 GB), because the file lives in the shared home. From the machine's side, Docker's local volume driver cannot mount an image file (`o=loop` gives "invalid argument"). A hard bound needs project quotas on the machine's file system (ext4 on dave), which the host setup would switch on, or a home on a disk image that the server creates and the guest cannot reach.
- **How:** `truncate`, `mkfs.ext4`, `mount -o loop` and `dd` in a sandbox; `docker volume create --opt type=ext4 --opt o=loop`.
  ```
  /dev/loop1  974M  280K  906M  1% /mnt/ws
  949948416 bytes (950 MB, 906 MiB) copied          (stopped: no space left)
  can the guest grow the image? yes, to 3.0G
  docker: ... failed to mount local volume: ... data: loop: invalid argument
  ```
- **What it changes:** nothing in M0: 12.4 already bounds only the sandbox's own Docker in the first version and leaves the rest to the disk guard. A hard bound is for M1's host setup to decide (project quotas on the data folder).

## A sandbox that stops answering (found during item 18)

- **What happened:** after a tmux session, a wait loop of mine, and a `pkill` were run in the sandbox `ag` through `docker exec`, every later `docker exec` into it hung, and so did `docker rm -f`, for more than six minutes. Its microVM was idle (2.5 percent CPU) and Docker still said it was running. Ending its `cloud-hypervisor` process on the machine let Docker remove it at once. Which of the three things stopped its exec channel is not known.
- **What it changes:** proposed for M1 and M3: the orchestrator bounds every `docker exec` and `docker rm` with a time limit, and when a removal does not finish, it ends the sandbox's hypervisor process and removes it again. A run whose sandbox stops answering ends with the reason `sandbox` (12.4).

## Item 5: from the start of a run's sandbox to the first event

- **Answer:** about 10 seconds. A fresh small sandbox of the stand-in image, with the harness already in it, is ready 4.0 to 4.2 seconds after `docker run` (the start script fills the home from the skeleton; `sh-agent install` finishes after "ready", in the background). The CLI's first event then comes about 5.5 to 6.5 seconds later: 9.4 to 10.5 seconds after the start, for both CLIs. Crew's own image drops `sh-agent install`, so its start can only be shorter; M3 measures it again.
- **How:** `spike/first-event.sh claude 3` and `agy 3`: a new sandbox, the login, one headless turn with no tools, the first line of output stamped.
  ```
  claude 1: sandbox ready 4223 ms, first event 10313 ms, turn ended 12520 ms after the start
  claude 2: sandbox ready 4027 ms, first event 10085 ms, turn ended 13753 ms after the start
  claude 3: sandbox ready 4091 ms, first event 9674 ms, turn ended 12754 ms after the start
  agy 1: sandbox ready 4147 ms, first event 9457 ms, turn ended 29623 ms after the start
  agy 2: sandbox ready 4079 ms, first event 9417 ms, turn ended 33437 ms after the start
  agy 3: sandbox ready 4023 ms, first event 10481 ms, turn ended 19124 ms after the start
  ```
- **What it changes:** nothing: 13.10's "about 3 seconds" is the sandbox alone; a run's first event comes about 10 seconds after its start.

## Item 18: a terminal inside a running run's microVM

- **Answer:** it does not disturb the CLI, and tmux keeps the session. While `claude -p` ran a 40-second command in a sandbox, a tmux session opened in the same microVM ran commands and showed the running `claude`; the run ended normally with its answer. The session was still there after the run, and kept what was typed and printed (12 lines of scrollback). An earlier try in the sandbox `ag` left that sandbox unreachable (above, "A sandbox that stops answering"); it was repeated cleanly in `cl`, where the sandbox still answered `docker exec` afterwards, so the earlier hang came from the wait loop or the `pkill` around it, not from the terminal.
- **How:** `spike/terminal-check.sh claude` in the sandbox `cl`.
  ```
  ❯ echo terminal-says-hi; ps -eo pid,etime,comm | grep -E "agy|claude" | head -3; uptime
  terminal-says-hi
     1433       00:12 claude
  --- the run: exit 0 "status":"completed" "result":"finished"
  --- scrollback lines kept after the run: 12
  cl still answers exec
  ```
- **What it changes:** nothing: 13.11 holds. The image's start script greets a terminal with styrman-crew's setup hint ("Run 'styrman-setup' to sign in ..."), which Crew's image drops.

## Item 39: one open process over several turns

- **Answer:** it works for both CLIs on the subscription, and it is clearly faster. With `--input-format stream-json` and standard input kept open, a second message gets its answer in 1.0 second on `claude` (`haiku`) and 2.4 seconds on `agy` (`gemini-3.8-flash-low`), against 4 to 13 seconds for a new call that continues the conversation, and both remember the first turn. In one process `claude` reports `num_turns: 1` for each turn and a `total_cost_usd` that adds up; `agy` adds up `num_turns` and `usage` as it does across calls.
- **How:** `spike/multiturn.py claude` in `cl`, `spike/multiturn.py agy` in a fresh sandbox.
  ```
  claude  turn 1: 3.3 s, answer 'ok'   cost 0.022983     turn 2: 1.0 s, answer 'lantern', cost 0.0249174
  agy     turn 1: 14.5 s, answer 'ok'  num_turns 1        turn 2: 2.4 s, answer 'lantern', num_turns 2
  ```
- **What it changes:** nothing now: 12.10 keeps one call per turn in the first version. The measurement says the later step is worth about 3 to 10 seconds per turn, most for the goal loop and for a conversation with the operator.

## Item 62: which secrets and ids are in what `agy` and `claude` print

- **Answer:** none of the operator's secrets or ids. Over the 454 raw files of the spike (every stream, standard error, and command output of both CLIs, the parallel tests included), there is no listed value (the mail address, Claude's account and organisation ids, the Google account id), not the operator's name, and no string of the shape of a token. What the CLIs print that is an id is their own: `session_id` and `conversation_id`, the `uuid` of each message, task ids, and paths in the home (`memory_paths`, `log_uri`). The `initialize` answer's `account` holds only `apiProvider` and `tokenSource`; `rate_limit_event` holds no account. The one place a secret could appear is a URL with a code during an interactive sign-in, which a headless run with a login does not print.
- **The mail shape misfires on code:** `@pytest.mark.parametrize` in a test file has the shape of a mail address (311 times in the parallel tests), so the scrub replaces it in a recording of a run that writes Python tests. Harmless for a secret, but it changes the recording: a test file that needs such a run is scrubbed with care, or the scrub learns that exception.
- **How:** a scan with the scrub script's own patterns, the listed values, and the operator's name from the `id_token`, printing only counts per kind and the domains of mail-shaped strings.
  ```
  files scanned: 454
  listed value 0   operator's name 0   every token shape 0   secret-field 0
  mail address 314: pytest.mark.parametrize 311, example.com 3, edgeless.systems 2 (Kata's advisory), tty1.service 1, anthropic.com 1 (noreply)
  ```
- **What it changes:** nothing: 16.12 holds. The scrub script stays as written: values from a file the operator fills, and token shapes.

## Items 1 and 35: ten `agy` runs at once on one subscription, and what the plan carries

- **Answer: no.** Ten sandboxes, each running `agy -p` on `gemini-3.8-flash-medium` through coding tasks one after another (about 3 minutes a task), reached the five-hour limit of the Gemini group after **33 minutes**: 89 calls succeeded, then all ten runs failed at the limit within 80 seconds of each other (16:05:18 to 16:06:38). Those 33 minutes also used 17 points of the weekly limit (94 to 77 percent). So this plan carries about 5.5 run-hours of Gemini Flash per five-hour window, which is about **one run working without a break**, and about 30 run-hours a week. The Claude and GPT group of `agy` is separate and was barely touched (95 percent of its five-hour window left). For comparison, `claude` on its Max plan carried five Sonnet runs at once at about 0.43 percent of its five-hour window a minute (item 72).
- **How:** `spike/parallel.sh agy 10 120 gemini-3.8-flash-medium -`, `spike/par-analyse.py spike/raw/par-agy`, `agy -p /usage` after.
  ```
  sandboxes 10, calls 100, wall time 34.3 min
  outcomes: SUCCESS 89, ERROR Individual quota reached ... 10, ERROR authentication failed or timed out 1
  seconds per call: median 190, min 3, max 456
  Gemini Models  Weekly Limit Remaining     77%   2026-10-19T08:25:47Z
  Gemini Models  Five Hour Limit Remaining  0%    2026-10-13T16:31:48Z
  Claude and GPT models  Five Hour Limit Remaining  95%
  ```
- **What it changes:** the way out that section 24 names for items 1 and 35 is needed: "the number of runs at once becomes what the spike found, and the lead slots follow it". `lead_slots` of `agy` becomes 1 (14.1, 7.9), and the quota guard, which reads `/usage` (12.7), holds back other runs on the Gemini group as it nears its limit. Most of Crew's work will run on `claude` with these plans. Changed in 0.33.

## Items 50 and 54: what `agy` prints at its limit, and that it spends nothing

- **Answer:** a run that reaches the limit ends in the middle of its turn with **exit code 3**. Standard error has the message and a JSON line, and the result has `status: ERROR`, the same message, and the usage of the work that was done before:
  ```
  error: Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 2h26m13s.
  AGY_ERROR: {"short_error":"RESOURCE_EXHAUSTED (code 429): Individual quota reached. ... Resets in 2h26m13s.","status":"RESOURCE_EXHAUSTED","error_code":429,"code_kind":"http","retryable":true,"error_id":"..."}
  {"event":"result","result":{"conversation_id":"...","status":"ERROR","error":"Individual quota reached. ... Resets in 2h26m13s.","duration_seconds":71.4,"num_turns":1,"usage":{"input_tokens":52325,...}}}
  ```
  The run stopped and spent nothing: paid credits were off, and `/credits` says `Remaining credits 0` before and after. `/usage` then shows the group at 0 percent with its reset time, and the other group still free. One run had done 35 steps of its task when the limit came, so work in progress is real at a limit.
- **How:** `testdata/agy/limit-reached-result.jsonl`, `testdata/agy/limit-reached-stderr.txt`, `testdata/agy/usage-after-limit.txt`.
- **What it changes:** 12.2's quirk is confirmed and made exact: the adapter takes the limit from `AGY_ERROR`'s `status: RESOURCE_EXHAUSTED` with exit code 3, and the reset from `/usage`, not from the text. The stop reason is `quota` (12.4), and the work in progress is kept (11.2).

## Items 2 and 17: ten copies of one `agy` login at once

- **Answer:** the copies hold, with one failure at the start. All ten sandboxes started within seconds with a copy whose access token had expired, so all ten renewed it at the same moment with one refresh token. Nine succeeded; one ended its first call after 16 seconds with "authentication required. Run 'agy' to log in, then retry." and exit code 1. Its file was renewed seconds later, and its next eight calls worked. After the test all ten files hold the original refresh token, and the operator's own `agy` on dave still works. No one had to sign in again. Because the limit came after 34 minutes, no copy reached its second renewal, so how they behave over hours is not tested.
- **How:** the fields of the ten token files after the test, compared in a script that prints only `True` or `False`; `raw/notes-item-2.txt`; `testdata/agy/concurrent-renewal-failed-stderr.txt`.
  ```
  par-agy-1 expiry 2026-10-13T14:32:18 refresh same: True     ... par-agy-10 expiry 2026-10-13T14:32:37 refresh same: True
  par-agy-10 turn 001: ERROR after 16 s, "Error: authentication required. Run 'agy' to log in, then retry." exit 1
  ```
- **What it changes:** in 0.33: the broker gives each sandbox a login whose access token is fresh, renewed by the login check, so sandboxes do not all renew at once; and a run that fails with `login` before any work is retried once before the CLI counts as signed out (12.6). Keeping a renewed file from a run (12.6) is not needed for `agy`, since the refresh token does not change; the daily check alone keeps the sign-in in use. Item 78 (weeks) stays open.

## Items 14 and 26: what an `agy` run writes and uses

- **Answer:** item 14: `agy` writes little: 0.9 MB of stream-json per hour of run time (5.0 MB over 5.55 run-hours), against 4.6 MB for `claude`, because it prints a tool step only when it starts and ends. Item 26: a large sandbox working with `agy` used 649 MB of memory (median, at most 986 MB of its 6 GB) and almost no CPU (median 0 percent, 3 percent at the 90th percentile, at most 35); the machine's free memory stayed above 38 GB with ten of them running. Builds in these tasks were small (Go and Python tests); Docker inside the sandbox was running but idle.
- **How:** `spike/par-analyse.py spike/raw/par-agy`.
  ```
  trajectory: 5.0 MB over 5.55 hours of run time = 0.9 MB per run-hour
  guest memory used MB: median 649, max 986 (of 6 GB each)
  guest CPU busy % (of 4 vCPUs): median 0, p90 3, max 35
  machine's free memory MB: min 38771, max 52910
  ```
- **What it changes:** parked question 101 is answered with my recommendation: the sizes and `cpu_overcommit: 2.0` stay, so about 10 sandboxes fit on dave. A working run uses a tenth of its memory and little CPU, but the subscriptions, not the machine, bound the work: `agy` carries about one run, `claude` about six.

# The items of M1

## Item 13: Cloud Hypervisor under Docker on Kata 4 (the M1 half)

- **Answer:** yes, with runtime-rs, set up the way kata-deploy sets it up. Kata 4.2.0 (the newest release on 2026-10-13) no longer ships the Go runtime that dave's 3.31 uses: its one shim is runtime-rs, `/opt/kata/runtime-rs/bin/containerd-shim-kata-v2`, and its Cloud Hypervisor configuration is `runtime-rs/configuration-clh-runtime-rs.toml`. runtime-rs refuses a configuration named by `KATA_CONF_FILE` ("only shipped Kata configuration files are accepted"), which is how 13.2's shim wrapper names it for 3.31. Given as the runtime's `ConfigPath` option in `daemon.json`, with the shim under its own name, a microVM starts with its own kernel and shares a folder.
- **How:** `make install-test DISTRO=debian PHASE=kata4`, on a fresh Debian 13 virtual machine with nested virtualization: Docker 29.7.2, the static release checked against GitHub's digest, then a container with `--runtime` set to that runtime.
  ```
  asset kata-static-4.2.0-amd64.tar.zst sha256 b828904fa3f1e49ddd7dc799c72cb1503cd1e772d354c3987c8d4189b2a623a8   PASS checksum
  kclh1 (KATA_CONF_FILE wrapper): invalid KATA_CONF_FILE ".../configuration-clh-runtime-rs.toml": only shipped Kata configuration files are accepted
  runtime kclh1: /opt/kata/runtime-rs/bin/containerd-shim-kata-v2 with ConfigPath .../runtime-rs/configuration-clh-runtime-rs.toml
  kclh1: 6.18.35   PASS kclh1 starts a microVM   PASS kclh1 shares a folder
  ```
- **What it changes:** nothing now. Crew stays on 3.31 until the move to Kata 4 (M17, 23), which then changes 13.2's shim wrapper to `ConfigPath`; the two annotation advisories of the M0 half are fixed in 4.0.0. The move needs the integration tests and its own measurements (start time, memory, the Docker daemon inside a sandbox), which this check does not make.

## Item 29: a sandbox and its IPv6, and the IPv4 rules, tested from inside

- **Answer:** yes, the rules hold. From inside a microVM on a sandbox bridge (`sdb-`), on dave with Crew's firewall: the internet answers; none of dave's addresses answers on 22, 80, 443, or 8080 (its LAN address, every Docker bridge's gateway, the sandbox bridge's own); the database does not answer; the metadata address does not; the LAN gateway does not, on 22, 53, 80, or 443. IPv6: the guest kernel gives itself a link-local address (fe80::/64, scope link) and its route, and nothing else: no global address and no default route. dave boots with `ipv6.disable=1`, and the firewall drops every IPv6 packet from a sandbox bridge besides. With the firewall's two jumps taken out, the same proof fails 24 times (it reaches the machine), so it checks what it claims. Inside a microVM Docker's resolver (127.0.0.11) is the guest's own loopback, so a sandbox needs public resolvers, which the runtime image's entrypoint writes (13.6).
- **How:** `deploy/proof.sh`, which `crew-ctl install` runs as its proof (16.11), on dave on 2026-10-13; then the jumps removed by hand, the proof run again, and the rules put back with `crew-firewall.sh`.
  ```
  PASS no IPv6 address but link-local     PASS no IPv6 route but link-local
  PASS resolves github.com                PASS reaches the internet (github.com:443)
  PASS does not reach the machine at 192.168.5.10:22 ... (every address and port)
  PASS does not reach the database at 172.16.0.2:5432    PASS does not reach the metadata address
  PASS does not reach the local network at 192.168.5.1:53
  without the jumps: proof: 24 failure(s)   e.g. FAIL reaches the machine at 192.168.5.10:443
  ```
- **What it changes:** the runtime image's entrypoint also switches IPv6 off in the guest, so a run's sandbox keeps not even its link-local address (13.6 says a sandbox gets no IPv6 address).

## Item 59: a detached command across a restart of the server and of Docker

- **Answer:** yes, both. A command started with `docker exec -d` in a microVM wrote a line every 5 seconds through a restart of `crew-server` and a restart of Docker with live restore: all 30 lines arrived, the largest gap was 6 seconds, the microVM kept its start time, and its end was read after both restarts. Crew's own services were back and healthy after Docker's restart.
- **How:** `spike/item-59.sh` on dave, with Crew installed, on 2026-10-13.
  ```
  16:42:27 restarting crew-server        16:42:38 after the server's restart: tick 5
  16:42:38 restarting Docker (live-restore: true)
  16:42:55 after Docker's restart: the microVM is running started 2026-10-13T16:42:15Z
  16:44:46 the end: ended 0     ticks written: 30 of 30     largest gap between ticks: 6 s
  ```
- **What it changes:** nothing: the way out in section 24 (end the run with `restart`) is not needed for these two restarts. A restart of the machine still ends every run (21).

## Item 34: the host setup on a current Ubuntu LTS

- **Answer:** yes. On a fresh Ubuntu 26.04.1 LTS, as on a fresh Debian 13, the host setup installs Docker 29.7.2 with Docker's own script and Kata 3.31.0 from its static release, merges Docker's settings, and a microVM starts with Kata's own kernel. The whole install cycle of 16.11 passes there, 38 of 38: doctor, install from one file of answers, status, edit, backup, settings export and import, migrate, restore, a newer checkout with a schema change installed over the running one, uninstall at each level with an install again after levels 1 and 2 on the kept data, nothing left after level 3 with the runtime, and an install again on the fresh machine.
- **How:** `make install-test DISTRO=ubuntu PHASE=cycle` and `DISTRO=debian`, on 2026-10-13: a virtual machine with 8 virtual CPUs and 32 GiB, from the dated cloud images in `versions.lock`, in a container that holds QEMU and is given `/dev/kvm`, on a sandbox bridge.
  ```
  up: Ubuntu 26.04.1 LTS        install-test ubuntu cycle: PASS=38 FAIL=0
  up: Debian GNU/Linux 13       install-test debian cycle: PASS=38 FAIL=0
  ```
- **What it changes:** nothing. The first runs found five faults of Crew's own, fixed before these: the UI's prerender listened where it did not fetch on a kernel with IPv6, a test machine's ingress would not start without a sign-in client, doctor failed where versions.lock was gone, edit did not wait for the server, and the test disk was too small for doctor after a build.

## Item 64: what `install --build` costs on a machine with an empty Docker

- **Answer:** about 6.5 to 7 minutes, and about 19 GB. With 8 virtual CPUs and the network of a home line, `install --build` took 388 s on Debian 13 and 418 s on Ubuntu 26.04, Docker's and Kata's installs and the proof included. It leaves 7.2 GB of images and 12 GB of build cache, of which 10 GB can be reclaimed, as a fresh Docker 29 counts them: it uses the containerd image store, which also counts the compressed layers. The same runtime image is 4.36 GB in the count of dave's Docker (overlay2), 6.21 GB in the fresh one's; the server is 109 and 160 MB, the ingress 131 and 182 MB.
- **How:** the install test above; `time`, `docker system df`, and `docker images` after the first install.
  ```
  item 64: install --build took 388 s in all          (Ubuntu: 418 s)
  Images 5  7.2GB    Build Cache 85  11.95GB (9.974GB reclaimable)
  crew-runtime 6.21GB   crew-caddy 182MB   crew-server 160MB
  ```
- **What it changes:** 13.4 says how the image's size is counted, and what `install --build` costs. Doctor's 200 GB of free disk covers it.

## Item 32: `gh` in the setup microVM, with git over SSH, and pushing to GitHub's registry

- **Answer:** yes for the registry, with the scope `write:packages`; the first half in part. `gh auth login --git-protocol ssh --web --insecure-storage` in the setup microVM signs in with a code in the browser, and Crew collects `hosts.yml` beside the SSH key of the step before. A login with the default scopes gets a registry token with `push: true`, but the registry refuses the upload: `403 DENIED: requested access to the resource is denied ... does not match expected scopes`. With `write:packages` it accepts the upload. That the server uses the two together, `gh` for GitHub's functions and the key for git, is shown when the server first pushes, in M5, where a project gets its repository on GitHub (section 26, 0.37).
- **How:** Setup's `gh` step on dave (2026-10-14, account `kesokaj`), then `crew admin probe ghcr kesokaj/crew-probe`, which starts an upload and leaves it unfinished. GitHub does not let an upload be cancelled; an unfinished one lapses, and no package was made.
  ```
  a registry token for kesokaj/crew-probe with push: true (HTTP 200)
  start an upload: HTTP 202
  cancel the upload: HTTP 405
  gh api /user/packages/container/crew-probe  ->  Package not found. (HTTP 404)
  ```
- **What it changes:** Setup's `gh` step asks for `write:packages` (12.6, 16.12; section 26, 0.35).

## Item 42: the App's one-hour token, read-only, for `git` over HTTPS and for `gh`, revoked at the end

- **Answer:** yes, all of it. In a run sandbox on dave the token from the App (contents: read, one repository named) cloned the private fixture over HTTPS through the git helper, and `gh` read with it. A push was refused, another private repository was not found, and the token named only the fixture. When the sandbox was removed, Crew revoked it: the same token was answered 200 before and 401 after. GitHub gives an installation token one hour.
- **How:** `crew admin sandbox start run --cli claude --repo kesokaj/crew-fixture --gcloud`, then as `agent` in it:
  ```
  git clone https://github.com/kesokaj/crew-fixture  ->  6978678 The fixture project of Crew's tests
  git push origin HEAD:refs/heads/crew-probe        ->  remote: Write access to repository not granted. (403)
  git clone https://github.com/kesokaj/crew          ->  fatal: repository 'https://github.com/kesokaj/crew/' not found
  gh repo view kesokaj/crew-fixture                  ->  crew-fixture PRIVATE
  gh api /installation/repositories                  ->  ["kesokaj/crew-fixture"]
  the sandbox's token: before rm HTTP 200, after rm HTTP 401
  ```
  The token was read into a shell variable and never printed.
- **What it changes:** nothing. 12.6 holds as written.

## Item 76: collecting the token of `claude setup-token` without showing it

- **Answer:** yes, with the spike's wrapper `crew-capture-token`, now in the image. In Setup on dave the terminal showed the sign-in link and then only `*` in place of the token, with `[the token was captured; the rest of the output is hidden]`. The wrapper wrote the token to a file the server collects (`capture: a token of 108 characters was written to /home/agent/.crew/setup/claude-login (mode 0600); the command ended with 0`). The stored token works: the login check's one call on `haiku` answered.
- **How:** Setup's `claude` step, run by the operator on 2026-10-14; `crew admin login check claude --real` -> `{"cli":"claude","state":"ok","detail":"one call on haiku answered","real":true}`.
- **What it changes:** nothing. The part of item 67 that runs ten sandboxes at once with the token is still open: it needs the operator's yes.

# The items of M3

## Item 46: the database's memory while 16 sandboxes write events at once

- **Answer:** little: at most 309 MiB of the 2 GiB that `crew-db` may use (median 243 MiB), with PostgreSQL's default `shared_buffers` of 128 MB, while 16 runs wrote as fast as they could. The 16 runs were the real run driver on `FakeHarness`, replaying every recorded line of `agy` and `claude` without a pause, each line one transaction as a real run stores it, on a throwaway PostgreSQL with `crew-db`'s image and limits. They stored 4,062 events a second, 254 for each run. A real run prints far less: `claude` about 4.6 MB of output an hour (item 14), about 1.5 lines a second at most, so 16 runs ask for about 25 events a second, a 160th of what was stored.
- **How:** `spike/dbload.sh load 16 3m`, which samples `docker stats` of the database every 2 seconds; output in `raw/dbload/`.
  ```
  stored 733562 events in 3m1s: 4062 a second, 253.9 a second per writer
  database 921 MB, events 910 MB with their indexes, 741429 events, 621 MB of original lines; 1287 bytes on disk per event, 1.47 bytes on disk per byte of line
  memory: 309.1MiB at most of 2GiB
  shared_buffers 128MB
  ```
- **What it changes:** nothing: `crew-db`'s 2 GB limit (deploy/compose.yaml) holds with room, and so does storing every event in its own transaction (12.4).

## Items 36 and 40: what a conversation loses when it is rebuilt from the turns

- **Answer:** everything that was only in the tool calls; nothing that was said in a turn. One `agy` conversation on the fixture wrote `NOTES.md`, committed and pushed it, and was stopped while it wrote `count.txt`. Continued in a new microVM with its own file put back (the second way of 12.10), it answered all three questions whose answers were only in its tool calls: the commit message, its first command, and the numbers it had written before the stop. Rebuilt onto `claude` (the third way, which a move to the other CLI takes), it answered none of three such questions, and said "unknown" to each rather than guess. What the turns' text holds came through both ways. Until Crew writes summaries (M6), a rebuilt conversation gets the last six turns in full, which hold the agent's answers but only the names of its tools.
- **How:** `crew admin run continue 1 --way files --text "<questions 1 to 4>"`, then `crew admin run continue 1 --cli claude --model haiku --text "<the same>"` and `--text "<questions 5 to 7>"`; the truth from the first run's stored events.
  ```
  way 2 (agy, its own file):   (1) Visited by agy. (2) Add NOTES.md (3) git status (4) 1 and 2          all right
  way 3 (rebuilt onto claude): (5) unknown (6) unknown (7) unknown
  the truth: (5) On branch subtask/1  (6) 68da673  (7) /home/agent/work/NOTES.md
  ```
  The rebuilt run's answer to questions 1 to 4 is not counted: the six turns it was given held the answers the second way had just given.
- **What it changes:** nothing in M3. 12.10 says a rebuilt turn is marked so its lower fidelity can be seen, and it is. Summaries (M6) should name the commands, the files, and the commits of the older turns, because that is what a rebuild loses.

## Item 74: the harness in the home, in a headless `claude` run, with Crew's own image

- **Answer:** yes. In a real run on dave (`claude` 2.1.287, Crew's runtime image `0.0.0-dev.84cb8ac1c6ec`), `init` listed 38 skills, among them the harness's (`golang`), and the harness's seven agents (bugs, changelog, documentation, explorer, reviewer, security, verifier) beside the CLI's own, with no MCP server, in `/home/agent/work`, with `bypassPermissions`. The other half of the item, a project's own skill and the added system prompt file, comes with the project's own skills and context (M12): no run of M3 has either.
- **How:** the raw `init` line of run `2f24687d`, read from the database.
  ```
  slash commands 72 with golang: True
  agents 12 ['bugs', 'changelog', 'claude', 'documentation', 'Explore', 'explorer', 'general-purpose', 'Plan', 'reviewer', 'security', 'statusline-setup', 'verifier']
  skills 38 | mcp servers [] | permission mode bypassPermissions | cwd /home/agent/work
  ```
- **What it changes:** nothing.

## Item 43: what one run writes to the database, and how fast it grows

- **Answer:** a short run writes about 30 events: `claude` 31 a run with lines of 886 bytes on average, `agy` 36 a run with lines of 347 bytes, over the 19 runs of 2026-10-15 on dave. On disk an event takes 1.47 bytes for each byte of its line, with its indexes and its payload (measured over 741,429 events in item 46's test; dave's own small sample, 612 events in 856 kB, carries the fixed cost of a partition). With the output per run-hour of items 14 and 26 (`claude` 4.6 MB, `agy` 0.9 MB) and what the subscriptions carry (items 1, 35, and 72: `claude` about six runs through a five-hour window, about six windows a week; `agy` about 30 run-hours a week), the trajectories grow at most about 1.2 GB a week, 64 GB a year, and at a quarter to a half of that use 16 to 32 GB a year. dave has 806 GB free.
- **How:** on dave, `select r.cli, count(distinct r.id), count(*), avg(length(e.raw)) from events e join runs r on r.id = e.run group by r.cli`; the projection from the numbers above.
  ```
  agy    5 runs   178 events  35.6 a run  347 bytes a line
  claude 14 runs  433 events  30.9 a run  886 bytes a line
  ceiling: claude 180 run-h/week, agy 30 run-h/week, spool 855 MB/week, on disk 1.23 GB/week, 64 GB/year
  ```
- **What it changes:** nothing: 8.3 keeps trajectories for good, and the disk guard (16.10) and the Database screen's months of room (21) are what watch it.

## Item 16: the harness built for `agy`, with Crew's own image

- **Answer:** yes. In a run's microVM of Crew's runtime image on dave, `agy -p /skills` listed every skill of `harness/` (api-auth-gcp, bash, cloud-run-deploy, codebase-design, data-gcp, docker, find-skills, frontend, gcloud-setup, gke-basics, golang, google-catalog, json-api-style, mermaid-syntax, operate-gcp, review-standards, terraform, testing-strategy, typescript) beside its own two. Found on the way: a microVM started without Docker inside (`--minimal`) got no harness at all, because the image's start script copied the skeleton only into a full sandbox; a run now asks for it with `CREW_HARNESS` (design 0.36).
- **How:** `sudo docker exec -u agent -e HOME=/home/agent crew-run-51b5e0ec73 bash -c 'cd ~/work && agy -p /skills'`, in the full sandbox of a run; the same in a minimal sandbox listed only `agy-customizations` and `antigravity-guide`.
- **What it changes:** the start script, and 0.36's change history.

## Item 44: a dump and a restore of the database at that size

- **Answer:** both grow in line with the size: about 7 seconds per GB of data for Crew's own dump (`backup.Dump`, plain SQL through gzip) and about 14 seconds per GB for its restore (`backup.Restore`, `psql` in one transaction), and the dump is about a fifth of the data. At 2 GB: 14 s, 417 MB, restore 30 s. At 8 GB: 54 s, 1,646 MB, restore 1 min 52 s. So at a year of trajectories at half the subscriptions' ceiling (item 43, 32 GB) the dump of an update's step 3 takes about 3.5 minutes and a restore about 7.5, and each daily backup is about 6.5 GB; at the ceiling, 64 GB, about 7 and 15 minutes and 13 GB.
- **How:** `spike/dbload.sh fill 2048` and `fill 8192`: a throwaway PostgreSQL with `crew-db`'s image and limits, events made from the recordings through COPY, then the dump and the restore into a second database made as an installation's is; output in `raw/dbload/`.
  ```
  database 2089 MB, events 2080 MB with their indexes, 1480000 events    dump: 14s, 417 MB compressed    restore: 30s
  database 8218 MB, events 8207 MB with their indexes, 5840000 events    dump: 54s, 1646 MB compressed   restore: 1m52s
  ```
- **What it changes:** nothing now. For updates and backups (M14), the time grows with the trajectories that are kept for good: a dump of the months that ended, which change only when a project or a conversation is deleted (8.3), could be made once and kept, and the daily backup made of the rest. A question for M14, not decided here.

## Item 5 again, and item 67: Crew's own image, and ten runs at once on one `claude` token

- **Answer:** from the start of a run to its first stored event, in microVMs of Crew's runtime image without Docker inside: `claude` 5.7 to 6.6 seconds (median 6.2), `agy` 10.0 seconds, with the harness in the home. That is under the stand-in's 9.4 to 10.5 seconds. Ten `claude` runs and one `agy` run ran at once through Crew's own driver, two turns each, on the fixture: all eleven ended well in 125 seconds, each ran `go test` and `go vet`, committed, and pushed its branch through the git endpoint; both CLIs were signed in after (`claude` by its real check, `agy` by `agy models`). Ten runs at once on one token of `claude setup-token`, as `CLAUDE_CODE_OAUTH_TOKEN`, work: the open part of item 67.
- **How:** `spike/m3-parallel.sh 10 1 haiku 0.85`, which stops every run if `claude`'s five-hour window passes 85%; output in `raw/m3-parallel-*`.
  ```
  10:29:00 11 runs started    10:30:36 first turns ended    10:31:00 second turns ended, after 125 s
  agy|idle|1     claude|idle|10
  seconds from the start of a run to its first event: agy 10.0 10.0 10.0   claude 5.7 6.2 6.6
  {"five_hour": {"utilization": 0}, "seven_day": {"utilization": 0.03}}     cost, estimated: claude 0.3759
  {"cli":"claude","state":"ok","detail":"one call on haiku answered","real":true}
  ```
- **What it changes:** nothing: 13.10's numbers hold, and the way out of item 67 is not needed.

## Item 24: three subtasks of one ticket, each on its own branch, joined on a real repository

- **Answer:** yes, and in the one live run no join needed a builder. On `wordcount`, a Go repository on GitHub, the ticket WC-12 ("Write counts as a table, as JSON, and as CSV") was analysed by the lead with `agy`, which planned three parts (WC-12.2 to WC-12.4) through `create_subtask`, each naming its files and its checks. All three ran at once, each on its own `sub/WC-12.n` branch. The backend joined each into the ticket's branch as it ended (WC-12.5 to WC-12.7, three joins, each with its check run); 0 of 3 needed a builder. Review, test, merge, and summary followed, 21.3 minutes from start to done. One run is not a rate: how often a join needs a builder in general is measured by the numbers Crew keeps from now on, not here.
- **How:** the live check L1, `tools/e2e/browser/live/parts.spec.ts`, approved by the operator, on dave; screenshots of the task view and the board while the parts ran.
  ```
  WC-12.2 part builder done lead sub/WC-12.2 joined
  WC-12.3 part builder done lead sub/WC-12.3 joined
  WC-12.4 part builder done lead sub/WC-12.4 joined
  WC-12.5 join crew done backend
  WC-12.6 join crew done backend
  WC-12.7 join crew done backend
  parts 3 at most running at once 3
  ✓ a ticket in three parts is built in parallel and joined (spike item 24) (21.3m)
  ```
- **What it changes:** one fix found on the way: a join's merge commit took the time of the join, so the same join gave another commit each pass and its check never settled (one extra check ran in this run, before the fix was installed). A join's commit now takes the later parent's date (0.42).

## Item 25: an agent on the laptop reaches the Crew MCP through the ingress with a personal token

- **Answer:** yes, for everything up to the laptop itself. A token made on the Crew MCP screen was used at `https://<the machine's address>/mcp` through the real ingress, with no cookie: `tools/list` and a call answered; after Revoke the same token was refused. Without a token `/mcp` answers 401, while `/api` still sends a stranger to Google's sign-in (302), so the ingress opens only `/mcp` to a bearer token. The last step, an agent CLI on the operator's own laptop configured with the address and the token, is the operator's to run: it uses the laptop and its CLI, not dave.
- **How:** `tools/e2e/browser/live/mcp.spec.ts` in the Playwright container through the ingress, approved by the operator; `curl` from dave's shell for the 401 and the 302.
- **What it changes:** nothing in the design; the laptop step goes to the handoff's next steps.

## Item 4: Model only mode with `agy`, through a proxy that lets through only a short list (M10, 2026-10-16)

- **Answer:** yes. A run of `agy` in a Model only sandbox (bridge `mdl-…`, no resolver of its own, the server's door as `HTTPS_PROXY`) reached its model and did its work, and the agent's own `curl https://example.com` was refused by the proxy (`CONNECT tunnel failed, response 403`). Which hosts a run cannot do without, measured by taking them off `network.model_only_hosts` one group at a time, one turn of `agy` on `gemini-3.8-flash` at low effort each:
  ```
  all seven hosts                                   worked; the proxy refused only example.com
  without flags, logs, and the picture              failed: "Eligibility check failed: failed to get profile picture: lh3.googleusercontent.com … Forbidden"
  without flags and logs (the picture kept)         worked
  without aicode and www.googleapis.com as well     failed: "Eligibility check failed: Get https://www.googleapis.com/oauth2/v2/userinfo: Forbidden"
  ```
  `antigravity-unleash.goog` (feature flags) and `play.googleapis.com` (logs) are not needed. `lh3.googleusercontent.com` (the account's picture) and `www.googleapis.com` (the account's userinfo) are, for the check `agy` makes before a turn. The login check, also Model only, needed `oauth2.googleapis.com` to renew the login: it is on the list since M10. `aicode.googleapis.com` was not told apart from `www.googleapis.com` in these runs and stays on the list. A run also asked for hosts the spike had not seen, which were refused and changed nothing: `accounts.google.com`, `update.googleapis.com`, `redirector.gvt1.com`, `android.clients.google.com`, and `optimizationguide-pa.googleapis.com`.
- **How:** `crew admin run start` on the fixture project set to Model only (`crew admin api PUT /api/projects/fixture/network`), with the machine's list changed through `PUT /api/settings/machine` between runs, and the proxy's log of every host it refused, by sandbox. Five model calls in all: the four planned, and the automatic retry of the fourth, which answered before the operator's stop took. The list and the fixture's mode were put back after.
- **What it changes:** `network.model_only_hosts` starts from five hosts, without the flags and the logs (design 0.43, 13.6 and 14.1).
