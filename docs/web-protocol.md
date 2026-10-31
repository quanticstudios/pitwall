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
ENG-2698 conformance fixtures; ENG-2700 aide's WebSocket transport.

## Contents

- [Connection](#connection)
- [Frames](#frames)
- [Session list](#list)
- [One session's deltas](#deltas)
- [Resync](#resync)
- [Backpressure](#backpressure)
- [Commands](#commands)
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
{"type": "welcome", "protocol": 1, "server": {"name": "pitwall", "version": "0.31.0"}, "deltaVersion": 1, "capabilities": ["subscribeSessions", "unsubscribeSessions", "listSessions", "subscribe", "unsubscribe", "read", "start", "send", "interrupt", "resolveApproval", "resolveInput", "set", "fork", "rewind", "archive", "unarchive", "listProjects", "listWorktrees"]}
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
| `queued` | messages waiting for the running turn to end |
| `turns` | turns so far |
| `seq` | the seq of the chat's latest delta |
| `updatedAt` | RFC 3339 |

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
chats:

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
| `resolveApproval` | `ChatApprove` | none |
| `resolveInput` | `ChatAnswer` | none |
| `set` | `ChatSet` | none |
| `fork` | `ChatFork` | `sessionId`, `workspaceId` of the new chat |
| `rewind` | `ChatRewind` | none |
| `archive` | `ChatArchive` | none |
| `unarchive` | `ChatUnarchive` | `workspaceId` |
| `listProjects` | the daemon's `State` | `projects` |
| `listWorktrees` | `gitstat.ListWorktrees` | `worktrees` |

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
an `image` with base64 `data`, or a `workspace-file` with a `path` under
`dir`. The server refuses more than 8, an image over 5 MiB, or images over
10 MiB in all, with `invalid`; those are aide's limits.

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

### fork and rewind

```json
{"type": "fork", "id": "12", "sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a", "turnId": "turn-2", "worktree": "resize-alt"}
```

```json
{"type": "rewind", "id": "13", "sessionId": "0b6c3e1a-5d2f-4c1e-9a7b-3f2e1d0c9b8a", "turnId": "turn-3"}
```

`fork` makes a new chat holding everything up to the end of `turnId`, in a
new worktree when `worktree` is set. `rewind` restores files to the
checkpoint of `turnId` and drops the turns from it on
([fork and rewind](managed-sessions.md#fork-rewind)).

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
| `setModel` | `set` with `model`, `effort` | a different provider (`mode`) gets `unsupported`; `serviceTier` is dropped |
| `setPermissionMode` | `set` with `permissionMode` | |
| `setMode` | `set` with `permissionMode` | `plan` is `permissionMode: "plan"`, `build` is `permissionMode: "default"` |
| `fork(sessionId, responseContentItemId)` | `fork` | pitwall forks at a turn: send the item's `turnId` |
| `previewRewind`, `rewind(previewId, mode)` | `rewind` | no preview step; the mode is always `code-and-conversation` |
| `getCheckpointDiff` | none in v1 | pitwall has the turn diff (`gitstat.TurnDiff`); a later `checkpointDiff` command is additive |
| `close` | `archive` | closing a chat tab archives the chat in pitwall |
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
5. A page the daemon serves gets its `hello` token from
   `/_pitwall/bootstrap` with a session cookie, never from `web.json` or a
   URL ([Web client](remote.md#web-client)).
