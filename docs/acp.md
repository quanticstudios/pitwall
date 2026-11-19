# ACP agents

Configure an executable that speaks the Agent Client Protocol over stdio in
`config.toml`. Arguments are passed directly to the executable. No shell is
involved.

```toml
[[agents.acp]]
name = "my-agent"
command = "/path/to/acp-agent"
args = ["--acp"]
env = ["EXAMPLE_API_KEY=example"]
```

The executable and its flags depend on the agent. This example is a placeholder,
not a bundled provider. Names use letters, digits, underscores and hyphens,
starting with a letter or digit, and must be unique. Environment entries are
`NAME=value` strings. They override the daemon's environment; a chat's own
overrides take precedence. Restart the daemon after changing the configuration.

List and select agents with:

```sh
pitwall chat providers
pitwall chat providers --json
pitwall chat new --provider acp:my-agent --dir /absolute/project -- "Read this project"
```

At socket protocol level 39, `proto.ChatProviders{}` returns
`proto.ChatProviderList{Providers: []proto.ChatProvider}`. Each entry has `ID` and
`Name`. This is the daemon's shared discovery entry point for pitwall and client
adapters such as aide. Commands, arguments and environment values are excluded.
The aide WebSocket bridge and its picker remain the separate managed-session
web/client tickets; this change does not add an aide UI.

## Protocol and capabilities

The driver implements [ACP protocol v1](https://agentclientprotocol.com/protocol/v1/overview)
against the stable [schema-v1.28.0 release](https://github.com/agentclientprotocol/agent-client-protocol/releases/tag/schema-v1.28.0),
including its [method names](https://github.com/agentclientprotocol/agent-client-protocol/releases/download/schema-v1.28.0/meta.json).
It negotiates `protocolVersion: 1` with `initialize`, then calls `session/new` or
`session/load`. Another negotiated protocol version is refused. No dependency
on an ACP SDK is needed.

Supported operations and events:

- Text prompts through `session/prompt`, plus images only when the agent
  advertises `promptCapabilities.image`.
- Text and thought chunks, complete tool values assembled from partial tool
  updates, textual tool output, file change descriptions, and plans from
  `session/update`. History replayed during `session/load` is ignored because
  pitwall already holds that transcript.
- `session/request_permission` becomes a normal chat approval. Answers select
  the agent's exact `optionId` for `allow_once`, `reject_once`, `allow_always` or
  `reject_always`. A missing choice returns `ErrUnsupported` and keeps the
  request pending. Edited inputs and denial reasons are unsupported because
  this ACP response has no corresponding fields.
- `session/cancel` interrupts a turn and resolves outstanding permissions with
  the required `cancelled` outcome. The prompt response ends the turn. Refusals fail the turn with an explanation;
  token and request limits add a system message with the stop reason.
- `session/load` resumes only when the agent advertises `loadSession`. Otherwise
  a system message marks the session as not resumable, the resume-id event
  reports `non-resumable`, and an attempted load returns `ErrUnsupported`.
- `available_commands_update` replaces the chat's slash commands: each
  command's `name`, `description` and `input.hint`. ACP has no TUI, so no
  ACP command is marked `Terminal`, and ACP gives no source.
- Modes and models. `session/new` and `session/load` may return `modes`
  (`currentModeId`, `availableModes`) and `configOptions`. The current mode
  and the current value of the `model` category option go to the snapshot's
  `permissionMode` and `model`, and so do later `current_mode_update` and
  `config_option_update` notifications. These updates count outside a turn
  too, and before `session/new` answers.
- `Set` changes the mode with `session/set_mode` when the agent advertised
  `modes`, else with `session/set_config_option` on its `mode` category
  option. Model and effort use `session/set_config_option` on the `model` and
  `thought_level` category options. v1.28.0 has no `session/set_model`; the
  config option is the stable way. A value that is already current sends
  nothing. A setting the agent gave no way to change returns
  `ErrUnsupported` before anything is sent. When the process starts, it applies a
  chat's saved model, effort and mode the same way, one at a time. A setting
  the agent rejects or cannot change leaves a system message naming it and
  the reason, the session starts anyway, and the snapshot shows the agent's
  real model and mode.
- User input. The driver advertises `elicitation.form` and turns each form
  `elicitation/create` during a turn into a user-input request: one question
  per `requestedSchema` property, in schema order. A string with `oneOf` or
  `enum`, or an array with `items.anyOf` or `items.enum`, lists those values as
  options; a plain string is free text; a boolean offers Yes and No. `Answer`
  replies `accept` with the answered fields in their schema types, or
  `decline` when nothing was answered. A cancelled or finished turn answers
  `cancel`. URL elicitations, request-scoped ones and ones outside a turn get
  JSON-RPC `-32602`.

Steering, fork and rewind return `ErrUnsupported`. Agent authentication must
already be configured, for example through the executable's environment.
Authentication errors are returned as provider errors. Besides form
elicitation, the driver advertises no optional client filesystem, terminal,
authentication or URL elicitation capabilities. Unexpected client requests
receive JSON-RPC `-32601`; unrecognized updates and unsupported content types
are logged once per type per process. They are never treated as executed
operations.

### How claude-agent-acp asks questions

Read from [agentclientprotocol/claude-agent-acp](https://github.com/agentclientprotocol/claude-agent-acp)
(Apache-2.0, 0.108.1, `src/elicitation.ts` and the `ask-user-question`
scenario snapshot). Claude Code routes `AskUserQuestion` through the
adapter's `canUseTool`. When the client advertises `elicitation.form`, the
adapter sends one form `elicitation/create` with the session id and the
tool call id. Each question becomes a `question_<n>` property, titled with
its header: single-select as a string with `oneOf` (`const` and `title` are
the option label, plus its description), multi-select as an array with
`items.anyOf`. Each is followed by an optional free-text
`question_<n>_custom` property titled "Other". A single question's text is
the `message`; with several, each property's `description` holds its
question. The adapter reads `accept` content back by those keys into the
tool's `answers`; `decline` tells the model the questions were skipped, and
`cancel` aborts the tool call. Without `elicitation.form` the adapter
disables `AskUserQuestion` altogether, so the model never asks.

Initialization has a 15-second timeout. Closing stdin requests shutdown; the
process is killed when the close context expires. Unexpected exits report the
exit code and the last 4 KB of stderr, following the Claude driver's structure.
`internal/session/acp/testdata/approval.json` is a scripted, synthetic ACP v1
conversation, not a capture from a real provider. Its test subprocess validates
both directions of the exchange before the events are reduced into a snapshot.
The tests extend it for commands, modes, `session/set_mode`,
`current_mode_update`, the model config option and an `AskUserQuestion`
elicitation shaped like claude-agent-acp's.
