# Web protocol

Spec for [ENG-2693](https://linear.app/quantic/issue/ENG-2693). It is how
aide, and later the phone, talk to pitwall's daemon about
[managed sessions](managed-sessions.md) (chats): JSON over one WebSocket,
with snapshots and the delta stream of aide's ENG-2655 schema. It holds to
the [web protocol constraints](managed-sessions.md#web) of the managed
sessions design, so the socket and the web never disagree.

[`web-protocol.schema.json`](web-protocol.schema.json) is the JSON Schema
for every frame. `internal/web/schema_test.go` checks it against aide's
delta fixtures (`internal/session/testdata/session-deltas/`) and against
every `json` example in this file.

Who builds it: ENG-2694 the listener, the token file and the handshake;
ENG-2695 subscriptions, resync and backpressure; ENG-2696 the commands;
ENG-2698 conformance fixtures; ENG-2700 aide's WebSocket transport;
ENG-2708 pull requests.

## Contents

- [Connection](#connection)
- [Frames](#frames)
- [Session list](#list)
- [One session's deltas](#deltas)
- [Resync](#resync)
- [Backpressure](#backpressure)
- [Commands](#commands)
- [Pull requests](#pull-requests)
- [Results and errors](#results)
- [aide's SessionTransport](#transport)
- [Versioning](#versioning)
- [Left open](#open)

<a id="connection"></a>
## Connection

### Discovery and auth

The daemon listens on loopback only: `127.0.0.1`, on a port it picks, never
on another interface. When the listener starts, the daemon writes
`web.json` to pitwall's state folder (`~/.local/state/pitwall/` on Linux
and macOS, `%LOCALAPPDATA%\pitwall` on Windows, see [state.md](state.md))
with mode 0600:

| Key | Value |
| --- | --- |
| `url` | the WebSocket URL, `ws://127.0.0.1:<port>/ws` |
| `token` | 64 hex digits, 32 bytes from `crypto/rand` |
| `pid` | the daemon's process id, so a client can tell a stale file |

The token stays the same across daemon restarts. `pitwall web token
--rotate` writes a new one; the daemon checks every `hello` against the
file, so the old token stops working at once, and connections already
open stay. Deleting `web.json` and restarting the daemon makes a new one
too. Only a process that can read the user's files can connect, which is
the same trust the Unix socket has. On Windows the mode bits do nothing,
and the file is as private as `%LOCALAPPDATA%`, which only the user, the
system and administrators can read.

The listener is off until `[web] enabled = true` in `config.toml`, so a
plain install serves nothing new. `[web] port` fixes the port; without it
the system picks a free one each time the daemon starts, and `web.json`
has it. The daemon rereads the setting every 2 seconds, as it does
`[remote]`, so turning it on or off needs no restart.

The HTTP upgrade request is refused with 403 before any frame when:

- `Host` is not `127.0.0.1:<port>` or `localhost:<port>`, which stops DNS
  rebinding.
- `Origin` is set and is not the listener's own origin (`http://127.0.0.1:<port>`
  or `http://localhost:<port>`, for the bundle ENG-2697 serves). Electron's
  main process and Node send no `Origin`. A web page on another site never
  gets as far as the token check.

The token goes in `hello`, not in the URL (URLs land in logs and history)
and not in a header (a browser's `WebSocket` cannot set one). The server
compares it in constant time. `hello` also takes the token of a browser
session for the bundle the daemon serves ([Web client](remote.md#web-client)).

### Handshake

The client's first frame is `hello`:

```json
{"type": "hello", "protocol": 1, "token": "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", "client": {"name": "aide", "version": "0.42.0"}}
```

`client.name` is what [approvals](managed-sessions.md#approvals) record as
the answerer (`web:aide`), and what `daemon.log` names. `client.version` is
for logs only.

The server answers with `welcome`:

```json
{"type": "welcome", "protocol": 1, "server": {"name": "pitwall", "version": "0.31.0"}, "deltaVersion": 1, "capabilities": ["subscribeSessions", "unsubscribeSessions", "listSessions", "subscribe", "unsubscribe", "read", "start", "send", "interrupt", "resolveApproval", "resolveInput", "set", "fork", "rewind", "archive", "unarchive", "import", "listProjects", "listWorktrees", "checkpoints", "checkpointDiff", "rewindPreview", "setJev", "reviewThreads", "prPrompt", "mergePR"]}
```

`capabilities` lists every command type this server accepts. A command
whose ticket has not landed (say `fork` before ENG-2691) is left out, and a
client hides what the list lacks. `deltaVersion` is the `version` every
delta carries (aide's `SESSION_DELTA_VERSION`).

Anything else ends the connection with a WebSocket close code and a reason
a person can read:

| Code | When | Reason, for example |
| --- | --- | --- |
| 4000 | the first frame is not a `hello`, or not JSON | `expected hello` |
| 4001 | the token is wrong or missing | `bad token: read web.json again` |
| 4002 | the server does not speak `protocol` | `pitwall speaks protocol 1, client sent 2: update aide or pitwall` |
| 4003 | no `hello` within 10 s | `no hello within 10s` |
| 1009 | a frame over 16 MiB | `frame over 16 MiB` |
| 1001 | the daemon exits | `daemon exiting` |

A client that gets 4002 does not retry with the same version. A client
that gets 1001 or loses the connection reconnects with backoff and
[resyncs](#resync).

<a id="frames"></a>
## Frames

- One WebSocket text frame holds one JSON object. Every object has `type`.
- The client sends `hello` and then [commands](#commands). Every command
  has an `id`, a string the client picks and does not reuse on that
  connection. The server answers each command with exactly one `result`
  that carries the same `id`.
- The server sends `welcome`, `result`, `error`, and the pushed messages
  `sessions`, `sessionsChanged`, `deltas` and `removed`.
- The server handles one connection's commands in the order they arrive,
  but results can come back in another order, since commands to different
  chats run on different chat loops. Pushed messages for one chat keep
  their order.
- Both sides ignore keys they do not know. A client ignores a server
  message `type` it does not know.

<a id="list"></a>
## Session list

The list is the live chats, each a `model.Chat` as JSON. Its keys are the
Go fields in camelCase; ENG-2684 gives `model.Chat` these JSON tags.

| Key | Meaning |
| --- | --- |
| `id` | the chat id, a UUID; the `sessionId` of its snapshot and deltas |
| `workspaceId` | the tab it owns |
| `provider` | `claude` or `codex` |
| `model`, `effort` | `""` for the provider's default |
| `mode` | the permission mode: `default`, `acceptEdits`, `plan`, `bypassPermissions` |
| `title` | the tab's title |
| `state` | aide's `sessionState`: `connecting`, `ready`, `running`, `waiting`, `stopped`, `error` |
| `pending` | approvals and input requests waiting |
| `recentPrompt` | optional first-prompt excerpt, at most 500 Unicode code points; also available after restart or archive |
| `queued` | messages waiting for the running turn to end |
| `turns` | turns so far |
| `seq` | the seq of the chat's latest delta |
| `updatedAt` | RFC 3339 |
| `pr` | the [pull request](#pull-requests) of the tab's branch; left out when it has none |

`subscribeSessions` gets the whole list at once, then changes:

```json
{"type": "subscribeSessions", "id": "1"}
```

```json
{"type": "result", "id": "1", "ok": true}
```

```json
{"type": "sessions", "chats": [{"id": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a", "workspaceId": "a1b2c3d4e5f60718", "provider": "claude", "model": "", "effort": "", "mode": "default", "title": "Fix the flaky resize test", "state": "running", "pending": 0, "queued": 1, "turns": 3, "seq": 412, "updatedAt": "2026-10-30T09:12:44.123Z"}]}
```

```json
{"type": "sessionsChanged", "changed": [{"id": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a", "workspaceId": "a1b2c3d4e5f60718", "provider": "claude", "model": "", "effort": "", "mode": "default", "title": "Fix the flaky resize test", "state": "waiting", "pending": 1, "queued": 1, "turns": 3, "seq": 415, "updatedAt": "2026-10-30T09:12:51.002Z"}], "removed": []}
```

`changed` holds whole entries, added or updated. `removed` holds the ids
of chats that were archived or deleted. The server sends `sessionsChanged`
when a field of `model.Chat` changes, which never happens per streamed
token ([placement](managed-sessions.md#placement)). `unsubscribeSessions`
stops them:

```json
{"type": "unsubscribeSessions", "id": "19"}
```

`listSessions` is the one-shot form, and the only way to see archived
chats. An archived chat's entry comes from its `config.json` alone: the
title and time it had when it was archived or [imported](#import), state
`ready`, and 0 for the counts.

```json
{"type": "listSessions", "id": "2", "archived": true}
```

```json
{"type": "result", "id": "2", "ok": true, "data": {"chats": []}}
```

<a id="deltas"></a>
## One session's deltas

A chat's contents are a snapshot and the deltas after it, in aide's shapes
([Delta](managed-sessions.md#events), aide's
`fixtures/session-deltas/README.md`). The web carries them as `ChatDeltas`
does on the socket:

```json
{"type": "subscribe", "id": "3", "sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a", "after": 0}
```

```json
{"type": "result", "id": "3", "ok": true}
```

```json
{"type": "deltas", "sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a", "seq": 412, "snapshot": {"sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a", "paneId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a", "workspaceId": "a1b2c3d4e5f60718", "provider": "claude", "content": [], "turns": [], "pendingApprovals": [], "pendingUserInputs": [], "planApproval": null, "sessionState": "running", "running": true, "detail": null, "lastCompletedAt": null, "lastError": null, "lastErrorAt": null, "model": null, "effort": null, "serviceTier": null, "permissionMode": "default", "interactionMode": "build", "selectionNotice": null, "runtimeGeneration": 1, "revision": 230, "updatedAt": "2026-10-30T09:12:44.123Z"}, "deltas": []}
```

```json
{"type": "deltas", "sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a", "deltas": [{"version": 1, "sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a", "seq": 413, "revision": 231, "updatedAt": "2026-10-30T09:12:45.001Z", "type": "item.text.append", "itemId": "msg_01", "text": "Reading the test", "itemUpdatedAt": "2026-10-30T09:12:45.001Z"}]}
```

- With `snapshot`, `seq` is the snapshot's seq: the client replaces what it
  holds with `{seq, snapshot}`, then applies `deltas`.
- Without `snapshot`, `deltas` starts at the seq after the client's last
  one. The client applies them with aide's `applyDelta`.
- `deltas` can itself hold a `snapshot` delta (a rewind writes one), which
  replaces everything as aide's README says.
- The server sends `deltas` as the chat loop numbers them, already
  coalesced ([Delta](managed-sessions.md#events)), so `seq` steps by one.

`unsubscribe` stops them:

```json
{"type": "unsubscribe", "id": "20", "sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a"}
```

Subscribing to a chat already subscribed on that
connection is a [resync](#resync), not a second stream.

When a subscribed chat is archived or deleted, the server sends `removed`
and the subscription ends. This is aide's `removed` event:

```json
{"type": "removed", "sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a"}
```

`read` is the one-shot form. It takes the same `after` and answers with
the same payload in `data`. On an archived chat it returns the saved
snapshot.

```json
{"type": "read", "id": "4", "sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a", "after": 415}
```

```json
{"type": "result", "id": "4", "ok": true, "data": {"seq": 415, "deltas": []}}
```

<a id="resync"></a>
## Resync

The client sends its last seq as `after` in `subscribe`. The chat loop
keeps the last 1,024 deltas of each chat in memory:

| `after` | The server sends |
| --- | --- |
| 0 | a snapshot |
| the latest seq | nothing until the next delta |
| within the last 1,024 | the deltas after it |
| older, or above the latest | a snapshot |

The log on disk is never read for resync. A client resyncs when:

- it reconnects: `subscribe` with the seq it held;
- aide's `applyDelta` throws `SessionDeltaResyncError` (`seq-gap`, `item`,
  `session`, `no-base`): `subscribe` with `after: 0`;
- a delta's `version` is not `welcome.deltaVersion`: the client cannot read
  this server, and says so instead of looping.

The session list has no seq. After a reconnect, `subscribeSessions` gets
the whole list again.

<a id="backpressure"></a>
## Backpressure

Each connection has one outgoing queue. When it passes 256 messages or
4 MiB:

1. The server drops every queued `deltas`, `sessions` and
   `sessionsChanged` message.
2. For each chat the connection subscribes to, it queues one `deltas`
   with a fresh snapshot, and for the list subscription one `sessions`.
   It builds each when the writer reaches it, so a slow client holds at
   most one snapshot per chat.
3. Deltas numbered after that snapshot follow as usual.

`result` and `error` messages are never dropped. They are bounded by the
client's own commands. The server does not merge deltas per client; that
would break the rule that `seq` steps by one.

A client whose writes block for 30 s is closed with 1008 (`client too
slow`), and resyncs on reconnect.

<a id="commands"></a>
## Commands

Every command maps to the chat-loop operation of the
[socket message](managed-sessions.md#socket) named in the table, with the
same checks and the same [result codes](#results). `sessionId` is a chat
id; the socket's other ways to name a chat (id prefix, `#N`) are for the
CLI only.

| Command | Socket message | Data on success |
| --- | --- | --- |
| `subscribeSessions`, `unsubscribeSessions` | `Hello` at `ChatLevel` and `State` | none |
| `listSessions` | `ChatList` | `chats` |
| `subscribe`, `unsubscribe` | `ChatSubscribe`, `ChatUnsubscribe` | none |
| `read` | `ChatRead` | `seq`, `snapshot`, `deltas` |
| `start` | `ChatNew` | `sessionId`, `workspaceId` |
| `send` | `ChatSend` | `turnId` when it started a turn |
| `interrupt` | `ChatInterrupt` | none |
| `updateQueue` | `ChatQueue` | `turnId` if steering starts a turn |
| `resolveApproval` | `ChatApprove` | none |
| `resolveInput` | `ChatAnswer` | none |
| `set` | `ChatSet` | none |
| `fork` | `ChatFork` | `sessionId`, `workspaceId` of the new chat |
| `rewind` | `ChatRewind` | none |
| `archive` | `ChatArchive` | none |
| `unarchive` | `ChatUnarchive` | `workspaceId` |
| `import` | none: it writes an archived chat's folder | `imported` |
| `listProjects` | the daemon's `State` | `projects` |
| `listWorktrees` | `gitstat.ListWorktrees` | `worktrees` |
| `setJev` | none: aide's Jev gates live in the daemon's memory | none |
| `checkpoints` | the chat's turns and `gitstat.Checkpoints` | `checkpoints` |
| `checkpointDiff` | `gitstat.CheckpointDiff` | `patch`, `files` |
| `rewindPreview` | `gitstat.CheckpointDiff`, twice | `patch`, `files`, `turnPatch`, `turnFiles` |
| `readUsage` | `internal/flow`, as Settings, Usage reads it | `usage` |
| `reviewThreads` | the window's Send review comments, without the send | `threads`, `changeRequests` |
| `prPrompt` | Send failures or Send review comments, without the send | `text` |
| `mergePR` | the merge dialog's `gh pr merge` | none |

### start

```json
{"type": "start", "id": "5", "dir": "/home/me/src/pitwall", "provider": "claude", "model": "claude-opus-5-5", "effort": "high", "permissionMode": "acceptEdits", "worktree": "fix-resize", "base": "main", "name": "resize", "prompt": "Fix the flaky resize test", "attachments": [{"kind": "workspace-file", "id": "att-1", "path": "internal/pane/pane_test.go"}]}
```

```json
{"type": "result", "id": "5", "ok": true, "data": {"sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a", "workspaceId": "a1b2c3d4e5f60718"}}
```

`dir` and `provider` are required. `dir` is the folder, or the repo a
worktree is made from. `worktree`, `base` and `branch` are as
`model.Task`. `name` is the tab's name. The daemon picks the chat id and
puts the tab in the pitwall session the user used last.

`attachments` (here and in `send`) are aide's `ManagedSessionAttachment`:
an `image` with base64 `data`, a `workspace-file` with a `path` under
`dir`, or `pasted-text` with a `name` and `text`. They show on the
`user-prompt` item and on a queued message. The provider gets each image
as an image (Claude's stream-json `image` block, Codex's `image` input),
and the text and files quoted after the prompt as aide's context
preparation quotes them; a workspace file that is a PNG, JPEG or WebP goes
as an image. The server reads workspace files when the turn starts.

It refuses, with `invalid`, what aide refuses: more than 8, two with one
`id`, an image that is not the PNG, JPEG or WebP its `mimeType` says, an
image over 5 MiB or images over 10 MiB in all, pasted text over 256 KiB, a
workspace file that is a link, outside `dir`, over 256 KiB or neither
UTF-8 text nor an image, and pasted text and files over 1 MiB in all.

### send

```json
{"type": "send", "id": "6", "sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a", "text": "Also check the Windows path"}
```

```json
{"type": "result", "id": "6", "ok": true, "data": {"turnId": "turn-4"}}
```

Steer and queue are ways to send, not commands of their own, as in aide.
`how` omitted starts a turn when the chat is idle and queues behind the
running turn otherwise. `how: "steer"` steers the running turn, and is
queued instead (with a `message` saying so) when the provider cannot.
`how: "queue"` always queues. A queued send has no `turnId`.

```json
{"type": "send", "id": "7", "sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a", "text": "Stop editing the fixtures", "how": "steer"}
```

```json
{"type": "result", "id": "7", "ok": true, "message": "Claude cannot steer yet; queued for the next turn"}
```

### updateQueue

Queue actions run atomically on the chat loop. `messageId` identifies a
`queuedInputs` entry. `edit` needs nonblank `text`; `move` needs a zero-based
`index` within the current queue; `remove` drops the entry. Unknown message
ids and invalid indices answer
`invalid` without changing the queue. `steer` promotes the message to the
running turn, or sends it when idle; a provider that cannot steer queues it.
`resume` needs no message id and releases a queue paused by interrupt.

```json
{"type":"updateQueue","id":"queue-edit","sessionId":"c1","action":"edit","messageId":"m1","text":"revised prompt"}
```

```json
{"type":"updateQueue","id":"queue-move","sessionId":"c1","action":"move","messageId":"m1","index":0}
```

```json
{"type":"updateQueue","id":"queue-remove","sessionId":"c1","action":"remove","messageId":"m1"}
```

```json
{"type":"updateQueue","id":"queue-steer","sessionId":"c1","action":"steer","messageId":"m1"}
```

```json
{"type":"updateQueue","id":"queue-resume","sessionId":"c1","action":"resume"}
```

The snapshot carries `queuedInputs` in queue order, with `id`, `text`,
`createdAt`, `attachments` and `recipients`, and `queuePaused`. Changes use
`session.patch`, including an empty array when the last message leaves.
The config remains the durable queue; loading an older config-only queue
publishes it into the snapshot before a read.

A rejected provider limit sets `usageLimit` on the snapshot with `provider`,
`limitId`, a display `label`, and `resetsAt` as an ISO timestamp or null.
A `limit` delta carries this object in `limit`, or null to clear it.
It survives the failed turn and clears on the next `turn.started`.
Non-rejected quota observations do not replace it.

For tests with `PITWALL_FAKE_PROVIDERS=1`, `start` may carry
`fakeStartFailure: "missing-cli"` to fail that start with a missing-executable
message. Any other nonempty value requests a generic start failure. Real
providers ignore it. It is never persisted or applied to another chat.

### interrupt

```json
{"type": "interrupt", "id": "8", "sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a"}
```

The turn ends `interrupted` and the queue is held until the next `send`
([lifecycle](managed-sessions.md#lifecycle)). With no turn running the
result is still `ok`: nothing runs, which is what the client asked for.

### resolveApproval and resolveInput

```json
{"type": "resolveApproval", "id": "9", "sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a", "requestId": "toolu_01", "decision": "deny", "reason": "Not on main"}
```

```json
{"type": "resolveInput", "id": "10", "sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a", "requestId": "question-1", "answers": {"branch": ["main"]}}
```

`decision` is `allow`, `allow-session` or `deny`. `answers` maps a
question id to the chosen labels or typed text, always a list. The first
answer from any client wins; a later one gets `conflict`
([who answers approvals](managed-sessions.md#approvals)).

```json
{"type": "result", "id": "9", "ok": false, "code": "conflict", "message": "toolu_01 was answered from cli"}
```

### set

```json
{"type": "set", "id": "11", "sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a", "model": "claude-sonnet-5-5", "permissionMode": "plan"}
```

`model`, `effort` and `permissionMode` are each optional; a key left out
stays as it is. The snapshot gets a `session.patch` delta.

`provider` other than the chat's switches the chat to it, as aide's
`switchSessionProvider` does. `model` and `effort` are then the new
provider's, and one left out is its default. The process ends, the new
provider starts a session of its own, the snapshot's `provider`, `model`,
`effort` and `runtimeGeneration` change, and the transcript gets a system
line "Switched to Codex". The next message carries the conversation so far
the way aide's `prepareInput` quotes it: the user and assistant texts, the
last 24,000 characters, as JSON after the message. A turn running or
waiting on an answer gets `busy`; switch once it ends. A provider that does
not start answers `provider` and leaves the chat on the one it had.

```json
{"type": "set", "id": "14", "sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a", "provider": "codex", "model": "gpt-6.1-sol", "effort": "high"}
```

### fork and rewind

```json
{"type": "fork", "id": "12", "sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a", "turnId": "turn-2", "worktree": "resize-alt"}
```

```json
{"type": "rewind", "id": "13", "sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a", "turnId": "turn-3"}
```

```json
{"type": "rewind", "id": "13", "sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a", "turnId": "turn-3", "keepFiles": true}
```

`fork` makes a new chat holding everything up to the end of `turnId`, in a
new worktree when `worktree` is set. `rewind` restores files to the
checkpoint of `turnId` and drops the turns from it on
([fork and rewind](managed-sessions.md#fork-rewind)). With `keepFiles` it
drops the turns and leaves the files as they are, and a turn without a
checkpoint can be rewound too.

<a id="checkpoints"></a>
### checkpoints, checkpointDiff and rewindPreview

pitwall takes a checkpoint of the tab's files when a turn starts, and the
turn's summary in the snapshot names it as `checkpointId`. A tab keeps its
last 20.

```json
{"type": "checkpoints", "id": "15", "sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a"}
```

```json
{"type": "result", "id": "15", "ok": true, "data": {"checkpoints": [{"turnId": "turn-2", "checkpointId": "4f1c9a0d2b7e6f8a1c3d5e7f9a0b2c4d6e8f0a1b", "capturedAt": "2026-10-30T09:10:02.511Z"}]}}
```

`checkpoints` lists the turns whose checkpoint the tab still has, oldest
first. A chat outside a git repository has none.

```json
{"type": "checkpointDiff", "id": "16", "sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a", "turnId": "turn-2"}
```

```json
{"type": "result", "id": "16", "ok": true, "data": {"patch": "diff --git a/README.md b/README.md\nindex 2a9e0f1..7d3b4c8 100644\n--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-before\n+after\n", "files": [{"path": "README.md", "status": "modified"}]}}
```

`checkpointDiff` is what turn `turnId` changed: from its checkpoint to the
next turn's, or to the files now for the last turn, so edits made after the
last turn ends count as its own. `patch` is `git diff` with three lines of
context and `a/`, `b/` prefixes. `files` names each file with `status`
`added`, `deleted`, `modified`, `renamed`, `copied` or `type-changed`, and
`from` for a renamed or copied file's old path. Untracked files count;
ignored ones do not.

```json
{"type": "rewindPreview", "id": "17", "sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a", "turnId": "turn-2"}
```

```json
{"type": "result", "id": "17", "ok": true, "data": {"patch": "diff --git a/README.md b/README.md\nindex 7d3b4c8..2a9e0f1 100644\n--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-after\n+before\n", "files": [{"path": "README.md", "status": "modified"}], "turnPatch": "", "turnFiles": []}}
```

`rewindPreview` is what `rewind` would do to the files now: `patch` and
`files` go from the files now to the turn's checkpoint, and `turnPatch`
and `turnFiles` are the turn's own change, as `checkpointDiff` answers.
It changes nothing and is refused, like `rewind`, while a turn runs
(`busy`). A turn without a checkpoint answers `unsupported`.

### archive and unarchive

```json
{"type": "archive", "id": "14", "sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a"}
```

```json
{"type": "archive", "id": "15", "sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a", "delete": true}
```

```json
{"type": "unarchive", "id": "16", "sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a"}
```

`delete: true` removes an archived chat for good. Archive is refused with
`conflict` for a pinned tab.

<a id="import"></a>
### import

aide moves the sessions of its own store into pitwall with `import`
([ENG-2707](https://linear.app/quantic/issue/ENG-2707)). Each one becomes
an archived chat: `chats/archive/<sessionId>/` with `config.json` and the
snapshot at seq 1 ([storage](managed-sessions.md#storage)). The chat
keeps aide's session id, so aide finds it under the id it already has.

```json
{"type": "import", "id": "21", "sessionId": "5d0e8a4c-7f3b-4b2a-9c1d-2e6f8a0b4c7d", "dir": "/home/me/src/app", "provider": "claude", "model": "claude-opus-5-5", "title": "Fix the login form", "createdAt": "2026-08-30T09:00:00.000Z", "updatedAt": "2026-09-01T10:00:00.000Z", "snapshot": {"sessionId": "5d0e8a4c-7f3b-4b2a-9c1d-2e6f8a0b4c7d", "paneId": "pane-1", "workspaceId": "aide-ws-1", "provider": "claude", "content": [{"id": "u1", "kind": "user-prompt", "text": "Fix the login form", "provider": "claude", "turnId": "t1", "updatedAt": "2026-09-01T10:00:00.000Z"}], "turns": [], "pendingApprovals": [], "pendingUserInputs": [], "planApproval": null, "sessionState": "stopped", "running": false, "detail": null, "lastCompletedAt": null, "lastError": null, "lastErrorAt": null, "model": null, "effort": null, "serviceTier": null, "permissionMode": "default", "interactionMode": "build", "selectionNotice": null, "runtimeGeneration": 1, "revision": 7, "updatedAt": "2026-09-01T10:00:00.000Z"}}
```

```json
{"type": "result", "id": "21", "ok": true, "data": {"imported": true}}
```

- `sessionId`, `dir`, `provider` and `snapshot` are required, and the
  snapshot's `sessionId` must be `sessionId`; otherwise `invalid`.
- Import is idempotent. Sending a session again replaces the copy an
  earlier import made while that chat stays archived, so aide's newer copy
  wins. A live chat with the id, or one pitwall archived itself, stays as
  it is, and the answer is `imported: false`.
- The folder is written beside the archive and renamed into place, so a
  crash leaves the earlier copy or none, never half of one.
- The chat is read-only history: `read` returns it, `listSessions` with
  `archived: true` lists it. A daemon that keeps no chats on disk answers
  `unsupported`.

### listProjects and listWorktrees

A project here is a repo, as aide means it: the tabs that share a
`RepoRoot`. It is not `model.Project`, which is a sidebar group.

```json
{"type": "listProjects", "id": "17"}
```

```json
{"type": "result", "id": "17", "ok": true, "data": {"projects": [{"root": "/home/me/src/pitwall", "name": "pitwall", "workspaces": [{"id": "a1b2c3d4e5f60718", "label": "Fix the flaky resize test", "branch": "fix-resize", "path": "/home/me/src/pitwall/.worktrees/fix-resize", "worktree": true, "pinned": false, "chat": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a"}]}]}}
```

`chat` is the chat id of a chat tab, `""` for a tab of panes. `worktree`
is true when pitwall made the tab's folder as a worktree.

```json
{"type": "listWorktrees", "id": "18", "root": "/home/me/src/pitwall"}
```

```json
{"type": "result", "id": "18", "ok": true, "data": {"worktrees": [{"path": "/home/me/src/pitwall", "branch": "main", "head": "05cf8bd1c0de"}, {"path": "/home/me/src/pitwall/.worktrees/fix-resize", "branch": "fix-resize", "head": "eb7213d2f00d"}]}}
```

### setJev

```json
{"type": "setJev", "id": "14", "apiKey": "ts_live_0123456789abcdef", "disabledRoots": ["/home/me/src/private"], "gates": {"global": {"toolGate": {"enabled": true}}, "workspaces": {"/home/me/src/pitwall": {"toolGate": {"denyAbove": 0.8}}}}}
```

```json
{"type": "result", "id": "14", "ok": true}
```

aide hands over its Jev settings for the chats it starts (ENG-2934): the
TypeSafe key from the OS keyring, the workspace roots where the user
turned TypeSafe processing off, and its global and per-workspace gate
layers. Each `setJev` replaces the last; aide's main process sends one
whenever the settings change and whenever the renderer connects, since
the daemon keeps them in memory only. The key is never written to disk or
a log, and no reply carries it. Only a client whose `hello` says `aide`
may send it (`invalid` otherwise), and the gates apply only to chats an
`aide` client started (and their forks):

- Every turn, whether sent, started with a prompt, steered, taken from
  the queue or sent by pitwall (a limit resume, a restart's continue),
  goes through aide's Jev preparation first (`internal/decide`, ported
  from aide's `context-preparation.ts`). Preparation refuses the turn
  when the workspace is in `disabledRoots` ("Allow TypeSafe processing
  for this workspace in Settings before sending a message."), when there
  is no key ("Add a TypeSafe API key in Settings before sending a
  message."), or when Jev or the workspace context fails. The command
  then fails with code `jev` and that message, and the prompt does not
  join the transcript. Otherwise the provider gets the prompt with the
  workspace evidence Jev chose, and the transcript keeps the prompt as
  typed.
- A queued `send` is checked for consent and a key when queued. One that
  Jev refuses when it comes up stays first in the queue, held, with the
  reason as a `system` line.
- With `toolGate.enabled`, a tool approval (not a plan) stays out of the
  snapshot while Jev judges it. Jev allows it when every probability is
  under `allowBelow`, denies it when any reaches `denyAbove`, and
  otherwise lets it through to the user, as it does when Jev does not
  answer. An allow or deny adds a `system` line. The gate is off unless a
  layer turns it on; the workspace layer wins over the global one.

`laneRouting` and `completionCheck` are accepted and ignored: they judge
aide's configured agents, which a pitwall chat does not have.

### readUsage

What Settings, Usage shows: each plan limit as its agent last reported it,
and the tokens and cost of every agent's session files on this machine,
by day and by session, over `days` days ending today (1 to 90, 30 when
left out). The limits come from pitwall statusline's files and 8 days of
Codex rollouts; the calls from the files `flow.Sources` names. Reading
them can take seconds, so the result may come after later commands'.

```json
{"type": "readUsage", "id": "19", "days": 2}
```

```json
{"type": "result", "id": "19", "ok": true, "data": {"usage": {"showCost": false, "pricesAsOf": "2026-10-22", "limits": [{"provider": "claude", "profile": "", "profileLabel": "", "name": "", "seen": "2026-10-31T09:12:00Z", "windows": [{"minutes": 300, "used": 42, "resets": "2026-10-31T12:00:00Z"}, {"minutes": 10080, "used": 18.5}]}], "days": [{"date": "2026-10-30", "tokens": 1200000, "cost": 3.1}, {"date": "2026-10-31", "tokens": 450000, "cost": 0.9}], "sessions": [{"provider": "claude", "sessionId": "5f0e9c2a-1b7d-4e63-a0c8-9d2f4b6e1a37", "last": "2026-10-31T09:10:00Z", "tokens": 900000, "cost": 2.4}], "total": {"sessions": 3, "tokens": 1650000, "cost": 4.0, "unpriced": 0}}}}
```

`used` is a percent as the agent reported it, so a window whose `resets`
has passed is unused now; `resets` is left out when unknown. `sessions`
are the 10 with the most tokens, by the agent's own session id. Costs are
at the API's list prices of `pricesAsOf`, count priced models only, and
are sent either way: `showCost` is `[usage] show_cost`, and a client shows
them only when it is on. A server without the scan leaves `readUsage` out
of `capabilities`.

<a id="pull-requests"></a>
## Pull requests

pitwall already polls the pull request of every tab's branch (`forge.View`,
backing off as `forge.Next` says). Each chat's list entry carries its
tab's as `pr`, so a client sees checks and reviews change through
`sessionsChanged` and never calls GitHub itself:

```json
{"type": "sessionsChanged", "changed": [{"id": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a", "workspaceId": "a1b2c3d4e5f60718", "provider": "claude", "model": "", "effort": "", "mode": "default", "title": "Fix the flaky resize test", "state": "ready", "pending": 0, "queued": 0, "turns": 4, "seq": 498, "updatedAt": "2026-10-30T09:31:02.418Z", "pr": {"number": 241, "state": "open", "draft": false, "url": "https://github.com/quanticstudios/pitwall/pull/241", "review": "changes", "checks": [{"name": "linux", "state": "fail"}, {"name": "other (macos-latest)", "state": "pass"}], "conflicts": false, "head": "eb7213d2f00d4e1c", "reason": "checks_failing", "detail": "linux failing"}}], "removed": []}
```

`state` is `open`, `merged` or `closed`; `review` is `""`, `required`,
`approved` or `changes`; a check's `state` is `pending`, `pass`, `fail`,
`skip` or `""`; `reason` and `detail` are why it is not ready to merge,
as the sidebar's PR row says it. Tabs on one branch in one folder share
one pull request.

The commands below name the chat; pitwall acts on its tab's pull request,
and answers `not-found` when the tab's branch has none. They call `gh`, so
they can take seconds, and later commands on the connection do not wait
for them. A failed `gh` answers `forge`.

### reviewThreads

```json
{"type": "reviewThreads", "id": "21", "sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a"}
```

```json
{"type": "result", "id": "21", "ok": true, "data": {"threads": [{"path": "internal/pane/pane_test.go", "line": 88, "comments": [{"author": "octocat", "body": "This sleep is the flake."}], "more": 0}], "changeRequests": [{"author": "octocat", "body": "Wait on the resize event instead."}]}}
```

`threads` are the unresolved review threads; `line` is the line on the
branch's version of the file, the line it was on when GitHub lost it, or 0
for a comment on the whole file. `more` counts the comments past the first
50. `changeRequests` are the standing change requests that have text.

### prPrompt

```json
{"type": "prPrompt", "id": "22", "sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a", "what": "failures"}
```

```json
{"type": "result", "id": "22", "ok": true, "data": {"text": "These checks failed on pull request #241:\n- linux: https://github.com/quanticstudios/pitwall/actions/runs/1/job/2 (no log)\n\nFix these and push."}}
```

`what` is `failures` (Send failures to agent) or `review` (Send review
comments to agent). `text` is the message those send, capped at 30 KiB
(`forge.MaxPrompt`), and `""` when there is nothing to send. The client
sends it with `send`, or starts a chat with it as `prompt`; pitwall's
`[git] send_failures_on_fail` still only reaches agent panes.

### mergePR

```json
{"type": "mergePR", "id": "23", "sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a"}
```

```json
{"type": "result", "id": "23", "ok": true}
```

It runs `gh pr merge <number> --<[git] merge_method>` in the tab's folder
and polls the pull request again at once, so the list shows it merged. It
answers `conflict` when the pull request is not open. Confirming is the
client's, as pitwall's merge dialog is.

<a id="results"></a>
## Results and errors

A failed command gets `ok: false` with a `code` and a `message` for a
person. The codes are the chat loop's ([errors](managed-sessions.md#driver)):

| Code | Means |
| --- | --- |
| `not-found` | no such chat, request or turn |
| `conflict` | someone answered first, the chat is archived, the tab is pinned |
| `busy` | the chat cannot take this now, for example `rewind` while a turn runs |
| `unsupported` | this provider cannot, or this server does not know the command |
| `provider` | the provider refused; `message` has its words |
| `invalid` | the command is malformed: a missing field, a bad value, too many attachments |
| `forge` | `gh` failed; `message` has its words |
| `jev` | a Jev gate refused the turn: no consent for the workspace, no key, or preparation failed; `message` is aide's |

A client treats a code it does not know as a failure with `message` to
show.

`error` is for a frame the server cannot tie to a command: not JSON, or no
`id`. The connection stays open.

```json
{"type": "error", "code": "invalid", "message": "frame is not a JSON object"}
```

<a id="transport"></a>
## aide's SessionTransport

aide's renderer calls the daemon through `SessionTransport`
(`src/renderer/src/lib/session-transport.ts`, ENG-2699). ENG-2700 writes a
WebSocket implementation of it. This is the mapping, member by member.

| SessionTransport | Protocol | Notes |
| --- | --- | --- |
| `listSessions(query?)` | `listSessions` | aide filters by `query` itself. `TaskLibrarySnapshot`'s notification settings stay aide's. `projectName`, `workspaceName` and `branchName` come from joining `workspaceId` with `listProjects`. `pinned`, `unread`, `excerpt` and `readActivityKey` have no pitwall field in v1. |
| `subscribeSessionList` | `subscribeSessions`, `unsubscribeSessions` | the listener runs on `sessions` and `sessionsChanged` |
| `updateSession(id, {archived})` | `archive`, `unarchive` | `title`, `pinned` and `readActivityKey` have no command in v1 |
| `subscribeSession` | `subscribe`, `unsubscribe` | `deltas` and `removed` become `ManagedSessionUpdate`s |
| `getSessions` | `listSessions`, then `read` per chat | |
| `getSavedSession` | `read` with `after: 0` | works on archived chats |
| `start` | `start` | renamed: `cwd` is `dir`, `mode` is `provider`. pitwall picks `sessionId`, `paneId` and `workspaceId` and returns them. Dropped: `agents`, `jevGates`, `orchestratorSelection`, `contextWindow`, `thinkingBudget`, `readOnly`, `serviceTier`, `selectionNotice`, `interactionMode` (see `setMode`). Added: `worktree`, `base`, `branch`, `name`, `prompt`. |
| `send` | `send` | `recipients` and `selectedSkill` are dropped; steer and queue are `how` |
| `interrupt` | `interrupt` | `recipientAlias` is dropped |
| `resolveApproval` | `resolveApproval` | `decision` renamed: `accept` is `allow`, `accept-for-session` is `allow-session`, `deny` is `deny` |
| `resolveInput` | `resolveInput` | a string answer is sent as a one-item list |
| `setModel` | `set` with `model`, `effort` | a different provider (`mode`) is `set` with `provider`; `serviceTier` is dropped |
| `setPermissionMode` | `set` with `permissionMode` | |
| `setMode` | `set` with `permissionMode` | `plan` is `permissionMode: "plan"`, `build` is `permissionMode: "default"` |
| `fork(sessionId, responseContentItemId)` | `fork` | pitwall forks at a turn: send the item's `turnId` |
| `previewRewind` | `rewindPreview` | pitwall keeps no preview: aide holds it, and `rewind` takes the turn |
| `rewind(previewId, mode)` | `rewind` | `conversation-only` is `keepFiles: true`. The chat is rewound in place, not copied to a new one |
| `getCheckpointDiff` | `checkpointDiff` | aide splits `patch` per file |
| `close` | `archive` | closing a chat tab archives the chat in pitwall |
| Jev settings (`updateJevSettings`, `updateJevGates`) | `setJev`, from aide's main process | the renderer never sees the key |
| `setAgents`, `acceptFleetNode` | none | fleet and multi-agent routing stay aide's ([what stays out](managed-sessions.md#out)) |

<a id="versioning"></a>
## Versioning

Three things carry the version:

- `protocol` (now 1) is this document. `hello` and `welcome` carry it.
- `deltaVersion` (now 1) is aide's `SESSION_DELTA_VERSION`, in every delta.
- `capabilities` lists the commands a server accepts.

A change that every older peer handles needs no bump:

- a new optional key in any frame (both sides ignore keys they do not know);
- a new server message `type` (clients ignore types they do not know);
- a new command, listed in `capabilities` (a client checks before it sends);
- a new value of `code`, `how` or `decision`, or a new key in `data`;
- a new optional snapshot key, as aide adds them.

Each of these updates `web-protocol.schema.json` in the same change. A
peer at the same `protocol` but older than the schema still works, since
it ignores what it does not know.

Bump `protocol` when an older peer would misread the change:

- a key, message, command or value renamed or removed;
- a key's type or meaning changed, or an optional key made required;
- a default changed;
- `deltaVersion` bumped, which aide does for any delta change an older
  consumer would misapply (a new delta type included).

A bump changes `protocol` in pitwall and aide together. The server accepts
one `protocol` and closes any other with 4002, so a mismatch shows as a
plain message, never as a half-working session. A server may keep
accepting the previous `protocol` for a release while aide catches up; it
then says so in `welcome`'s `protocol`, which is always the one it will
speak on this connection.

<a id="open"></a>
## Left open

1. The queue's contents. The socket's `ChatQueue` reorders and edits
   queued messages, but neither the snapshot nor `model.Chat` (only
   `queued`, a count) shows them, so the web has no `queue` command yet.
   It needs a snapshot key for the queue first, which is additive.
2. Rename, pin and read state of a chat (`updateSession`'s `title`,
   `pinned`, `readActivityKey`): additive commands once aide needs them
   over the web.
3. Which pitwall session and group `start` puts a tab in: the last used
   one for now; `session` and `group` keys are additive.
4. The phone: it keeps its device tokens and its own HTTP pages
   ([phone.md](phone.md)). The full web client on a phone authenticates
   through a device token, which buys a session and its own `hello` token
   ([Web client](remote.md#web-client)).
5. What the chat loop cannot do yet answers `unsupported`, and the
   commands without a socket message are left out of `capabilities`:
   `unarchive`. `archive` with
   `delete: true` archives a live chat as plain `archive` does, as
   `ChatArchive` does on the socket.
6. A page the daemon serves gets its `hello` token from
   `/_pitwall/bootstrap` with a session cookie, never from `web.json` or a
   URL ([Web client](remote.md#web-client)).
