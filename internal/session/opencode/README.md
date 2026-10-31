# OpenCode managed driver

`Provider{}` runs `opencode serve --hostname=127.0.0.1 --port=0` in the
chat's working directory with its environment overrides. It reads the SDK's
readiness line and uses a random HTTP Basic password for that process.
`PITWALL_OPENCODE_COMMAND` replaces the executable. `Provider{URL: url}`
attaches to an existing server; `Client` can supply its authentication or
transport. Closing an attached driver aborts its session without stopping
the shared server.

The protocol reference is **OpenCode v1.19.4**, checked against the official
[server docs](https://opencode.ai/docs/server/),
[SDK docs](https://opencode.ai/docs/sdk/),
[versioned SDK v2 types](https://github.com/anomalyco/opencode/blob/v1.19.4/packages/sdk/js/src/v2/gen/types.gen.ts),
[server launcher](https://github.com/anomalyco/opencode/blob/v1.19.4/packages/sdk/js/src/server.ts),
and [permission routes](https://github.com/anomalyco/opencode/blob/v1.19.4/packages/opencode/src/server/routes/instance/httpapi/groups/permission.ts).
The older SDK entry point has stale permission event types; this driver
uses the v2 SDK's public `/session` and `/event` contract, not `/v2/session`.

| Operation | HTTP request |
| --- | --- |
| Version | `GET /global/health` |
| Start | `POST /session`, with an `ask` permission ruleset |
| Resume | `GET /session/{id}`, then `PATCH /session/{id}` to restore managed permissions |
| Events | `GET /event`, SSE, scoped by the `directory` query and filtered by session ID |
| Send | `POST /session/{id}/prompt_async`, with text/file parts, optional model, agent and variant |
| Permission | `POST /permission/{requestID}/reply`, with `reply: once`, `always` or `reject`, and optional denial `message` |
| Question | `POST /question/{requestID}/reply`, with answers ordered by question index |
| Interrupt | `POST /session/{id}/abort` |

Text and reasoning parts become content events. Full part snapshots and
`message.part.delta` append each text fragment once. Tool snapshots carry
input, output, status, command and exit code. Completed assistant messages
update token usage; `session.status` with `idle` ends the turn. The
deprecated `session.idle` event is ignored so a second idle notification
cannot complete a subsequent turn. The last assistant message ID is the
turn point. Disconnects and malformed streams fail the turn and end the
driver rather than attempting an incomplete replay.

`session.resume-id` carries OpenCode's session ID and server version. The
caller saves the ID in `Config.Resume` and supplies `Origin{Resume: id}`
to a fresh provider. The restart test writes and reloads this config using
`eventlog` and verifies a second turn on the same fake server session.
Daemon-level restart recovery remains ENG-2687. This package does not add
a daemon provider registry or chat protocol.

`Set` changes the next prompt's model (`provider/model`), effort (`variant`)
or mode (`default` uses the build agent, `plan` uses the plan agent).
An empty field leaves its setting alone. Mid-turn steering, edited tool
input, other permission modes, fork and rewind return `ErrUnsupported`.
Questions use zero-based string IDs and option labels as option IDs, as
the API expects selected labels in question order.

The tests use `httptest` and the synthetic v1.19.4 transcript in
`testdata/turn.sse`. No OpenCode installation, provider credentials or
model calls are needed. A helper process exercises the executable launch,
working directory, environment, authentication and shutdown. Compatibility
with a live OpenCode installation has not been tested.

```sh
mise exec -- go test -race -timeout 60s ./internal/session/opencode
```
