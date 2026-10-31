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

Steering, fork, rewind, user-input answers, and model/effort/mode changes return
`ErrUnsupported`. Agent authentication must already be configured, for example
through the executable's environment. Authentication errors are returned as
provider errors. The driver advertises no optional client filesystem, terminal,
authentication or elicitation capabilities. Unexpected client requests receive
JSON-RPC `-32601`; unrecognized updates and unsupported content types are logged
once per type per process. They are never treated as executed operations.

Initialization has a 15-second timeout. Closing stdin requests shutdown; the
process is killed when the close context expires. Unexpected exits report the
exit code and the last 4 KB of stderr, following the Claude driver's structure.
`internal/session/acp/testdata/approval.json` is a scripted, synthetic ACP v1
conversation, not a capture from a real provider. Its test subprocess validates
both directions of the exchange before the events are reduced into a snapshot.
