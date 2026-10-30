# Claude Code stream-json mode

Spike for [ENG-2677](https://linear.app/quantic/issue/ENG-2677). The question:
can pitwall drive Claude from Go through
`claude -p --input-format stream-json --output-format stream-json --verbose`,
or does the Claude driver need another shape? Short answer: stream-json
covers approvals, interrupt, resume, fork, rewind, hooks, MCP, plan mode,
model and effort switching, images, and usage and limit events. Drive it
directly from Go.

Everything below was captured on 2026-10-30 against Claude Code 2.1.314 on
Linux, in a scratch git repo under `/tmp`, with throwaway sessions only. The
probe ran `--model claude-sonnet-5-5 --effort low`. Raw captures are in
[`internal/session/claude/testdata/`](../../internal/session/claude/testdata/),
one scenario per prefix: `<name>.args` is the command line,
`<name>.stdin.jsonl` what the probe wrote, `<name>.stdout.jsonl` what the
CLI wrote. Home paths and the account email are replaced with `/home/user`
and `user@example.com`. Samples here are trimmed; the fixtures are not.

## Version and flags

`claude --version` prints `2.1.314 (Claude Code)`. From `claude --help`:

| Flag | Help text, abridged |
| --- | --- |
| `-p, --print` | Non-interactive. Skips the workspace trust dialog. |
| `--input-format <text\|stream-json>` | `stream-json` is "realtime streaming input". Needs `-p`. |
| `--output-format <text\|json\|stream-json>` | Needs `-p`. |
| `--verbose` | "Override verbose mode setting". Without it: `Error: When using --print, --output-format=stream-json requires --verbose`, exit 1. |
| `--include-partial-messages` | Emits `stream_event` deltas. |
| `--include-hook-events` | Emits every hook lifecycle event. |
| `--replay-user-messages` | Echoes stdin user messages back with their `uuid`. |
| `--permission-mode <mode>` | `acceptEdits`, `auto`, `bypassPermissions`, `manual`, `dontAsk`, `plan`. |
| `--permission-prompts <host\|none>` | Who answers prompts: "the SDK host or --permission-prompt-tool", or nobody. Default `host`. |
| `-r, --resume [id]` | Resume by session id or name. |
| `--fork-session` | With `--resume`/`--continue`: new session id instead of reusing the old one. |
| `--session-id <uuid>` | Use this id for the conversation. |
| `-c, --continue` | Most recent conversation in the cwd. |
| `--no-session-persistence` | Nothing saved, cannot resume. |
| `--model <model>`, `--effort <low..max>`, `--fallback-model` | |
| `--mcp-config <configs...>`, `--strict-mcp-config` | |
| `--settings <file-or-json>`, `--setting-sources <user,project,local>` | |
| `--allowedTools`, `--disallowedTools`, `--tools` | |
| `--max-budget-usd`, `--json-schema`, `--add-dir`, `--append-system-prompt` | |

Two flags the probe used are not in `--help`. Both work on 2.1.314, and the
Agent SDK passes both (read from `sdk.mjs` in
`@anthropic-ai/claude-agent-sdk` 0.3.282, the version aide uses):

- `--permission-prompt-tool stdio` turns on `can_use_tool` control requests
  over stdin/stdout. The SDK passes exactly this when a `canUseTool`
  callback is set. `--permission-prompt-tool <mcp tool name>` routes the
  same asks to an MCP tool instead.
- `--resume-session-at <message uuid>` resumes the transcript cut at that
  message. The SDK maps `resumeSessionAt` to it. The SDK also has
  `--resume-drops-turn`, which the probe did not exercise.

The SDK's `sdk.d.ts` documents every control request and message type, and
is the best spec for this protocol.

## Wire basics

One JSON object per line in each direction. A user turn on stdin:

```json
{"type":"user","session_id":"","parent_tool_use_id":null,"message":{"role":"user","content":"Reply with exactly the word: pong"}}
```

Either side can send a control request; the receiver answers with one
`control_response` carrying the same `request_id`:

```json
> {"type":"control_request","request_id":"req_1","request":{"subtype":"initialize"}}
< {"type":"control_response","response":{"subtype":"success","request_id":"req_1","response":{"commands":[...],"models":[...],"account":{"email":"user@example.com","subscriptionType":"Claude Max","apiProvider":"firstParty"},"current_permission_mode":"default","session_state":"idle",...}}}
```

`initialize` is optional. Every scenario except `basic` and `hooks` skipped
it and worked. It is the only way to register SDK-style hook callbacks
(see Hooks).

The CLI prints nothing until the first user message arrives. Then each
turn emits `system/init` again (with `session_id`, `model`,
`permissionMode`, `tools`, `mcp_servers`, `capabilities`), then
`assistant` and `user` (tool result) messages and one `result`. The first
turn of a process also carries a `rate_limit_event`. `basic.stdout.jsonl`:

```json
{"type":"system","subtype":"init","cwd":"/tmp/stream-json-scratch","session_id":"e4cb503e-…","model":"claude-sonnet-5-5","permissionMode":"default","claude_code_version":"2.1.314","capabilities":["interrupt_receipt_v1","interrupt_cancel_queued_v1",…],…}
{"type":"assistant","message":{"model":"claude-sonnet-5-5","content":[{"type":"text","text":"pong"}],"usage":{…}},"session_id":"e4cb503e-…","uuid":"8c0f054c-…"}
{"type":"result","subtype":"success","is_error":false,"result":"pong","stop_reason":"end_turn","terminal_reason":"completed","session_id":"e4cb503e-…","total_cost_usd":0.0244504,"usage":{…},"modelUsage":{…},"permission_denials":[],"num_turns":1}
```

The process stays up between turns. Closing stdin ends it: exit 0 when the
last `result` succeeded, exit 1 when it was an error (an interrupted turn
counts).

## Approvals

**Answer: a client can answer permission requests over stdin. No MCP
server is needed.** Pass `--permission-prompt-tool stdio`. Each ask arrives
as a `can_use_tool` control request (`permission-stdio.stdout.jsonl`):

```json
< {"type":"control_request","request_id":"efc1a10c-…","request":{"subtype":"can_use_tool","tool_name":"Bash","display_name":"Bash","input":{"command":"touch made-by-bash.txt","description":"Create an empty file"},"description":"Create an empty file","permission_suggestions":[{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"touch made-by-bash.txt"}],"behavior":"allow","destination":"localSettings"},{"type":"setMode","mode":"acceptEdits","destination":"session"}],"blocked_path":"/tmp/stream-json-scratch/made-by-bash.txt","tool_use_id":"toolu_01MDbpPT…"}}
```

Allow, optionally with edited input:

```json
> {"type":"control_response","response":{"subtype":"success","request_id":"f6dae1ab-…","response":{"behavior":"allow","updatedInput":{"file_path":"/tmp/stream-json-scratch/notes.txt","content":"hello\n"}}}}
```

Deny. The message becomes the tool result the model sees, and the turn
goes on:

```json
> {"type":"control_response","response":{"subtype":"success","request_id":"efc1a10c-…","response":{"behavior":"deny","message":"The pitwall user denied this command."}}}
< {"type":"user","message":{"content":[{"type":"tool_result","content":"The pitwall user denied this command.","is_error":true,…}]}}
< {"type":"result","subtype":"success","result":"denied","permission_denials":[{"tool_name":"Bash","tool_use_id":"toolu_01MDbpPT…","tool_input":{…}}],…}
```

Read-only commands such as `echo hook-test` ran without an ask. The
`permission_suggestions` array is what an "always allow" button would send
back as `updatedPermissions` (not exercised).

Without `--permission-prompt-tool`, every ask is denied on the spot. The
stream says so with a `system/permission_denied` event
(`permission-no-handler.stdout.jsonl`):

```json
{"type":"system","subtype":"permission_denied","tool_name":"Write","tool_use_id":"toolu_01L1EwiC…","message":"Claude requested permissions to write to /tmp/stream-json-scratch/other.txt, but you haven't granted it yet."}
```

The MCP route works too (`permission-mcp.*`). With
`--mcp-config` naming a stdio server and
`--permission-prompt-tool mcp__probe__approve`, the CLI calls that tool for
every ask, MCP tools included. The tool returns the same allow/deny JSON as
text (`permission-mcp.mcp-server.txt`, `RESP` lines are the server's
replies):

```json
{"method":"tools/call","params":{"name":"approve","arguments":{"tool_name":"Write","input":{"file_path":"/tmp/stream-json-scratch/mcp.txt","content":"hello\n"},"tool_use_id":"toolu_01Y196M9…"}},"jsonrpc":"2.0","id":4}
RESP {"id":4,"jsonrpc":"2.0","result":{"content":[{"type":"text","text":"{\"behavior\":\"allow\",\"updatedInput\":{…}}"}]}}
```

It needs a separate server process and a channel back to the daemon, so
the stdio route is simpler.

`AskUserQuestion` also arrives as `can_use_tool`, with
`requires_user_interaction: true`. Answer with `allow` and the answers
keyed by question text (`ask-user-question.*`, the same shape aide sends):

```json
< {"type":"control_request","request_id":"ad218f90-…","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"question":"Which fruit do you prefer?","header":"Fruit","options":[{"label":"apple",…},{"label":"banana",…},{"label":"cherry",…}],"multiSelect":false}]},"tool_use_id":"toolu_015GhzUN…","requires_user_interaction":true}}
> {"type":"control_response","response":{"subtype":"success","request_id":"ad218f90-…","response":{"behavior":"allow","updatedInput":{"questions":[…],"answers":{"Which fruit do you prefer?":"cherry"}}}}}
```

Allowing with the input unchanged gives the model "The user did not answer
the questions." (`plan.stdout.jsonl`).

## Interrupt

**Answer: send `{"subtype":"interrupt"}` as a control request. It answers
at once, the turn ends with an error `result`, and the session takes the
next user message.** Three cases were captured.

During a running tool (`interrupt-tool.stdout.jsonl`), a foreground Bash
command was 4 s into a 40 s sleep:

```json
> {"type":"control_request","request_id":"req_1","request":{"subtype":"interrupt"}}
< {"type":"control_response","response":{"subtype":"success","request_id":"req_1","response":{"still_queued":[]}}}
< {"type":"system","subtype":"task_notification","task_id":"b357wc0a9","tool_use_id":"toolu_01BoJ1ng…","status":"stopped",…}
< {"type":"user","message":{"content":[{"type":"tool_result","content":"The user doesn't want to proceed with this tool use. … this call was stopped while running, not refused before it ran. …","is_error":true,…}]}}
< {"type":"user","message":{"content":[{"type":"text","text":"[Request interrupted by user for tool use]"}]}}
< {"type":"result","subtype":"error_during_execution","is_error":true,"terminal_reason":"aborted_streaming","stop_reason":"tool_use",…}
```

During streamed text (`interrupt-partial.stdout.jsonl`, with
`--include-partial-messages`), the CLI flushes the partial `assistant`
message (it had counted to 96), then `[Request interrupted by user]`, then
the same error `result`.

While a `can_use_tool` is unanswered
(`interrupt-pending-permission.stdout.jsonl`), the CLI withdraws the ask
with `control_cancel_request` before it answers the interrupt. The driver
must close the approval it is showing:

```json
< {"type":"control_cancel_request","request_id":"bdfd90a9-…"}
< {"type":"control_response","response":{"subtype":"success","request_id":"req_1","response":{"still_queued":[]}}}
```

An interrupt while idle gets the same empty success response and nothing
else (`interrupt.stdout.jsonl`, last line). After each interrupt, the next
user message ran normally ("still here").

Claude Code itself refuses `sleep 45 && echo finished` ("Blocked: sleep 45
followed by …"), which is why the tool case used `python3 -c`. A driver
test that wants a long tool needs a command that is not a bare `sleep`.

## Resume, fork and rewind

**Answer: all of them work in this mode. Store the session id, and choose
it yourself with `--session-id`.** `resume-*` (one process each, run in
order):

| Run | Flags | New `session_id` | Answer to "current code word?" |
| --- | --- | --- | --- |
| 1 | `--session-id 40ab806d-…` | `40ab806d-…` (the chosen one) | set PAPAYA, then MANGO |
| 2 | `--resume 40ab806d-…` | `40ab806d-…` (same) | MANGO |
| 3 | `--resume 40ab806d-… --fork-session` | `5428c68a-…` (new) | MANGO |
| 4 | `--resume 40ab806d-… --resume-session-at 07a45f3e-… --fork-session` | `5fdfaca6-…` (new) | PAPAYA |
| 5 | `--continue` | `5fdfaca6-…` (the latest in the cwd) | PAPAYA |

`07a45f3e-…` is the `uuid` of the assistant message that answered the first
turn, as it appeared on stdout. Run 4 dropped everything after it.

The session id is the transcript's file name,
`~/.claude/projects/<cwd with / as ->/<session_id>.jsonl`. It is also on
every stdout message, but only once the first turn starts, so passing
`--session-id` is the only way to know it at spawn. A fork reports its new
id in the next `system/init`; the driver must store that one from then on.

A client can set `uuid` on a stdin user message. The CLI keeps it: the
`--replay-user-messages` echo carries it with `"isReplay":true`, and it is
written to the transcript. That gives pitwall stable ids for rewind
points without parsing transcripts.

The probe did not test `--resume` on a session another process still has
open, or `--resume-drops-turn`.

## Hooks

Command hooks from `--settings` (or project settings) run in `-p` mode
with the usual stdin payloads. With `--include-hook-events` each one shows
up on stdout (`hooks.stdout.jsonl`):

```json
{"type":"system","subtype":"hook_started","hook_id":"029bc213-…","hook_name":"PreToolUse:Bash","hook_event":"PreToolUse"}
{"type":"system","subtype":"hook_response","hook_id":"029bc213-…","hook_name":"PreToolUse:Bash","hook_event":"PreToolUse","output":"","stdout":"","stderr":"","exit_code":0,"outcome":"success"}
```

SessionStart, UserPromptSubmit, PreToolUse, PostToolUse and Stop all fired.
SDK-style hooks are registered in `initialize` and come back as control
requests the driver answers:

```json
> {"type":"control_request","request_id":"req_1","request":{"subtype":"initialize","hooks":{"PreToolUse":[{"matcher":"Bash","hookCallbackIds":["probe_pre_bash"]}]}}}
< {"type":"control_request","request_id":"b679a1c2-…","request":{"subtype":"hook_callback","callback_id":"probe_pre_bash","input":{"session_id":"0bf013c4-…","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"echo hook-test",…},…}}}
> {"type":"control_response","response":{"subtype":"success","request_id":"b679a1c2-…","response":{"continue":true}}}
```

So pitwall's existing hook scripts keep working for managed sessions, and
the driver can also take hook events in-process. `get_hooks_listing`
returns the registered hooks per event.

The probe ran with `--setting-sources project` so the user's own hooks and
plugins stayed out. A real managed session will load the user's settings,
pitwall's hooks included. ENG-2678 has to decide whether those hooks should
also report a managed session to the daemon.

## MCP config

`--mcp-config '{"mcpServers":{"probe":{"type":"stdio","command":…}}}'`
plus `--strict-mcp-config` loaded exactly that server. `system/init`
lists it and its tools:

```json
"mcp_servers":[{"name":"probe","status":"connected","source":"dynamic"}],"tools":[…,"mcp__probe__echo"]
```

The model called `mcp__probe__echo` like any tool, and the ask went
through the permission tool first (`permission-mcp.mcp-server.txt`).
`mcp_status` answers with the server list. The protocol also has
SDK-hosted MCP servers that talk over the control channel (`mcp_message`);
not exercised.

## Plan mode

`--permission-mode plan` starts in plan mode (`"permissionMode":"plan"` in
`system/init`). The model writes its plan to a file and calls
`ExitPlanMode`, which arrives as `can_use_tool` with
`requires_user_interaction: true` (`plan.stdout.jsonl`):

```json
< {"type":"control_request","request_id":"f2bd0a8a-…","request":{"subtype":"can_use_tool","tool_name":"ExitPlanMode","input":{"plan":"1. Create plan.txt …\n2. Verify …\n","planFilePath":"/home/user/.claude/plans/plan-adding-a-file-sharded-quilt.md"},"tool_use_id":"toolu_01Dxc5fB…","requires_user_interaction":true}}
```

The probe denied it ("Not yet. Keep planning.") and the model stayed in
plan mode. Then it switched modes mid-session:

```json
> {"type":"control_request","request_id":"req_1","request":{"subtype":"set_permission_mode","mode":"acceptEdits"}}
< {"type":"control_response","response":{"subtype":"success","request_id":"req_1","response":{"mode":"acceptEdits"}}}
< {"type":"system","subtype":"status","status":null,"permissionMode":"acceptEdits"}
```

The next turn wrote `plan.txt` without an ask. The plan file lands in
`~/.claude/plans/`, outside the repo, even for a session in a scratch
directory.

## Model and effort

Both start from flags (`--model`, `--effort`) and change mid-session
(`model-effort.stdout.jsonl`):

```json
> {"type":"control_request","request_id":"req_1","request":{"subtype":"set_model","model":"claude-opus-5-5"}}
< {"type":"control_response","response":{"subtype":"success","request_id":"req_1","response":{"model":"claude-opus-5-5"}}}
> {"type":"control_request","request_id":"req_2","request":{"subtype":"apply_flag_settings","settings":{"effortLevel":"medium"}}}
< {"type":"control_response","response":{"subtype":"success","request_id":"req_2"}}
> {"type":"control_request","request_id":"req_3","request":{"subtype":"get_settings"}}
< {"type":"control_response","response":{"subtype":"success","request_id":"req_3","response":{"applied":{"model":"claude-opus-5-5","effort":"medium",…},…}}}
```

The next turn's `system/init` and `assistant.message.model` said
`claude-opus-5-5`, and the `result`'s `modelUsage` listed both models.
`set_model` also writes a `<local-command-stdout>Set model to …` user
message to stdout. `set_model` with `"default"` resolved to
`claude-opus-5-5` on this account. The `initialize` response lists every
model with `supportedEffortLevels`, which is enough for a picker.

## Images

A user message takes Messages API content blocks. A base64 PNG worked
(`image.*`; the model answered "Red" for a red square):

```json
{"type":"user","session_id":"","parent_tool_use_id":null,"message":{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo…"}},{"type":"text","text":"What single color fills this image? Reply with one word."}]}}
```

## Usage and limits

- Every `assistant` message carries the API `usage` (input, output, cache
  read and write tokens).
- Every `result` carries `usage`, `modelUsage` per model with `costUSD`
  and `contextWindow`, `total_cost_usd`, `num_turns`, `duration_ms` and
  `permission_denials`. `total_cost_usd` adds up over the life of the
  process; `model_usage_since_last_result` has the share for this turn.
- On a subscription account a `rate_limit_event` arrives after the first
  API response of each process. Later turns in the same process sent none,
  so it is not a per-turn meter; poll `get_usage` for that:

```json
{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning","resetsAt":1793386800,"rateLimitType":"seven_day","utilization":0.8,"surpassedThreshold":0.75,"overageStatus":"rejected","overageDisabledReason":"org_level_disabled","unifiedWindows":{"five_hour":{"utilization":0.18,"resetsAt":1793388600,"status":"allowed"},"seven_day":{"utilization":0.8,"resetsAt":1793386800,"status":"allowed_warning","surpassedThreshold":0.75}}}}
```

- `get_usage` (with `skip_behaviors: true`) answers with the session cost
  and the plan windows in percent (`"five_hour":{"utilization":18,"resets_at":"2026-10-30T19:29:59…"}`).
  `get_context_usage` answers with the context window by category.

The probe never hit a limit, so the `rejected` status and the error
`result` that goes with it are uncaptured. `SDKRateLimitInfo` in `sdk.d.ts`
lists their fields, and pitwall's screen-based limit detection
(`internal/agent/limit.go`) is the fallback until a real sample exists.

Other messages seen: `system/status` (`"status":"requesting"` before the
API call, only in the runs with `--include-partial-messages`; with a
`permissionMode` after `set_permission_mode`), `system/thinking_tokens` (estimated thinking progress),
`system/task_started` and `task_notification` around Bash, and, with
`--include-partial-messages`, `stream_event` wrapping the raw API events
(`message_start`, `content_block_delta`, …). Thinking blocks come with
empty `thinking` text and a signature.

## Not covered

- A limit being hit (`rate_limit_event` with `status: rejected`).
- Sending a user message while a turn runs. The interrupt response's
  `still_queued` and the `interrupt_cancel_queued_v1` capability say the
  CLI queues it; untested.
- `--resume` of a session that another process has open.
- `updatedPermissions` on an allow ("always allow").
- `--bare` and API-key auth. The probe used the logged-in OAuth account.
- macOS and Windows. Everything ran on Linux.

## Recommendation

Drive stream-json directly from Go. Skip the Node helper.

Every question in the ticket has a working answer over stdin and stdout
with no MCP server and no Node. The Agent SDK is a thin client of this
same protocol: it spawns the CLI with `--input-format stream-json
--output-format stream-json --verbose --permission-prompt-tool stdio` and
sends the control requests shown above. A Node helper would add a runtime
to install and a second process per session, and would gain nothing the
wire does not already give.

The cost is that the protocol is not a public, stable contract. Two flags
the driver needs (`--permission-prompt-tool`, `--resume-session-at`) are
missing from `--help`, and Claude Code ships often. To keep that cheap:

1. Use `sdk.d.ts` from `@anthropic-ai/claude-agent-sdk` as the spec, and
   check it again when either side moves.
2. Decode leniently. Switch on `type` and `subtype`, ignore unknown types
   and fields, and answer CLI control requests the driver does not know
   with an error response (`{"subtype":"error","request_id":…,"error":"…"}`)
   so the CLI never waits on pitwall.
3. Record `claude_code_version` and `capabilities` from `system/init`.
   Gate optional behavior on capabilities rather than on version numbers.
4. Run the driver tests (ENG-2682) against the fixtures in
   `internal/session/claude/testdata/`, and re-capture them when
   the CLI changes the shapes.
5. Keep the Go driver interface narrow (start, send, approve, interrupt,
   set mode, set model, close, events out) so a Node helper could sit
   behind it later if the CLI ever drops this mode.

For the design (ENG-2678): spawn with a pitwall-chosen `--session-id` and
store that id; store the new id after a fork; set `uuid` on every user
message so rewind targets are pitwall's own ids; pass
`--permission-prompt-tool stdio`, and treat `control_cancel_request` as
"close this approval".
