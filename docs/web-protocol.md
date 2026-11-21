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
- [Control](#control)
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
{"type": "welcome", "protocol": 1, "server": {"name": "pitwall", "version": "0.31.0"}, "deltaVersion": 1, "capabilities": ["subscribeSessions", "unsubscribeSessions", "listSessions", "subscribe", "unsubscribe", "read", "start", "send", "interrupt", "resolveApproval", "resolveInput", "set", "fork", "rewind", "archive", "unarchive", "rename", "pin", "import", "listProjects", "listWorktrees", "checkpoints", "checkpointDiff", "rewindPreview", "setJev", "reviewThreads", "prPrompt", "mergePR"]}
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

<a id="local-socket"></a>
### The local socket

On Linux the daemon's own Unix socket, `$PITWALL_SOCKET` in every pane,
speaks this protocol too, as JSON lines. The daemon reads the first byte
of each connection without taking it: `{` starts a JSON client, and
anything else is one of pitwall's own clients, as before. macOS and
Windows serve only pitwall's own clients there for now.

- Each frame is one JSON object on one line, ended by `\n`, both ways.
  The server never puts a line break inside a frame. Blank lines are
  skipped. A line over 16 MiB ends the connection with 1009.
- `hello` needs no `token`, and one sent is ignored. Whoever can open the
  socket is trusted, as pitwall's own commands are: the socket's folder
  belongs to the user with mode 0700, and the daemon refuses a folder that
  other users can open.
- The first byte must come within 10 s, and `hello` within 10 s after it.
- Where WebSocket sends a close frame, the socket sends a `close` line
  with the same code and reason, then closes:

```json
{"type": "close", "code": 4002, "reason": "pitwall speaks protocol 1, client sent 2: update aide or pitwall"}
```

- It serves no chats and no pull request commands. `welcome` lists the
  [control](#control) commands, and every other command gets
  `unsupported`. Neither TCP listener serves the control commands.
- A daemon from before the JSON socket closes the connection on the first
  line, so a client that reads EOF right after its `hello` has an older
  pitwall.

A minimal session, with `socat`:

```sh
printf '%s\n' '{"type":"hello","protocol":1,"client":{"name":"script"}}' |
  socat - UNIX-CONNECT:"$PITWALL_SOCKET"
```

To reach another machine's daemon, forward its socket to a socket in a
folder only you can open, never to a TCP port, which any local user could
connect to. `pitwall remote-start` on that machine prints the socket's
path there as the last field of its line:

```sh
mkdir -m 700 -p ~/.pitwall-hosts
ssh -fN -L ~/.pitwall-hosts/box.sock:/run/user/1000/pitwall/pitwall.sock box
```

OpenSSH creates the local socket with mode 0600 (`StreamLocalBindMask`
0177).

<a id="frames"></a>
## Frames

- One WebSocket text frame holds one JSON object. Every object has `type`.
- The client sends `hello` and then [commands](#commands). Every command
  has an `id`, a string the client picks and does not reuse on that
  connection. The server answers each command with exactly one `result`
  that carries the same `id`.
- The server sends `welcome`, `result`, `error`, and the pushed messages
  `sessions`, `sessionsChanged`, `deltas`, `removed` and `tabs`.
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
| `title` | the tab's title: the first prompt until someone [renames](#rename) the chat or its tab |
| `pinned` | `true` when the chat is [pinned](#rename) in aide's task history; left out otherwise |
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

`result`, `error` and `removed` messages are kept while the connection is
open. At 64 queued frames or 8 MiB the server stops reading commands
until fewer than half are left. Before queuing a reply it also waits for
space for that reply, so the next result cannot push the queue past the
limit. Asynchronous chat removals cannot wait on the daemon; if one
cannot fit, the connection closes with 1008 and the client resyncs.

On the [local socket](#local-socket) a single result over 4 MiB is sent
as `tooLarge` instead. If the request ID makes even that error result
exceed 4 MiB, the connection closes with 1009. WebSocket keeps its large
chat replies: one reply may exceed 8 MiB when no other result, error or
removal is queued. Further replies wait for it to leave the queue.
The server does not merge deltas per client; that
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
| `rename` | `ChatRename` | none |
| `pin` | `ChatPin` | none |
| `import` | none: it writes an archived chat's folder | `imported` |
| `listProjects` | the daemon's `State` | `projects` |
| `listWorktrees` | `gitstat.ListWorktrees` | `worktrees` |
| `setJev` | none: deprecated, stores nothing | none |
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

`skill` (here and in `send`) is a provider skill aide resolved for the
message: `provider`, aide's `id`, `name` (the native name) and, for
Codex, the skill's `path`. Claude gets `/name` on a line ahead of the
text, unless the text starts with it. Codex gets a `skill` input item for
`path` ahead of the text, and cannot steer with one, so the send queues.
The fake providers answer `Handled with <provider> skill <id>: <text>`.
It refuses, with `invalid`, a skill with no `id` or `name`, and one whose
`provider` is not the chat's, or the provider a held switch (`set`) moves
the chat to. A skill for a held switch's provider queues for that switch
instead of steering the running turn. A queued message keeps its skill.

```json
{"type": "send", "id": "5a", "sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a", "text": "prepare the release", "skill": {"provider": "codex", "id": "codex:skill:eafb4a4ea8096486", "name": "release", "path": "/home/me/.codex/skills/release/SKILL.md"}}
```

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

A pane chat's snapshot also carries `live`: items its agent's terminal
shows before the transcript has them, drawn after `content`. They are the
prompt the chat sent, the reply as it streams, and Antigravity's thought
and running command. A `live` delta carries the whole list each time it
changes, and an empty one once the transcript's own items replace it. A
client that does not draw them can ignore both.

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

`decision` is `allow`, `allow-session` or `deny`; `allow-session` fails on an approval with `noSession`, a TUI prompt that has no such answer. `answers` maps a
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
last 24,000 characters, as JSON after the message. A switch while a
turn runs or waits on an answer is held, as aide holds it: the snapshot's
`pendingProviderSelection` gets `mode`, `model` and `effort`, a later `set`
with `provider` replaces or drops it, and it runs once the turn ends or
with the next message. A provider that does not start answers `provider`
and leaves the chat on the one it had; a held switch that does not start
leaves an error in the snapshot.

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
checkpoint can be rewound too. A rewind of the files is refused while
another agent works in the same repository, and while files changed
after the agent's last turn; `"stash": true` keeps those changes in the
backup commit the rewind takes first, which its `system` item names.

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

`unarchive` brings an archived chat back as a live chat, as aide's Restore
does and as aide does when it reopens a closed Agent pane. Its folder moves
back from `chats/archive/` and a new tab opens in the chat's folder; the
answer carries the chat's `sessionId`, unchanged, and that tab's
`workspaceId`. The chat keeps its transcript and its provider session id,
so the next message that needs the provider resumes the session it had, as
after a daemon restart. An [imported](#import) chat unarchives too and is
pitwall's from then on: a later `import` with its id leaves it alone. A
live chat stays where it is and answers with its tab, so two clients that
both reopen a chat agree. An id with no archived chat answers `not-found`,
and a daemon that keeps no chats on disk `unsupported`. A chat archived a
moment ago may still be closing; `unarchive` waits for it.

<a id="rename"></a>
### rename and pin

```json
{"type": "rename", "id": "18", "sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a", "title": "Daily review"}
```

```json
{"type": "pin", "id": "19", "sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a", "pinned": true}
```

`rename` names the chat's tab, as Rename tab does, and the chat's `title`
follows it. An empty `title` goes back to the first prompt. A name another
tab of the same session has answers `conflict`.

`pin` marks the chat in aide's task history; `pinned: false` unmarks it.
It is the chat's own flag, kept in its `config.json`, and is not the tab's
pin: a pinned chat still archives, and the archived list keeps `pinned`
and the title. Both answer `not-found` for an archived chat. Each changes
the chat's entry in `sessionsChanged`.

<a id="import"></a>
### import

aide moves the sessions of its own store into pitwall with `import`
([ENG-2707](https://linear.app/quantic/issue/ENG-2707)). Each one becomes
an archived chat: `chats/archive/<sessionId>/` with `config.json` and the
snapshot at seq 1 ([storage](managed-sessions.md#storage)). The chat
keeps aide's session id, so aide finds it under the id it already has.

```json
{"type": "import", "id": "21", "sessionId": "5d0e8a4c-7f3b-4b2a-9c1d-2e6f8a0b4c7d", "dir": "/home/me/src/app", "provider": "claude", "model": "claude-opus-5-5", "title": "Fix the login form", "pinned": true, "createdAt": "2026-08-30T09:00:00.000Z", "updatedAt": "2026-09-01T10:00:00.000Z", "snapshot": {"sessionId": "5d0e8a4c-7f3b-4b2a-9c1d-2e6f8a0b4c7d", "paneId": "pane-1", "workspaceId": "aide-ws-1", "provider": "claude", "content": [{"id": "u1", "kind": "user-prompt", "text": "Fix the login form", "provider": "claude", "turnId": "t1", "updatedAt": "2026-09-01T10:00:00.000Z"}], "turns": [], "pendingApprovals": [], "pendingUserInputs": [], "planApproval": null, "sessionState": "stopped", "running": false, "detail": null, "lastCompletedAt": null, "lastError": null, "lastErrorAt": null, "model": null, "effort": null, "serviceTier": null, "permissionMode": "default", "interactionMode": "build", "selectionNotice": null, "runtimeGeneration": 1, "revision": 7, "updatedAt": "2026-09-01T10:00:00.000Z"}}
```

```json
{"type": "result", "id": "21", "ok": true, "data": {"imported": true}}
```

- `sessionId`, `dir`, `provider` and `snapshot` are required, and the
  snapshot's `sessionId` must be `sessionId`; otherwise `invalid`.
- `pinned` keeps aide's pin, since [`pin`](#rename) takes only a
  live chat.
- Import is idempotent. Sending a session again replaces the copy an
  earlier import made while that chat stays archived, so aide's newer copy
  wins. A live chat with the id, or one pitwall archived itself, stays as
  it is, and the answer is `imported: false`.
- The folder is written beside the archive and renamed into place, so a
  crash leaves the earlier copy or none, never half of one.
- The chat is archived: `read` returns it, `listSessions` with
  `archived: true` lists it, and [`unarchive`](#archive-and-unarchive)
  makes it live. A daemon that keeps no chats on disk answers
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

Deprecated. It stays so that aide versions that call it keep working, and it
does nothing: the server ignores the key, consent list and gate settings, keeps
none of them, and answers `ok`. Chats start and approvals reach the user the
same with or without it. Any client may send it.

```json
{"type": "setJev", "id": "14", "apiKey": "ts_live_0123456789abcdef", "disabledRoots": ["/home/me/src/private"], "gates": {"global": {"toolGate": {"enabled": true}}, "workspaces": {"/home/me/src/pitwall": {"toolGate": {"denyAbove": 0.8}}}}}
```

```json
{"type": "result", "id": "14", "ok": true}
```

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

pitwall already reads pull requests in batches by GitHub remote
(`forge.Poller.Poll`, backing off as `forge.Next` and the REST budget say). Each chat's list entry carries its
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

<a id="control"></a>
## Control

On the [local socket](#local-socket) only, programs drive pitwall's tabs
as its commands for agents do ([agent-skill.md](agent-skill.md)). Each
command does what the CLI command in the table does, with the CLI's own
code, and its result's `data` holds what that command prints.

| Command | CLI | Data on success |
| --- | --- | --- |
| `listTabs` | `pitwall ls --json` | `session`, `tabs` |
| `subscribeTabs`, `unsubscribeTabs` | none | none; `tabs` frames follow |
| `readPane` | `pitwall read` | `text` |
| `scrollback` | `pitwall scrollback` | `text` |
| `waitPane` | `pitwall wait` | `outcome`, and `state`, `question` or `exit` |
| `newTab` | `pitwall new` | `tab`, `pane`, or `tab` and `closed` |
| `splitPane` | `pitwall split` | `pane` |
| `sendPane` | `pitwall send` | none |
| `promptPane` | `pitwall prompt` | none, or with `wait` `outcome` and `reply` or `question` |
| `killTab`, `renameTab`, `detachTab` | `pitwall kill`, `rename`, `detach` | none |
| `focusTab` | `pitwall attach`, without opening a window | none |
| `tabNew`, `tabClose` | `pitwall tab new`, `tab close` in that pane | `tab`, none |
| `setStatus` | `pitwall status` | none |
| `worktreeNew` | `pitwall worktree new` in the repo | `tab`, `pane` |
| `queueAdd`, `queueDrop` | `pitwall queue add`, `queue rm` | none |
| `queueList` | `pitwall queue ls` | `session`, `tasks` |
| `hostCheck` | the check `pitwall new --host` asks a host for | `problems` |
| `adopt` | `pitwall adopt` run in `tab` | `adopted` |

These commands act on this machine only. A request that would start or
change a worker on another host answers `remote` and names the host, so
nothing starts there; use the CLI, or that host's socket.

A program outside a pane has no pane or session of its own, so a request
names them:

- `tab` is a tab's address as `pitwall kill` takes it, the same as
  `pane` without `TAB.N`; `@` and a tab id from `listTabs` works too.
- `pane` is an address as `pitwall read` takes it: `#N` or `N` for a tab's
  main pane, `TAB.N` for its Nth pane, a title or a unique title prefix,
  or `@` and a pane id from `listTabs`, which stays valid while tabs move.
- `session` is a session's id, its exact name or a unique prefix of its
  name. While more than one session is open, an address other than `@` and
  an id needs it, so `#1` cannot reach a tab in another session than the
  one the program meant. `listTabs` and `subscribeTabs` without it take
  the most recently used session, and their data names it.
- A key the command does not take is `invalid`.

A worker that a tab started on another host (`pitwall new --host`) shows
in `listTabs` as a stale row: `stale: true`, `state: "unknown"`, its
`host`, `addr` `@` and its id, and no panes. This daemon records the
worker but not its state. For its state, forward that host's socket as
in [The local socket](#local-socket) and ask that daemon. An address of
such a worker, or `box:3` for a host's tab, answers `remote` and names
the host.

### listTabs

`tabs` are the rows of `pitwall ls --json`, the same fields with the same
meanings ([agent-skill.md](agent-skill.md)). `children` (a pane id) lists
only the tabs that pane's tab opened, as `ls --children` run in that pane
does; `orphans` lists workers whose parent tab is gone, as
`ls --orphans`. Rows never carry a command line or an environment.

```json
{"type": "listTabs", "id": "c1", "session": "work"}
```

```json
{"type": "result", "id": "c1", "ok": true, "data": {"session": {"id": "4f1c2a9e", "name": "work"}, "tabs": [{"n": 1, "id": "a3f2", "title": "fix-login", "group": "pitwall", "cwd": "/home/me/src/pitwall", "branch": "fix-login", "agent": "claude", "state": "working", "question": "", "exit_code": null, "panes": 1, "detached": false, "pane_list": [{"n": 1, "id": "p7c1", "agent": "claude", "state": "working", "question": "", "exit_code": null, "queued": 0, "since": "2026-11-15T09:12:00Z", "repeats": 0, "repeating": ""}], "parent": "", "since": "2026-11-15T09:12:00Z", "pr_state": "", "quiet_since": null, "repeats": 0, "repeating": "", "status": "", "host": "", "addr": "#1", "stale": false}, {"n": 0, "id": "b9e0", "title": "", "group": "", "cwd": "", "branch": "", "agent": "", "state": "unknown", "question": "", "exit_code": null, "panes": 0, "detached": false, "pane_list": [], "parent": "a3f2", "since": null, "pr_state": "", "quiet_since": null, "repeats": 0, "repeating": "", "status": "", "host": "box", "addr": "@b9e0", "stale": true}]}}
```

### subscribeTabs and unsubscribeTabs

After its result, `subscribeTabs` pushes a `tabs` frame with the
session's tabs as `listTabs` gives them, then another each time they
change. Changes close together come as one frame, and a client that
reads slowly gets the latest list rather than every one. A second
`subscribeTabs` replaces the first; `unsubscribeTabs` stops them.

```json
{"type": "subscribeTabs", "id": "c2", "session": "work"}
```

```json
{"type": "tabs", "session": {"id": "4f1c2a9e", "name": "work"}, "tabs": []}
```

```json
{"type": "unsubscribeTabs", "id": "c3"}
```

### readPane and scrollback

`readPane` is the reply the pane's agent last finished, or with `screen`
the text on the pane's screen. `scrollback` is the pane's history and
screen; `lines` keeps only the last lines, at most 10,000. A text over
4 MiB is `tooLarge`: ask for fewer `lines`.

```json
{"type": "readPane", "id": "c4", "session": "work", "pane": "#1", "screen": true}
```

```json
{"type": "result", "id": "c4", "ok": true, "data": {"text": "$ go test ./...\nok  \tgithub.com/quanticstudios/pitwall/internal/web\t1.2s"}}
```

```json
{"type": "scrollback", "id": "c5", "pane": "@p7c1", "lines": 200}
```

### waitPane

`waitPane` answers once the pane gets to `until`, as `pitwall wait` does:
`done` (its agent finished a turn), `idle`, `blocked` or `exit`. It
remembers what it saw while it waits, so `done` after an interrupted turn
that ended idle still counts. `outcome` says why it ended:

| `outcome` | Means | Also in `data` |
| --- | --- | --- |
| `reached` | the pane got to `until` | `state` |
| `blocked` | its agent waits on a person | `question` |
| `exited` | its process ended | `exit`: `code`, `unknown`, `signal` |
| `closed` | the pane closed without an exit code, on purpose | none |

`exit.code` is the program's own, so a program that exits 2 is `exited`
with `code` 2. `unknown` means the daemon restarted while it ran;
`signal` that a signal ended it, with a negative `code`. With
`timeoutMs`, a wait that has not ended by then answers `timeout`. It is
0 for no timer, or at most 9223372036854 milliseconds. Up to
64 waits can be pending on one connection; more answer `busy`. A client
that closes its sending side gets no result of a pending wait.

```json
{"type": "waitPane", "id": "c6", "session": "work", "pane": "#2", "until": "exit", "timeoutMs": 600000}
```

```json
{"type": "result", "id": "c6", "ok": true, "data": {"outcome": "exited", "exit": {"code": 3, "unknown": false, "signal": false}}}
```

```json
{"type": "result", "id": "c7", "ok": false, "code": "timeout", "message": "timed out after 10m0s"}
```

### newTab

`newTab` opens a tab in `dir`, an absolute path, running `cmd`, a
preset's agent before `cmd`, or a shell, as `pitwall new` does with
`-n` (`name`), `-d` (`detached`), `--preset`, `--model` and `--guard`.
The daemon finds the program on its own `PATH`. `parentPane`, a pane id,
is the agent that asks for the tab, as `$PITWALL_PANE` is for the CLI:
its tab becomes the new tab's parent, and with `[agents] guard_workers`
on, an agent it starts runs under the guard.

`startId` is 16 hex digits the client picks, so a request it retries
after a dropped connection, a timeout or a daemon handoff opens one tab:
a retry of a tab that is open answers that tab, and one of a tab that
closed since answers `closed: true`. The tab gets it as its id, so
`@` and it addresses the tab. Without `startId` the daemon picks one.
Successful start identities persist separately from the reopen archive,
so pruning that archive cannot make a retry open another tab. The history
holds at most 65,536 identities. At capacity, a new start drops one whose
tab is neither open nor in the reopen archive. Keep the daemon's saved
state to preserve this guarantee across restarts and handoffs.

The guard follows the CLI: `guard`, or a preset with `guard = true`,
starts the agent with `PITWALL_GUARD=1` once the daemon has checked
that its hooks deny a `git push`. A start the guard cannot cover is
`refused` with the reason and starts nothing.

```json
{"type": "newTab", "id": "c8", "session": "work", "dir": "/home/me/src/pitwall", "cmd": ["claude"], "name": "fix-login", "guard": true, "startId": "9f2c4e1a7b3d5c60"}
```

```json
{"type": "result", "id": "c8", "ok": true, "data": {"tab": "9f2c4e1a7b3d5c60", "pane": "p7c1"}}
```

```json
{"type": "result", "id": "c8", "ok": false, "code": "refused", "message": "guarded tab not started: no settings.json, so pitwall's hooks are not installed in /home/me/.claude; run CLAUDE_CONFIG_DIR=/home/me/.claude pitwall hooks install"}
```

### splitPane, tabNew and the guard

`splitPane` opens a pane beside the active split's last pane of `tab`,
or below it with `down`, running `cmd` or a shell. `tabNew` opens a
shell tab after the tab of `pane`, as `pitwall tab new` run there does.

A tab is guarded when one of its panes started with the guard. There a
split gets `PITWALL_GUARD=1` and the same check as `newTab`, so a split
of an agent the guard covers runs under it, and a split of a shell or
another program is `refused`. `tabNew` from a guarded tab is `refused`,
since its shell is out of the guard's reach.

```json
{"type": "splitPane", "id": "c9", "session": "work", "tab": "#1", "down": true, "cmd": ["make", "test"]}
```

```json
{"type": "result", "id": "c9", "ok": true, "data": {"pane": "p8d2"}}
```

```json
{"type": "tabNew", "id": "c10", "pane": "@p7c1"}
```

### sendPane and promptPane

`sendPane` queues `text` for the pane's agent, as `pitwall send` does;
the daemon types it once the agent sits at its prompt. `fromPane` is the
sending agent's pane id, which the message's header names, and which
may send one pane at most 20 messages until the user types there.
JSON sends also hold at most 20 queued messages per pane across all
connections, including sends without `fromPane`; further sends answer
`busy` until the queue drains.

`promptPane` waits for the agent's turn to end, then types `text`, as
`pitwall prompt` does. Without `wait` it answers once it typed. With
`wait` it answers once the turn the prompt started ends: `outcome`
`reached` with `reply`, the turn's last message, or `blocked` with the
agent's `question`. A blocked agent before the prompt answers `blocked`
too, and nothing is typed. A pane that exits answers as `waitPane`
does. A turn that has not started 30 seconds after the prompt answers
`timeout`, as does the whole of it past `timeoutMs`. Pending prompts
count toward the 64 waits a connection may have.

```json
{"type": "sendPane", "id": "c11", "pane": "@p7c1", "text": "the build is green", "fromPane": "p8d2"}
```

```json
{"type": "promptPane", "id": "c12", "pane": "@p7c1", "text": "run the tests", "wait": true, "timeoutMs": 1800000}
```

```json
{"type": "result", "id": "c12", "ok": true, "data": {"outcome": "reached", "state": "done", "reply": "All 214 tests pass."}}
```

### killTab, renameTab, detachTab and focusTab

These do what `pitwall kill`, `rename` and `detach` do to `tab`.
`killTab` asks nothing first: a pinned tab is `conflict`. `focusTab`
brings a detached tab back and asks the windows showing its session to
show it, as `pitwall attach` does without opening a window.

```json
{"type": "renameTab", "id": "c13", "session": "work", "tab": "#1", "title": "fix-login"}
```

```json
{"type": "killTab", "id": "c14", "tab": "@9f2c4e1a7b3d5c60"}
```

```json
{"type": "detachTab", "id": "c15", "tab": "@9f2c4e1a7b3d5c60"}
```

```json
{"type": "focusTab", "id": "c16", "tab": "@9f2c4e1a7b3d5c60"}
```

```json
{"type": "tabClose", "id": "c17", "pane": "@p8d2"}
```

### setStatus

With `text`, `setStatus` sets the line under the row of the pane's tab;
an empty or missing `text` clears it. With `state`, it reports the
pane's program state instead, as `pitwall status <state>` run in the
pane does, with `app`, `progress` (working or blocked only) and
`message` ([Programs and scripts](agent-report.md#programs-and-scripts)). It stays until another report replaces or clears it, or the pane exits.

```json
{"type": "setStatus", "id": "c18", "pane": "@p8d2", "text": "deploying to staging"}
```

```json
{"type": "setStatus", "id": "c19", "pane": "@p8d2", "state": "working", "app": "deploy", "progress": 40}
```

### worktreeNew

`worktreeNew` opens a tab in a new worktree, `<repo>/.worktrees/<name>`,
as `pitwall worktree new <name>` run in `repo` does: `repo` is an
absolute path in the repository. `from`, `branch` and `pr` pick what it
checks out, as `--from`, `--branch` and `--pr`; with none, a new branch
`name` off the default branch. `cmd` runs in it, or a `preset`'s agent,
or with `lane` (fleet mode) the lane's preset with the words of `cmd` as
one brief; without them the tab opens empty. A lane whose workers start
on another host is `remote`. `guard` and `parentPane` work as for
`newTab`, and the tab joins the group of `parentPane`'s tab; without
`parentPane` it joins the repository's group in `session`, which the
daemon makes when missing.

A worktree that opened but whose setup had a problem answers `ok` with
the problem in `message`, as the CLI prints `#N` and then the problem.

```json
{"type": "worktreeNew", "id": "c20", "repo": "/home/me/src/pitwall", "name": "fix-login", "parentPane": "p7c1", "preset": "safe", "cmd": ["fix the login loop"]}
```

```json
{"type": "result", "id": "c20", "ok": true, "data": {"tab": "5d0e8a2c9b1f7a34", "pane": "p9a3"}}
```

### queueAdd, queueList and queueDrop

`queueAdd` queues `cmd`, or a `preset`'s agent with `cmd` after it, to
start in `dir` once the session's queue has room, as `pitwall queue add`
does with `--preset`, `--model`, `--at`, `--every` and `--worktree`.
`dir` is an absolute path. `queueList` lists the session's queued tasks,
first to start first, each with its `title` (its prompt's first line)
but never its command line or environment. `queueDrop` takes `task` off
the queue: an `id` from `queueList`, or its `n`, which needs `session`
while more than one session is open.

```json
{"type": "queueAdd", "id": "c21", "session": "work", "dir": "/home/me/src/pitwall", "cmd": ["codex", "fix the flaky test"], "at": "02:00"}
```

```json
{"type": "queueList", "id": "c22", "session": "work"}
```

```json
{"type": "result", "id": "c22", "ok": true, "data": {"session": {"id": "4f1c2a9e", "name": "work"}, "tasks": [{"n": 1, "id": "7b2e", "title": "fix the flaky test", "dir": "/home/me/src/pitwall", "group": "pitwall", "worktree": "", "freshWorktree": false, "branch": "", "nextStart": "2026-11-16T02:00:00Z", "repeatMs": 0}]}}
```

```json
{"type": "queueDrop", "id": "c23", "task": "7b2e"}
```

### hostCheck

`hostCheck` answers what would stop a worker with these fields from
starting on this machine: `cmd` or a `preset`'s agent on the daemon's
`PATH`, its hooks and login, with `guard` the guard's check, and with
`repo` a work tree whose origin git and `gh` can reach. It starts
nothing. `dir` and `repo` are absolute or start with `~/`, the daemon's
home folder. `clone` is origin's URL for the fix of a missing `repo`.
Each problem has `what`, a `fix` and `warn`: with `warn` the worker
would start without it. Raw git, SSH and `gh` error output is omitted, and
credential patterns in the remaining messages are redacted. The whole
check has a 30-second deadline, including git config reads and hook probes;
cancelled subprocess pipes have at most one second to close.

```json
{"type": "hostCheck", "id": "c24", "cmd": ["claude"], "guard": true, "repo": "~/src/pitwall"}
```

```json
{"type": "result", "id": "c24", "ok": true, "data": {"problems": [{"what": "claude is not on the daemon's PATH", "fix": "install it, or add its folder to the PATH pitwall's daemon starts with (docs/remote.md)", "warn": false}]}}
```

### adopt

`adopt` makes `tab` the parent of orphans of its session, the tabs whose
parent tab is gone, as `pitwall adopt` run in that tab does: those
`ids` lists (from `listTabs` with `orphans`), or `all`. `all` skips
`tab` itself and its own ancestors. A worker on another host is
`remote`, and `all` while one is an orphan changes nothing; adopt those
with the CLI. This daemon does not see the tabs of a session it no
longer has on other hosts, which `pitwall adopt --all` also takes.

```json
{"type": "adopt", "id": "c25", "tab": "@9f2c4e1a7b3d5c60", "all": true}
```

```json
{"type": "result", "id": "c25", "ok": true, "data": {"adopted": ["b9e0"]}}
```

`docs/examples` has two clients that list the tabs and read a pane:
[`control.py`](examples/control.py) (Python, standard library) and
[`control.sh`](examples/control.sh) (bash, `socat` and `jq`).

<a id="results"></a>
## Results and errors

A failed command gets `ok: false` with a `code` and a `message` for a
person. The codes are the chat loop's ([errors](managed-sessions.md#driver)):

| Code | Means |
| --- | --- |
| `not-found` | no such chat, request, turn, session, tab or pane |
| `conflict` | someone answered first, the chat is archived, the tab is pinned, a tab has that name; a control command the daemon refused, with its words |
| `busy` | the chat cannot take this now, for example `rewind` while a turn runs; 64 `waitPane` already pending |
| `unsupported` | this provider cannot, or this server does not know the command |
| `provider` | the provider refused; `message` has its words |
| `invalid` | the command is malformed: a missing field, a bad value, too many attachments |
| `forge` | `gh` failed; `message` has its words |
| `tooLarge` | the result is over 4 MiB, on the [local socket](#local-socket) |
| `ambiguous` | a [control](#control) address or session matches more than one |
| `remote` | a control address names a tab on another host, or a request would act on a worker or lane there |
| `timeout` | a `waitPane` or `promptPane` passed its `timeoutMs`, or the prompted turn never started |
| `refused` | the guard cannot cover a `newTab`, `worktreeNew`, `splitPane` or `tabNew`; nothing started |
| `jev` | deprecated: this server never sends it |

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
| `listSessions(query?)` | `listSessions` | aide filters by `query` itself. `TaskLibrarySnapshot`'s notification settings stay aide's. `projectName`, `workspaceName` and `branchName` come from joining `workspaceId` with `listProjects`. `pinned` is the chat's (`pin`). `unread`, `excerpt` and `readActivityKey` have no pitwall field in v1. |
| `subscribeSessionList` | `subscribeSessions`, `unsubscribeSessions` | the listener runs on `sessions` and `sessionsChanged` |
| `updateSession(id, {archived, title, pinned})` | `archive`, `unarchive`, `rename`, `pin` | `readActivityKey` has no command in v1 |
| `subscribeSession` | `subscribe`, `unsubscribe` | `deltas` and `removed` become `ManagedSessionUpdate`s |
| `getSessions` | `listSessions`, then `read` per chat | |
| `getSavedSession` | `read` with `after: 0` | works on archived chats |
| `start` | `start` | renamed: `cwd` is `dir`, `mode` is `provider`. pitwall picks `sessionId`, `paneId` and `workspaceId` and returns them. Dropped: `agents`, `jevGates`, `orchestratorSelection`, `contextWindow`, `thinkingBudget`, `readOnly`, `serviceTier`, `selectionNotice`, `interactionMode` (see `setMode`). Added: `worktree`, `base`, `branch`, `name`, `prompt`. |
| `send` | `send` | `recipients` is dropped; steer and queue are `how`; `selectedSkill` is `skill`, which aide's main process resolves to its native name and path first |
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
| Jev settings (`updateJevSettings`, `updateJevGates`) | none | `setJev` is accepted and ignored |
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
2. Read state of a chat (`updateSession`'s `readActivityKey`): an
   additive command once aide needs it over the web.
3. Which pitwall session and group `start` puts a tab in: the last used
   one for now; `session` and `group` keys are additive.
4. The phone: it keeps its device tokens and its own HTTP pages
   ([phone.md](phone.md)). The full web client on a phone authenticates
   through a device token, which buys a session and its own `hello` token
   ([Web client](remote.md#web-client)).
5. What the chat loop cannot do yet answers `unsupported`. `archive` with
   `delete: true` archives a live chat as plain `archive` does, as
   `ChatArchive` does on the socket.
6. A page the daemon serves gets its `hello` token from
   `/_pitwall/bootstrap` with a session cookie, never from `web.json` or a
   URL ([Web client](remote.md#web-client)).
