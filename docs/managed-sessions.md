# Managed sessions

Design for [ENG-2678](https://linear.app/quantic/issue/ENG-2678). A managed
session is an agent the daemon drives over the agent's own structured
protocol (Claude Code's stream-json mode, Codex's app-server) instead of a
PTY. It is the second kind of agent in the daemon, next to agents in panes.
The tickets after this one build against the types, files and messages
below. Each section starts with an anchor that the [ticket map](#tickets)
links to; keep those anchors when you edit a heading.

Inputs: [the stream-json spike](research/claude-stream-json.md)
(ENG-2677, Claude Code 2.1.314), aide's delta schema (ENG-2655,
`src/shared/session-delta.ts` and `fixtures/session-deltas/` in aide),
aide's Codex backend (`CodexManagedSessionBackend` in
`src/main/managed-session-manager.ts`), and the app-server schema that
`codex app-server generate-json-schema` writes for codex-cli 0.166.0.

## Contents

- [Names](#names)
- [Where a chat sits in model.State](#placement)
- [Lifecycle](#lifecycle)
- [Event model](#events)
- [Driver interface and errors](#driver)
- [Claude driver](#claude)
- [Codex driver](#codex)
- [Fake providers](#fake)
- [Storage](#storage)
- [Resume](#resume)
- [Restart reconciliation](#reconcile)
- [Fork and rewind](#fork-rewind)
- [Who answers approvals](#approvals)
- [Worktrees, ports, checkpoints, triage, limits, notifications](#attach)
- [Socket messages and CLI](#socket)
- [GUI](#gui)
- [Web protocol constraints](#web)
- [What stays out](#out)
- [Left open on purpose](#open)
- [Ticket map](#tickets)

<a id="names"></a>
## Names

pitwall already uses "session" for a named set of tabs (`model.Session`,
GLOSSARY.md). To keep the two apart:

| Word | Means | In code |
| --- | --- | --- |
| chat | one managed session, as users and the CLI see it (`pitwall chat`) | `model.Chat`, `chat` in `internal/daemon` |
| managed session | the same thing, in design text and in aide's code | `session.Snapshot` |
| provider session | the agent's own conversation: Claude's `session_id`, a Codex thread | `session.Config.Resume` |
| driver | the Go code that runs one provider for one chat | `session.Driver` |
| delta | one change to a chat's snapshot, in aide's ENG-2655 shape | `session.Delta` |
| event | what a driver reports, before the daemon turns it into deltas | `session.Event` |

ENG-2684 adds the **Chat** line to GLOSSARY.md.

Packages:

| Package | Owns | Imports |
| --- | --- | --- |
| `internal/session` | `Event`, `Delta`, `Snapshot`, `Reduce`, `Apply`, `Driver`, `Config` | stdlib, `internal/model` |
| `internal/session/eventlog` | a chat's files: config, snapshot, delta log | stdlib, `internal/session` |
| `internal/session/claude` | the Claude driver | stdlib, `internal/session` |
| `internal/session/codex` | the Codex driver | stdlib, `internal/session` |
| `internal/session/fake` | scripted providers for tests and aide's e2e | stdlib, `internal/session` |
| `internal/daemon` (`chats.go`) | the chat loop, commands, attachments, State summaries | the above |
| `internal/proto` | `Chat*` socket messages | |
| `cmd/pitwall` (`chat.go`) | `pitwall chat` | |
| `internal/web` | the web protocol (M5) | `internal/session` |

No package under `internal/session` imports `internal/daemon`, `proto` or
another driver.

<a id="placement"></a>
## Where a chat sits in model.State

A chat owns one tab. Worktrees, ports, branch stats, PRs, the conflict
radar, pinning, colors, groups and the sidebar row all key on
`Workspace.ID`, so a chat gets every one of them by being a tab.

```go
// internal/model/model.go

type Workspace struct {
	// ...existing fields...

	// Chat is the id of the managed session this tab shows (State.Chats),
	// "" for a tab of panes. A chat tab has no Tabs and no panes.
	Chat string
}

// Chat is a managed session's summary. The daemon keeps it current; the
// transcript is fetched with proto.ChatRead, never pushed in State.
type Chat struct {
	ID          string // a UUID the daemon chose; also the chat's Activity.PaneID
	WorkspaceID string // the tab it owns
	Provider    Provider
	Model       string // "" means the provider's default
	Effort      string
	Mode        string // permission mode: default, acceptEdits, plan, bypassPermissions
	// Title is the first prompt (first line, whitespace collapsed, cut to 48
	// runes, as Pane.Prompt) until someone renames the tab.
	Title     string
	State     string // aide's sessionState: connecting, ready, running, waiting, stopped, error
	Pending   int    // approvals and input requests waiting on someone
	Queued    int    // messages waiting for the running turn to end
	Turns     int
	Seq       int64 // the seq of the chat's latest delta
	UpdatedAt time.Time
}

type State struct {
	// ...existing fields...

	// Chats are the live (not archived) chats of every session. The daemon
	// fills it in from the chats folder; it is not saved in state.json.
	Chats []Chat `json:"-"`
}
```

Rules:

- `Workspace.Chat` is saved in `state.json`; `State.Chats` is not. The
  daemon rebuilds `Chats` from `chats/<id>/` at start ([Storage](#storage)).
  Adding `Workspace.Chat` bumps `store.formatVersion` to 9, so the
  `.prev` copy covers a downgrade.
- Each chat that has started a turn has one `model.Activity` whose `PaneID`
  is the chat id and whose `SessionID` is the provider session id. Pane ids
  are 16 hex digits (`daemon.newID`); chat ids are UUIDs, so the two never
  collide. Code that looks a pane up from `Activity.PaneID` must expect a
  chat id. ENG-2688 lists and fixes those call sites (`limitStopped`,
  `fireResume`, `resumeBlocked`, `internal/remote`'s list, triage).
- The Activity follows the snapshot:

  | Snapshot | `Activity.State` | `Detail` |
  | --- | --- | --- |
  | `sessionState` connecting | `connecting` | |
  | running | `working` | |
  | waiting, `planApproval` set | `plan-ready` | the plan's first line |
  | waiting, an approval pending | `pending-approval` | the approval's `detail` |
  | waiting, an input pending | `awaiting-input` | the first question |
  | ready after a turn | `completed` | `agent.Summary` of the last assistant text |
  | stopped (interrupted) | `completed` | "Interrupted" |
  | error | `error` | `lastError` |

  No Activity exists before the first turn, as for an agent pane at its
  first prompt.
- The tab's title (`Tab.Title`, `Workspace.Label`) is `Chat.Title` unless
  the tab is named.
- A GUI, CLI or watch client below `proto.ChatLevel` never sees a chat:
  the daemon leaves chat tabs out of `Workspaces`, their ids out of
  `Session.Order`, and their activities out of `Activities` in that
  client's `StateMsg`. gob drops `State.Chats` and `Workspace.Chat` on its
  own for a peer without them.
- Text streaming never bumps `State.Version`. The chat loop takes `d.mu`
  and calls `changed()` only when a field of `model.Chat` or the chat's
  Activity changes: state, pending, queued, turns, title, model, mode.

<a id="lifecycle"></a>
## Lifecycle

A chat has a lifetime; its provider process has a shorter one.

```
              ChatNew
                 │
                 ▼
  ┌────────── live ───────────┐   ChatArchive    ┌──────────┐  ChatArchive{Delete}  ┌─────────┐
  │ process: none ⇄ running   │ ───────────────▶ │ archived │ ────────────────────▶ │ deleted │
  │ turn: idle ⇄ turn ⇄ wait  │ ◀─────────────── │          │                       └─────────┘
  └───────────────────────────┘   ChatUnarchive  └──────────┘
```

| Step | What the daemon does |
| --- | --- |
| Create (`ChatNew`) | Makes the tab: in `Dir`, or in a new worktree with its port block, exactly as `NewWorkspace` does. Writes `chats/<id>/config.json` and a first `snapshot` delta. Starts the provider (`Provider.Start` with an empty `Origin`). Sends the prompt, if any, as the first turn. |
| Turn (`ChatSend`) | Idle: adds a `user-prompt` item, takes the turn checkpoint ([attach](#attach)), calls `Driver.Send`. A turn running: queues the message (or steers, when asked and the driver can). When a turn ends and the queue is not held, the next queued message starts the next turn. |
| Steer (`ChatSend{How: "steer"}`) | `Driver.Steer`. On `ErrUnsupported` the message is queued instead and the result says so. |
| Interrupt (`ChatInterrupt`) | `Driver.Interrupt`. The turn ends `interrupted`. Pending approvals and inputs of that turn resolve. The queue is held until the next `ChatSend`, so stopping a turn does not start the next one. |
| Answer (`ChatApprove`, `ChatAnswer`) | First answer wins ([approvals](#approvals)). Allowing a `plan-approval` (Implement) while `permissionMode` is `plan` then sets it to `default`, as `ChatSet` would. |
| Settings (`ChatSet`) | `Driver.Set`, then a `session.patch` delta with the new `model`, `effort` or `permissionMode`. A different `Provider` closes the driver and starts the other provider instead, between turns ([web protocol, set](web-protocol.md#set)). |
| No process | After a daemon restart, after a crash, or after `Close`. The chat stays `ready`; the next command that needs the provider starts it with `Origin{Resume: cfg.Resume}` ([resume](#resume)). |
| Archive (`ChatArchive`) | Closes the driver, compacts the log, moves `chats/<id>/` to `chats/archive/<id>/`, removes the tab as `CloseTab` would (a worktree stays; deleting it stays `DeleteWorkspace`'s job). Refused for a pinned tab, like `CloseTab`. Closing a chat tab or killing its session archives the chat. |
| Unarchive (`ChatUnarchive`) | Moves the folder back and opens a tab in the chat's `Cwd`. The process starts lazily. |
| Delete (`ChatArchive{Delete: true}`) | Removes the folder. An archived chat only. |

Close of the daemon: each chat loop flushes, writes its snapshot, and calls
`Driver.Close` with a 5 s deadline; no turn continues without the daemon.

<a id="events"></a>
## Event model

Two layers, both from aide, so aide's fixtures test pitwall unchanged:

```
driver ──Event──▶ chat loop: Reduce ──Delta (seq)──▶ eventlog, socket subscribers, web clients
                                 └──Apply──▶ Snapshot (in memory, and snapshot.json)
```

- `Event` is the Go form of aide's `ManagedProviderRuntimeEvent`
  (`src/shared/provider-runtime.ts`), plus two pitwall kinds. Drivers emit
  events and know nothing about deltas, seq or storage. Fake provider
  scripts are JSON arrays of events.
- `Delta` is aide's `SessionDelta` at version 1, field for field. It is
  what the log stores, what `ChatRead` returns and what the web protocol
  sends. Nothing else stores or sends chat contents.
- `Snapshot` is aide's `ManagedSessionSnapshot`, the fields pitwall fills.

### Event

```go
package session

// EventType values are aide's ManagedProviderRuntimeEvent types, plus
// "limit" and "session.resume-id", which are pitwall's.
type EventType string

const (
	EventStarted         EventType = "session.started"
	EventState           EventType = "session.state.changed" // State, Detail
	EventExited          EventType = "session.exited"        // ExitCode (nil when killed)
	EventTurnStarted     EventType = "turn.started"          // TurnID, QueuedSendCount
	EventTurnCompleted   EventType = "turn.completed"        // TurnID, TurnState, ErrorMessage, Point
	EventRequestOpened   EventType = "request.opened"        // RequestID, RequestType, Detail, Request
	EventRequestResolved EventType = "request.resolved"      // RequestID
	EventContent         EventType = "content.delta"         // ItemID, Kind, Phase, Model, Text, Tool
	EventInputRequested  EventType = "user-input.requested"  // RequestID, Detail, Questions
	EventInputResolved   EventType = "user-input.resolved"   // RequestID
	EventError           EventType = "runtime.error"         // Message
	EventUsage           EventType = "session.usage.updated" // token counts, Report
	EventLimit           EventType = "limit"                 // Limit
	EventResumeID        EventType = "session.resume-id"     // ResumeID, Version, Capabilities
)

// Event is one thing a driver saw. JSON tags match aide's runtime events,
// so a fixture's runtimeEvents decode into []Event. Only the fields of
// Type are set.
type Event struct {
	Type      EventType `json:"type"`
	CreatedAt string    `json:"createdAt"` // Stamp(time)

	State  string `json:"state,omitempty"` // session.state.changed: a SessionState; turn.completed: a TurnState
	Detail string `json:"detail,omitempty"`

	ExitCode *int `json:"exitCode,omitempty"`

	TurnID          string `json:"turnId,omitempty"`
	QueuedSendCount int    `json:"queuedSendCount,omitempty"`
	ErrorMessage    string `json:"errorMessage,omitempty"`
	// Point is where a fork or rewind to the end of this turn continues
	// from: Claude's last assistant message uuid, Codex's turn id. The chat
	// loop keeps it in Config.Points; it never reaches a delta.
	Point string `json:"point,omitempty"`

	RequestID   string       `json:"requestId,omitempty"`
	RequestType RequestType  `json:"requestType,omitempty"`
	Request     *ToolRequest `json:"request,omitempty"`
	Questions   []Question   `json:"questions,omitempty"`

	ItemID string    `json:"itemId,omitempty"`
	Kind   ItemKind  `json:"kind,omitempty"`
	Phase  string    `json:"phase,omitempty"` // commentary, final_answer
	Model  string    `json:"model,omitempty"`
	Text   string    `json:"text,omitempty"` // text to append; "" with a Tool
	Tool   *ToolCall `json:"tool,omitempty"` // invariant: the whole call so far, never a delta

	Message string `json:"message,omitempty"`

	InputTokens       int64        `json:"inputTokens,omitempty"`
	OutputTokens      int64        `json:"outputTokens,omitempty"`
	CachedInputTokens *int64       `json:"cachedInputTokens,omitempty"`
	ContextTokens     *int64       `json:"contextTokens,omitempty"`
	MaxTokens         *int64       `json:"maxTokens,omitempty"`
	Report            *UsageReport `json:"report,omitempty"`

	Limit *Limit `json:"limit,omitempty"`

	ResumeID     string   `json:"resumeId,omitempty"`
	Version      string   `json:"version,omitempty"`      // the CLI's version
	Capabilities []string `json:"capabilities,omitempty"` // Claude's system/init capabilities
}

// Limit is a plan limit the provider reported. The daemon turns it into a
// flow.Limit for internal/flow and limit resume.
type Limit struct {
	Rejected bool          `json:"rejected"` // the provider refused the turn on it
	Resets   time.Time     `json:"resets"`   // zero when unknown
	Windows  []LimitWindow `json:"windows"`
}

type LimitWindow struct {
	Minutes int64     `json:"minutes"`
	Used    float64   `json:"used"` // percent, 0 to 100
	Resets  time.Time `json:"resets"`
}

// Stamp formats t as aide does (JavaScript toISOString): UTC, milliseconds.
func Stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
```

Timestamps in events, deltas and snapshots are `Stamp` strings, not
`time.Time`, so a fixture round-trips byte for byte. Item, turn and request
ids are strings the driver picks: the provider's own id where it has one
([Claude](#claude), [Codex](#codex)), else a UUID. They are unique within a
chat for its whole life, across processes.

### Delta

```go
// DeltaVersion is aide's SESSION_DELTA_VERSION.
const DeltaVersion = 1

type DeltaType string // snapshot, item.add, item.text.append, item.patch, tool.start,
// tool.output.append, tool.end, reasoning.append, plan.update, approval.add,
// approval.resolve, input.add, input.resolve, turn, status, usage,
// usage.reports, error, session.patch

// Delta is aide's SessionDelta: the envelope and the fields of every type,
// flat. MarshalJSON writes the envelope and only Type's fields, with null
// where aide's type is `T | null`; UnmarshalJSON reads any of them.
type Delta struct {
	Version   int       `json:"version"`
	SessionID string    `json:"sessionId"` // the chat id
	Seq       int64     `json:"seq"`       // steps by exactly 1 per chat
	Revision  int64     `json:"revision"`  // the snapshot's revision once applied
	UpdatedAt string    `json:"updatedAt"` // the snapshot's updatedAt once applied
	Type      DeltaType `json:"type"`

	Snapshot      *Snapshot       `json:"snapshot"`
	Item          *Item           `json:"item"`
	ItemID        string          `json:"itemId"`
	Text          string          `json:"text"`
	ItemUpdatedAt string          `json:"itemUpdatedAt"`
	Patch         json.RawMessage `json:"patch"` // item.patch: a partial Item
	Status        string          `json:"status"` // tool.end: completed, failed; status: working, waiting, done, error, interrupted
	ExitCode      *int            `json:"exitCode"`
	StartedAt     string          `json:"startedAt"`
	EndedAt       string          `json:"endedAt"`
	Tasks         []Task          `json:"tasks"`
	Approval      *Approval       `json:"approval"`
	RequestID     string          `json:"requestId"`
	Input         *InputRequest   `json:"input"`
	Turn          *TurnSummary    `json:"turn"`

	SessionState    string  `json:"sessionState"`
	Running         bool    `json:"running"`
	Detail          *string `json:"detail"`
	LastCompletedAt *string `json:"lastCompletedAt"`

	Usage        *Usage        `json:"usage"`
	UsageReports []UsageReport `json:"usageReports"`
	UsageSummary *UsageSummary `json:"usageSummary"`
	LastError    *string       `json:"lastError"`
	LastErrorAt  *string       `json:"lastErrorAt"`

	Set   json.RawMessage `json:"set"`   // session.patch: a partial Snapshot of patchable keys
	Unset []string        `json:"unset"` // session.patch
}
```

The table of types and their effect is aide's
`fixtures/session-deltas/README.md`; pitwall does not restate it, and a
change there is a change here. Envelope rules pitwall adds:

- Each `Reduce` call is one revision: every delta it returns shares one
  `revision` (previous + 1) and one `updatedAt`. That matches aide, which
  publishes several deltas under one revision.
- The chat loop coalesces text before it numbers deltas: consecutive
  `item.text.append`, `reasoning.append` and `tool.output.append` deltas
  for the same item within 16 ms merge into one. Any other delta flushes
  the pending one first. Every consumer (log, socket, web) sees the same
  numbered stream.
- A chat's first delta, seq 1, is a `snapshot`.

### Snapshot

```go
// Snapshot is aide's ManagedSessionSnapshot. Fields aide marks optional
// and pitwall never sets (fleet, agents, jevGates, orchestratorSelection,
// pendingProviderSelection, preparingActivityId) are left out; aide reads
// them as absent.
type Snapshot struct {
	SessionID         string         `json:"sessionId"`   // the chat id
	PaneID            string         `json:"paneId"`      // the chat id too; aide's bridge (M6) maps it
	WorkspaceID       string         `json:"workspaceId"` // the tab (model.Workspace.ID)
	Provider          model.Provider `json:"provider"`
	Content           []Item         `json:"content"`
	Turns             []TurnSummary  `json:"turns"`
	PendingApprovals  []Approval     `json:"pendingApprovals"`
	PendingUserInputs []InputRequest `json:"pendingUserInputs"`
	PlanApproval      *PlanApproval  `json:"planApproval"`
	SessionState      string         `json:"sessionState"`
	Running           bool           `json:"running"`
	Detail            *string        `json:"detail"`
	LastCompletedAt   *string        `json:"lastCompletedAt"`
	LastError         *string        `json:"lastError"`
	LastErrorAt       *string        `json:"lastErrorAt"`
	Model             *string        `json:"model"`
	Effort            *string        `json:"effort"`
	ServiceTier       *string        `json:"serviceTier"`
	PermissionMode    string         `json:"permissionMode"`
	InteractionMode   string         `json:"interactionMode"` // build, plan
	SelectionNotice   *string        `json:"selectionNotice"`
	RuntimeGeneration int            `json:"runtimeGeneration"` // +1 each time a process starts
	Revision          int64          `json:"revision"`
	UpdatedAt         string         `json:"updatedAt"`
	Usage             *Usage         `json:"usage,omitempty"`
	UsageReports      []UsageReport  `json:"usageReports,omitempty"`
	UsageSummary      *UsageSummary  `json:"usageSummary,omitempty"`
}
```

`Item`, `ToolCall`, `Task`, `TurnSummary`, `Approval`, `ToolRequest`,
`InputRequest`, `Question`, `Usage`, `UsageReport` and `UsageSummary` are
the same-named aide types, field for field, with `unknown` as
`json.RawMessage`. Where aide's field is optional and nullable
(`model?: string | null`), the fixtures decide between a pointer with
`omitempty` and one without. `TurnSummary.checkpointId` carries pitwall's
turn checkpoint ([attach](#attach)).

### Reduce and Apply

```go
// Reduce turns one driver event into the deltas that bring s up to date,
// without envelopes; the chat loop stamps Version, SessionID, Seq,
// Revision and UpdatedAt. It is pure: same s and e, same deltas.
func Reduce(s Snapshot, e Event) []Delta

// Apply is aide's applyDelta: it returns the snapshot after d, or a
// *ResyncError when d cannot apply. It never changes s.
func Apply(s *Snapshot, seq int64, d Delta) (Snapshot, error)

// ResyncError reasons are aide's: version, no-base, session, seq-gap, item.
type ResyncError struct{ Reason, Message string }
```

`Apply` with a nil base accepts only a `snapshot` delta, as aide's does.
The chat loop's own changes (a `user-prompt` item on send, the turn summary
with `requestText`, pitwall's system lines, reconciliation) are deltas it
builds directly, not events.

`Reduce` rules, the ones aide's manager applies:

| Event | Deltas |
| --- | --- |
| `session.state.changed` | `status` (sessionState, running, detail, lastCompletedAt) |
| `turn.started` | `turn` (state running), `status` running |
| `turn.completed` | `turn` (its state, completedAt), `status`; failed with a message also `error` |
| `content.delta`, new item, no tool | `item.add` |
| `content.delta`, known text item | `item.text.append` (`reasoning.append` for reasoning kinds) |
| `content.delta`, new tool | `tool.start` |
| `content.delta`, known tool | the difference: new output suffix `tool.output.append`, new tasks `plan.update`, status to completed or failed `tool.end`, anything else `item.patch` |
| `request.opened` | `approval.add`, `status` waiting |
| `request.resolved` | `approval.resolve`, `status` |
| `user-input.requested` / `resolved` | `input.add` / `input.resolve`, `status` |
| `runtime.error` | `error` |
| `session.usage.updated` | `usage`, and `usage.reports` with a report |
| `session.exited` | ends an open turn failed, resolves pending requests, `status` error unless a close was asked for |
| `limit` | nothing; the chat loop hands it to [limits](#attach) |
| `session.resume-id` | nothing; the chat loop saves it in `Config.Resume` |

Tests (ENG-2679): copy aide's `fixtures/session-deltas/*.json` to
`internal/session/testdata/session-deltas/`. For each fixture, `Apply` over
`deltas` gives `snapshot` (the requirement), and `Reduce` over
`runtimeEvents` plus the loop's send-side deltas gives the same snapshot
(the parity check aide's `session-delta-fixtures.test.ts` does). A fuzz
test shuffles a stream: `Apply` never panics and reports the gap.

<a id="driver"></a>
## Driver interface and errors

```go
package session

// Provider starts drivers of one kind. The daemon holds one per
// model.Provider it can drive.
type Provider interface {
	// Start runs the provider for one chat and returns once it takes
	// commands. emit gets every event, from one goroutine, in order, until
	// Close returns; never after.
	Start(ctx context.Context, cfg Config, from Origin, emit func(Event)) (Driver, error)
}

// Origin is where a started provider session comes from.
type Origin struct {
	Resume string `json:"resume,omitempty"` // a provider session to continue; "" starts a new one
	At     string `json:"at,omitempty"`     // a Config.Points value: continue from there, dropping what came after
	Fork   bool   `json:"fork,omitempty"`   // continue under a new provider id, leaving Resume as it was
}

// Driver is one running provider process for one chat. Every method is
// safe to call from any goroutine and returns once the provider took or
// refused the command, not when the work is done.
type Driver interface {
	// Send starts a turn with m and returns its turn id. ErrBusy while a
	// turn runs: the chat loop queues instead.
	Send(ctx context.Context, m Message) (turnID string, err error)
	// Steer adds m to the running turn. ErrUnsupported when the provider
	// cannot (the loop queues), ErrIdle with no turn.
	Steer(ctx context.Context, m Message) error
	// Interrupt stops the running turn; a no-op while idle. The turn's end
	// arrives as turn.completed with state interrupted.
	Interrupt(ctx context.Context) error
	// Approve answers request.opened requestID.
	Approve(ctx context.Context, requestID string, d Decision) error
	// Answer answers user-input.requested requestID, by question id.
	Answer(ctx context.Context, requestID string, answers map[string][]string) error
	// Set changes model, effort or permission mode; "" leaves one as it is.
	Set(ctx context.Context, s Settings) error
	// Close ends the process: politely, then killed when ctx ends.
	Close(ctx context.Context) error
}

type Message struct {
	ID     string // the user-prompt item id; Claude's stdin uuid, Codex's clientUserMessageId
	Text   string
	Images []Image
}

type Image struct {
	MimeType string // image/png, image/jpeg, image/webp
	Name     string
	Data     []byte
}

type Decision struct {
	Allow      bool
	ForSession bool            // allow this kind of call for the rest of the session
	Reason     string          // deny: what the model reads
	Input      json.RawMessage // allow: edited tool input, nil for unchanged
	// Plan approvals: Allow is Implement, a deny with Reason is Refine.
}

type Settings struct{ Model, Effort, Mode string }

// Config is a chat's config.json (see Storage).
type Config struct {
	Format    int            `json:"format"` // 1
	ID        string         `json:"id"`
	Provider  model.Provider `json:"provider"`
	Workspace string         `json:"workspace"`
	Cwd       string         `json:"cwd"`
	Model     string         `json:"model,omitempty"`
	Effort    string         `json:"effort,omitempty"`
	Mode      string         `json:"mode,omitempty"`
	// Resume is the provider session id: Claude's session_id, Codex's
	// thread id. A fork's new id replaces it as soon as the driver says.
	Resume string `json:"resume,omitempty"`
	// From is where the next start continues instead of Resume: a fork or
	// rewind whose new provider id has not arrived yet (Claude reports it
	// with the first turn). The new id clears it.
	From *Origin `json:"from,omitempty"`
	// Points maps a turn id to Event.Point, for fork and rewind.
	Points  map[string]string `json:"points,omitempty"`
	Queue   []QueuedMessage   `json:"queue,omitempty"`
	Held    bool              `json:"held,omitempty"` // the queue waits for the next send (after an interrupt)
	Created time.Time         `json:"created"`
	// Env is what the process gets on top of the daemon's environment;
	// rebuilt from the tab on every start, never saved.
	Env []string `json:"-"`
}

type QueuedMessage struct {
	ID     string   `json:"id"`
	Text   string   `json:"text"`
	Images []string `json:"images,omitempty"` // paths under the chat's images/ folder
	Attachments []Attachment `json:"attachments,omitempty"` // aide's, inline
}
```

The interface is the spike's narrow one (start, send, approve, interrupt,
set, close, events out) plus `Steer` and `Answer`. Fork and rewind are
`Start` with an `Origin`, so no driver has a fork method.

### Errors

```go
var (
	ErrUnsupported = errors.New("the provider cannot do this")
	ErrBusy        = errors.New("a turn is running")
	ErrIdle        = errors.New("no turn is running")
	ErrClosed      = errors.New("the provider process has ended")
)

// ProviderError is the provider refusing a command: a JSON-RPC error, a
// control_response with subtype error.
type ProviderError struct{ Op, Message string }
```

The chat loop adds `ErrNotFound` (no such chat or request) and
`ErrResolved` (someone answered first). How each surfaces:

| Where it happens | Who sees it |
| --- | --- |
| A command fails | The caller only, as a typed result: `not-found`, `conflict` (`ErrResolved`, archived), `busy`, `unsupported`, `provider`, `invalid`. Never a delta. |
| `Send` fails with `ProviderError` | The caller, and an `error` delta: the turn did not start and the transcript says why. |
| The provider reports an error (`runtime.error`, a failed turn) | An `error` delta, the Activity's `error` state, a line in `errors.jsonl` as for a pane's API error. |
| The process exits on its own | `session.exited`: an open turn fails with "Claude exited (code N)" and the last 4 KB of stderr, pending requests resolve with a system line, `sessionState` error. The next command restarts it with `Origin{Resume}`. A fourth restart within a minute is refused with `provider` until the user sends again. |
| A line the driver cannot decode | Skipped. `daemon.log` gets one line per unknown `type`/`subtype` per process. |

<a id="claude"></a>
## Claude driver

`internal/session/claude`, one `claude` process per chat, as the spike
recommends. The command is `claude` from `PATH`; `PITWALL_CLAUDE_COMMAND`
replaces it for tests.

### Start

```
claude -p --input-format stream-json --output-format stream-json --verbose
       --include-partial-messages
       --permission-prompt-tool stdio
       --session-id <uuid>                          # Origin{} : a UUID the driver picks
       --resume <Resume> [--resume-session-at <At>] [--fork-session]   # Origin{Resume,...}
       [--model <Model>] [--effort <Effort>] [--permission-mode <Mode>]
```

- cwd is `Config.Cwd`; the environment is the daemon's plus `Config.Env`
  ([attach](#attach)).
- The driver sends no `initialize`; the spike ran every scenario but two
  without it. pitwall's command hooks run from the user's settings.
- The CLI prints nothing until the first user message, so `Start` emits
  `session.state.changed` ready once the process is up, and
  `session.resume-id` with the `--session-id` it chose.
- Every `system/init` is read for `session_id`, `model`,
  `permissionMode`, `claude_code_version` and `capabilities`. A
  `session_id` other than `Config.Resume` (after `--fork-session`) emits
  `session.resume-id`.

### Stdin

| Driver call | Line written |
| --- | --- |
| `Send` | `{"type":"user","uuid":<m.ID>,"session_id":"","parent_tool_use_id":null,"message":{"role":"user","content":[...]}}`, images as base64 `image` blocks. The turn id is `m.ID`. |
| `Interrupt` | `control_request` `{"subtype":"interrupt"}` |
| `Approve` allow | `control_response` success `{"behavior":"allow","updatedInput":<d.Input or the request's input>}`; with `ForSession`, also `updatedPermissions`: the request's `addRules` suggestions with `destination` `session` |
| `Approve` deny | `{"behavior":"deny","message":<d.Reason or "The pitwall user denied this.">}` |
| `Answer` (AskUserQuestion) | allow with `updatedInput` = the input plus `answers` keyed by question text |
| `Set` model | `control_request` `{"subtype":"set_model","model":...}` |
| `Set` effort | `control_request` `{"subtype":"apply_flag_settings","settings":{"effortLevel":...}}` |
| `Set` mode | `control_request` `{"subtype":"set_permission_mode","mode":...}` |
| `Close` | close stdin; kill after the deadline |
| an unknown CLI `control_request` | `control_response` `{"subtype":"error","request_id":...,"error":"pitwall does not handle <subtype>"}` |

`Steer` returns `ErrUnsupported`, so the loop queues the message for the
next turn: no capture shows a user line written mid-turn joining that turn.
`Interrupt` while idle writes nothing. `Approve` and `Answer` refuse a
request id Claude no longer waits on with `ProviderError`. Responses to the
driver's own control requests are not matched yet, so a refused `set_model`
goes unreported.

### Stdout

| Line | Event |
| --- | --- |
| `stream_event` text `content_block_delta` | `content.delta` assistant-text, item id `<message.id>:<block index>` |
| `stream_event` thinking delta | `content.delta` reasoning-text, same id rule |
| `assistant` text block | `content.delta` with only the text not yet streamed |
| `assistant` `tool_use` block | `content.delta` tool-call, item id the `tool_use` id, `Tool{status: running}`; `TodoWrite` fills `Tool.Tasks` |
| `user` `tool_result` | that tool's `content.delta`: status completed, or failed when `is_error`, output the result text |
| `result` | `turn.completed`: `success` completed; `error_during_execution` with `terminal_reason` `aborted_streaming` or `aborted_tools` (stopped during a tool, as in `interrupt-tool`) interrupted; any other error failed with `result` as the message. `Point` is the `uuid` of the turn's last `assistant` line. Then `session.usage.updated` from `usage` and `modelUsage`. |
| `control_request` `can_use_tool` | `request.opened`, request id the `request_id`, type from the tool name as aide's `classifyToolRequestType`, detail `<tool>: <command or input>`; `ExitPlanMode` as `plan-approval` with the plan as detail; `AskUserQuestion` as `user-input.requested`, question ids the question texts, option ids the labels |
| `control_cancel_request` | `request.resolved` or `user-input.resolved` for that id; the loop adds "Claude withdrew this request" |
| `rate_limit_event` | `limit` from `rate_limit_info`: `Rejected` when `status` is `rejected`, windows from `unifiedWindows` |
| `system/status` with `permissionMode` | the loop's `session.patch` for `permissionMode` |
| `system/permission_denied` | a `system` item with the message (not expected with the stdio tool) |
| `system/task_*`, `system/thinking_tokens`, `system/hook_*` | ignored |

### What the spike calls unstable, and what the driver does about it

- `--permission-prompt-tool stdio` and `--resume-session-at` are not in
  `--help`. The SDK passes both. The driver records `claude_code_version`
  in `config.json` per start and logs it once in `daemon.log`.
- Thinking blocks carry empty text on 2.1.314, so reasoning items may stay
  empty. That is not an error.
- `rate_limit_event` came only once per process, and a `rejected` sample
  was never captured. The fields come from `SDKRateLimitInfo` in
  `sdk.d.ts`, and the screen-based limit reading stays the PTY fallback.
- `updatedPermissions` (allow for session), steering mid-turn, `--resume`
  of a session another process holds open, macOS and Windows are untested.
  ENG-2686 wrote allow for session from `sdk.d.ts`'s `PermissionUpdate`
  (an ask with no `addRules` suggestion is allowed once) and left `Steer`
  `ErrUnsupported`; both want a capture.
- Decoding is lenient: switch on `type` and `subtype`, ignore unknown
  fields and types, answer unknown control requests with an error.
- Optional behavior is gated on `capabilities`, not version numbers.
- Driver tests replay `internal/agent/testdata/claude-stream-json/`.
  ENG-2682 moves those files to `internal/session/claude/testdata/` with
  `git mv` and fixes the link in `docs/research/claude-stream-json.md`.

<a id="codex"></a>
## Codex driver

`internal/session/codex`, one `codex app-server` per chat, as aide runs it.
One server per daemon would save processes, but every command a Codex
thread runs inherits the server's environment, and each chat needs its
tab's ports and `PITWALL_PANE`. One server per chat also keeps a crash to
one chat. The command is `codex` from `PATH`; `PITWALL_CODEX_COMMAND`
replaces it for tests.

JSON-RPC 2.0, one object per line, no `jsonrpc` field needed (aide sends
none). Method names below are from the schema codex-cli 0.166.0 writes;
ENG-2681 regenerates it and checks again (`codex app-server
generate-json-schema --out DIR`). aide uses only the ones marked "aide".

| Driver step | Request (client to server) |
| --- | --- |
| `Start` | `initialize` `{clientInfo:{name:"pitwall",title:"pitwall",version}, capabilities:{experimentalApi:true}}`, then the `initialized` notification (aide) |
| `Start`, `Origin{}` | `thread/start` `{cwd, model?, approvalPolicy: "on-request" (or "never" for bypassPermissions), sandbox: "workspace-write" (or "read-only" for plan)}`; the response's `thread.id` is the resume id (aide) |
| `Start`, `Origin{Resume}` | `thread/resume` `{threadId, cwd, model?, approvalPolicy, sandbox}`; on an archived thread, `thread/unarchive` first |
| `Start`, `Origin{Resume, At, Fork}` | `thread/fork` `{threadId: Resume, lastTurnId: At, cwd, model?, approvalPolicy, sandbox}`; `lastTurnId` keeps the turns through it, inclusive, and the response's `thread.id` replaces the resume id. `At` without `Fork` is `ErrUnsupported`: 0.166.0's `thread/revert` cuts history only before a turn, and the driver does not know the next one |
| `Send` | `turn/start` `{threadId, input:[{type:"text",text},{type:"image",url:"data:<mime>;base64,..."}], clientUserMessageId: m.ID, effort?}`; the response's `turn.id` is the turn id (aide, without the id) |
| `Steer` | `turn/steer` `{threadId, expectedTurnId, input, clientUserMessageId}` (aide) |
| `Interrupt` | `turn/interrupt` `{threadId, turnId}` (aide) |
| `Set` | `thread/settings/update` `{threadId, model?, effort?, approvalPolicy?, sandboxPolicy?}` |
| `Close` | close stdin, kill after the deadline |

`thread/start` has no effort field in 0.166.0 (aide passes
`reasoningEffort`, which the schema lacks); effort goes on `turn/start` and
`thread/settings/update`.

| Server message | Event |
| --- | --- |
| `thread/status/changed` | `session.state.changed`: `idle` ready, `systemError` error, `activeFlags` set waiting, else running (aide) |
| `turn/started`, `turn/completed` | `turn.started`, `turn.completed` (failed with `turn.error.message`); `Point` is the turn id (aide) |
| `item/started`, `item/completed` | `content.delta`: tool items (command, file change, MCP call, web search) as tool calls with status and exit code; `agentMessage` as assistant-text with its `phase` (aide) |
| `item/agentMessage/delta` | `content.delta` assistant-text (aide) |
| `item/reasoning/textDelta`, `item/reasoning/summaryTextDelta` | reasoning-text, reasoning-summary-text (aide) |
| `item/commandExecution/outputDelta`, `item/fileChange/outputDelta` | the tool call's output (aide) |
| `turn/plan/updated` | the turn's plan as one `Plan` tool item, `plan-<turn id>`, its steps as `Tasks` |
| `thread/tokenUsage/updated` | `session.usage.updated` (aide's `parseCodexUsage`) |
| `account/rateLimits/updated` | `limit`: `primary` and `secondary` windows (`usedPercent`, `resetsAt`, `windowDurationMins`); `Rejected` when `rateLimitReachedType` is set |
| `error` | `runtime.error` (aide) |
| `serverRequest/resolved` | `request.resolved` or `user-input.resolved` for the request it names, unless pitwall already answered it |
| server request `item/commandExecution/requestApproval`, `item/fileChange/requestApproval`, `item/fileRead/requestApproval` | `request.opened` with a UUID the driver maps to the JSON-RPC id (aide) |
| server request `item/tool/requestUserInput` | `user-input.requested` (aide) |
| any other server request | error `-32601` (aide), and a `runtime.error` that names the method |

Answers:

| Driver call | Response |
| --- | --- |
| `Approve` allow | `{decision:"accept"}`, or `"acceptForSession"` with `ForSession` |
| `Approve` deny | `{decision:"decline"}`; the turn goes on. With a `Reason`, the driver then steers the reason into the turn, since the decision has no field for it |
| `Answer` | `{answers:{<question id>:{answers:[...]}}}` (aide) |

The driver posts `request.resolved` or `user-input.resolved` as it answers.
A turn's end resolves any request still open.

aide answers a deny with `decision: "deny"`, which the 0.166.0 schema does
not list (`accept`, `acceptForSession`, `acceptWithExecpolicyAmendment`,
`applyNetworkPolicyAmendment`, `decline`, `cancel`). pitwall sends
`decline`.

<a id="fake"></a>
## Fake providers

`internal/session/fake` is a `session.Provider` that plays a script. A
script is aide's fixture shape: a JSON file with `mode` (`claude` or
`codex`), `prompt` and `runtimeEvents`, plus pitwall's optional
`delayMs` per event and `answers` (what an approval or input waits for
before the script goes on). aide's fake provider scenarios become scripts
of the same names under `internal/session/fake/testdata/`.

- `PITWALL_FAKE_PROVIDERS=1` makes the daemon use `fake` for Claude and
  Codex. It logs a warning at start, and works in release builds only when
  set by hand.
- `PITWALL_FAKE_SCRIPT=<name>` picks the script for the next started chat;
  else the prompt's first word names it, as in aide's fake.
- Driver tests do not use `fake`: they run the real driver against a fake
  binary (`PITWALL_CLAUDE_COMMAND`, `PITWALL_CODEX_COMMAND`) that replays
  recorded stdout.
- [`testdata/web-protocol/`](../testdata/web-protocol/README.md) holds a
  conformance fixture per script: the events it emits, the delta stream a
  web client gets and the final snapshot. aide vendors them; `go test
  ./internal/session/fake -run TestConformance -update` rewrites them.

<a id="storage"></a>
## Storage

Under `config.StateDir()` (`~/.local/state/pitwall/` on Linux):

```
chats/
  <chat id>/
    config.json      session.Config, written atomically on change (mode 0600)
    snapshot.json    {"format":1,"seq":<n>,"snapshot":<Snapshot>}, written atomically
    events.jsonl     one Delta per line, as JSON
    images/          <sha256>.<ext>: images sent with messages, for the queue and resends
  archive/
    <chat id>/       the same files, for an archived chat
```

Folders are 0700, files 0600, as `internal/store` makes them. `store`'s
`writeAtomic` is exported as `store.WriteAtomic` for `eventlog` to use.

```go
package eventlog

// Open loads the chat in dir: snapshot.json, then each events.jsonl line
// with a seq above the snapshot's, applied in order. A torn last line is
// dropped and the file truncated after the last full line, so the next
// append starts clean. A seq gap or an Apply error stops the replay there,
// keeps what applied, and logs it.
func Open(dir string) (*Log, session.Config, session.Snapshot, int64, error)

func (l *Log) Append(ds []session.Delta) error // one write per batch
func (l *Log) Sync() error
// Compact writes snapshot.json at seq, then truncates events.jsonl. A crash
// between the two is safe: Open skips lines at or below the snapshot's seq.
func (l *Log) Compact(s session.Snapshot, seq int64) error
func (l *Log) WriteConfig(c session.Config) error
func (l *Log) Close() error

func Archive(root, id string) error   // chats/<id> to chats/archive/<id>
func Unarchive(root, id string) error // and back
func Delete(root, id string) error    // an archived chat's folder, gone
```

- Each chat's loop goroutine is the only writer of its files.
- `Sync` runs on `turn` deltas that end a turn, on `approval.resolve` and
  `input.resolve`, and on every `Compact`.
- `Compact` runs every 1,000 deltas, when `events.jsonl` passes 8 MB, on
  archive and on clean shutdown. The log never grows past that.
- Images stay inline in `item.add` deltas as aide's attachment shape
  (base64 `data`), within aide's limits: 8 attachments, 5 MB each, 10 MB per
  message. A queued message keeps its attachments inline in `config.json`
  (`queue[].attachments`); `images/` is unused so far.
- Archive writes the tab's title and the time to `config.json` (`title`,
  `updated`), since the tab that held them goes; an archived chat lists
  from that file alone. aide's old sessions arrive in `archive/` through
  the web protocol's [`import`](web-protocol.md#import), with
  `imported: true` in `config.json`.
- `snapshot.json` `format` and `config.json` `format` start at 1. A reader
  refuses a higher one and leaves the chat out of `State.Chats` with a line
  in `daemon.log`, as `store` treats a newer `state.json`.
- Load budget: a chat of 50,000 deltas loads from its snapshot in under
  200 ms. The daemon reads only `config.json` of each chat at start
  ([resume](#resume)); a chat's snapshot and log load when something opens
  it.

<a id="resume"></a>
## Resume

The resume id is `Config.Resume`: Claude's `session_id` or the Codex thread
id. It is known before the first turn for Claude (pitwall passes
`--session-id`) and from the `thread/start` response for Codex. A fork's
new id replaces it when `session.resume-id` arrives.

- Lazy: at start the daemon lists `chats/`, reads each `config.json`, and
  builds `State.Chats` from it and from the tab. No snapshot loads and no
  process starts. A chat's first command, subscription or `ChatRead`
  loads it; a command that needs the provider starts it with
  `Origin{Resume: cfg.Resume}`. Boot time grows with the number of chats
  only by one small file read each.
- A Claude `--resume` that fails (the process exits before its first
  `system/init`, or `result` says the session is missing) and a Codex
  `thread/resume` error fall back to a new provider session. The loop adds
  a system item, "Resumed without history: <why>", and sends the next
  message with a short summary of the last turns before it (the last
  assistant text of each, cut to 2,000 characters in all). The new id
  replaces `Config.Resume`.
- A fallback drops `Config.Points` with the session they point into, so a
  later fork or rewind to one of those turns takes the text recap of
  [fork and rewind](#fork-rewind).

<a id="reconcile"></a>
## Restart reconciliation

When a chat loads, the loop compares the snapshot with what a process that
is not running can still do, and appends deltas. It never rewrites the log.

| Found in the snapshot | Delta appended |
| --- | --- |
| A turn with state `running` | `turn` with state `interrupted` and `completedAt` now, a `system` item "pitwall restarted during this turn" |
| A pending approval or input | `approval.resolve` / `input.resolve`, a `system` item "Expired: pitwall restarted before anyone answered" |
| `sessionState` running, waiting or connecting | `status` ready, running false |
| A tool call with status running | `tool.end` failed |

The same rules run when a process exits on its own ([errors](#driver)).

A chat loads when the first command names it, so this runs then, before
the command. On an exit, `Reduce` has already ended the turn and the
requests; the rest still runs, with the line "Expired: <Provider> exited
before anyone answered".

`[agents] chat_continue_after_restart` (default false): when on, after a
reconciled interrupted turn the loop resumes the provider and sends
"Continue where you left off", at most 3 times per chat per 5 minutes.
It runs once the command that loaded the chat is done, and not when that
command started a turn. `config.json` keeps the times in `continued`, so
the limit holds across restarts; a refused one adds a `system` item "Not
continued: ...".

<a id="fork-rewind"></a>
## Fork and rewind

Both name a turn of the chat, by id or by number from 1. The driver's
point for "the end of turn T" is `Config.Points[T]`, which
`turn.completed` sets; a turn that failed or was interrupted has none.
Both go through a fork of the provider session, so the session they
start from never changes:

| | Origin | Claude | Codex |
| --- | --- | --- | --- |
| Fork after T | `{Resume, At: Points[T], Fork: true}` | `--resume <Resume> --resume-session-at <At> --fork-session` | `thread/fork` `{threadId, lastTurnId: At}` |
| Rewind to before T | `{Resume, At: Points[T-1], Fork: true}`, or `{}` when T is the first | the same flags | the same request |

Codex's `lastTurnId` is inclusive ("turns after `last_turn_id` are
omitted"), so the fork keeps T. `thread/revert` would cut in place, but
it takes the turn after the point, and one fork path serves both.

The origin waits in `Config.From` until the provider reports the new
session's id (`session.resume-id`): Codex does in the `thread/fork`
response, Claude only in the `system/init` of the next turn. A daemon
restart before then starts from `From` again, so a fork never resumes the
session it forked in place.

- Fork (`ChatFork`) makes a new chat and tab, in the same session as the
  old tab, or in a new worktree made from the old tab's branch when
  `Worktree` is set. Its `config.json` copies the old one's settings and
  the points of the turns it keeps. Its first delta is a snapshot holding
  the old chat's turns through T and the items before the first item of
  a later turn, then a `system` item "Forked from <title>"; the copied
  turns lose their `checkpointId`, which names the old tab's checkpoints.
  The provider starts at once. The old chat does not change. A fork of a
  running turn is `busy`.
- Each turn's checkpoint ([attach](#attach)) is its summary's
  `checkpointId`: the loop takes it before `Driver.Send` and sets it with
  a `turn` delta when `turn.started` arrives. A checkpoint that takes
  longer than the prompt hook's 2 s leaves the turn without one.
- Rewind (`ChatRewind`) refuses while a turn runs (`busy`) and for a turn
  without a `checkpointId` (`unsupported`). It restores the files with
  `gitstat.RewindTurn`, Undo turn's code aimed at T's checkpoint, and
  refuses (`conflict`) in the same cases: HEAD moved since the checkpoint,
  a `.gitignore` changed while files would be removed, a file in the way
  that no snapshot has. Then it drops the checkpoints after T and keeps
  the files it replaced as the redo snapshot, so Redo turn puts them
  back; files that already match T's checkpoint are no refusal. Only once
  the files are back does it close the provider, append a `snapshot`
  delta with T and the turns after it removed and a `system` item
  "Rewound to before turn N", and drop the removed turns' points. The
  provider starts with the next command that needs it.
- A provider that cannot fork (refuses `Start` from the origin), and a
  turn without a point, get the replay fallback of [resume](#resume): a
  new provider session, a `system` item "The provider continues from a
  text recap of this chat, not its history: <why>", and the recap before
  the next message.

<a id="approvals"></a>
## Who answers approvals

Every pending approval and input request lives in the chat's snapshot, so
every client sees the same list. Any of these may answer:

| Client | Path |
| --- | --- |
| pitwall's GUI | `proto.ChatApprove` / `ChatAnswer` over the socket |
| the CLI (`pitwall chat approve`) | the same messages; refused when `$PITWALL_PANE` is set, so an agent in a pane does not answer for another agent (a seatbelt: an agent can unset it) |
| aide and other web clients | the web protocol's answer commands (ENG-2696) |
| a paired phone | `POST /api/approve` and `/api/input` on the phone page (`internal/remote`), with its device token, as `ChatApprove` / `ChatAnswer` (ENG-2692) |

First wins:

1. Every answer goes to the chat's loop, which handles one command at a
   time.
2. If the request id is not in `pendingApprovals` / `pendingUserInputs`,
   the answer gets `conflict` (`ErrResolved`).
3. Otherwise the loop removes it with `approval.resolve` / `input.resolve`
   before it calls `Driver.Approve` / `Answer`, so a second answer that
   arrives while the driver writes also gets `conflict`.
4. If the driver call fails, the loop appends a `system` item with the
   error and does not put the request back. The provider still holds it;
   the next `control_cancel_request` or `serverRequest/resolved`, or the
   turn's end, settles it.
5. A request the provider withdrew, or that expired on restart, is no
   longer pending, so a late answer gets `conflict` too.

The answer, who gave it (`gui`, `cli`, `web:<client name>`,
`phone:<device>`) and how long it waited go to `decisions.jsonl` as the
approval outcome. No hook needs to infer it.

pitwall never answers for the user, with one exception: aide's Jev tool
gate. It is off by default, as in aide, and when on it answers only for
chats aide started ([setJev](web-protocol.md#setjev)). Jev's advice shows
on a chat's approval as it does on a pane's ([decisions.md](decisions.md));
nothing else approves on its own.

<a id="attach"></a>
## Worktrees, ports, checkpoints, triage, limits, notifications

| What | How a chat gets it |
| --- | --- |
| Worktree | `ChatNew{Worktree, Base, Branch}` makes the tab with `worktree.go`, as `NewWorkspace` does: copy and link run as today. A `setup` command from `config.toml` runs before the first turn, with its output in a `command-output` item. One from the repo's `.pitwall/worktree.toml` asks first, as a pitwall-owned approval in the chat ("Run the repo's setup: <command>"); allow runs it, deny skips it. |
| Ports | The tab's port block. `Config.Env` gets `portEnv(ws.Ports)`. |
| Environment | `Config.Env` is what a pane of that tab gets: the preset's variables, the guard's variable when the guard is on for the tab, the ports, and `PITWALL_PANE=<chat id>`. |
| Hooks | The agent loads the user's settings, so pitwall's hooks run. With `PITWALL_PANE` set, the guard denies as in a pane, and `pitwall new` from the agent makes its tab a worker of the chat's tab (the daemon resolves a chat id in `ParentPane` to its tab). The daemon drops hook state reports (`proto.AgentEvent`) whose pane is a chat: the stream is the source of truth, and the turn checkpoint is the loop's job. |
| Turn checkpoints | Before `Driver.Send`, the loop takes `d.checkpoint(cwd, ws)` as the prompt hook does for panes (`daemon.go`, `turnWait`). The turn summary's `checkpointId` is the checkpoint's commit. This turn, Undo turn and Redo turn work on the chat's tab unchanged. |
| aide's Jev gates | A chat aide started over the web protocol (`Config.Aide`) gets aide's gates from `internal/decide`, with the key, consent list and gate settings aide hands over in `setJev`: no turn starts without consent for the workspace, a key and Jev's preparation of the prompt, and with the tool gate on Jev answers the approvals it is sure of. See [setJev](web-protocol.md#setjev). |
| Triage, Jev | Through the chat's Activity: triage, approval advice, turn check and check runs read Activity and the snapshot's text, not a screen. Stuck detection reads the snapshot's last items in place of the screen. |
| Limits | `limit` events become `flow.Limit` values the daemon adds to the ones `internal/flow` reads, so the limits view shows them. A turn that ends on a rejected limit calls `limitStopped(chat id, ...)` with the reset time. `fireResume` sends "continue" through `Driver.Send` instead of typing into a pane (`resume` gets a `send func(string) error`). |
| Notifications | `model.DecideNotifications` reads Activities, so desktop notifications, the jump-to-attention key, phone push and the Unseen dot work unchanged. `SeePane` with a chat id marks it seen. |
| `pitwall wait`, `ls` | A chat tab's agent state is its Activity; `wait <tab>` works on it. `ls` lists chats in a section of their own. |

<a id="socket"></a>
## Socket messages and CLI

New messages, appended to `proto.Messages`, all at `proto.ChatLevel` (the
Level ENG-2684 adds; `Since` returns it for each). The wire fingerprint
changes with them.

```go
// Client to daemon.

type ChatNew struct {
	SessionID, GroupID string
	Dir                     string // the folder, or the repo a worktree is made from
	Worktree, Base, Branch  string // as model.Task
	Provider                model.Provider
	Model, Effort, Mode     string
	Prompt                  string
	Images                  []string // paths the daemon reads and copies into images/; refused for now
	Attachments             []session.Attachment // aide's, sent with Prompt
	Name                    string   // the tab's name, "" for none
	ParentPane              string   // as NewSession.ParentPane
}

type ChatSend struct {
	Chat   string
	Text   string
	Images []string // refused for now
	Attachments []session.Attachment
	How    string // "" starts a turn or queues behind the running one; "steer"; "queue"
}

type ChatInterrupt struct{ Chat string }

type ChatApprove struct {
	Chat, Request string
	Decision      string // allow, allow-session, deny
	Reason        string
	By            string // who answers, for decisions.jsonl; the daemon sets it for socket clients
}

type ChatAnswer struct {
	Chat, Request string
	Answers       map[string][]string // question id to the chosen labels or typed text
}

type ChatSet struct{ Chat, Model, Effort, Mode string; Provider model.Provider } // "" leaves one as it is; another Provider switches

type ChatQueue struct { // reorder, edit or drop queued messages
	Chat  string
	Queue []session.QueuedMessage // the whole queue as it should be; unknown ids are refused
}

type ChatRead struct {
	Chat  string
	After int64 // 0: a snapshot; else the deltas after seq After, or a snapshot when they are gone
}

type ChatSubscribe struct{ Chat string; After int64 } // ChatRead, then every new delta as ChatDeltas
type ChatUnsubscribe struct{ Chat string }

type ChatFork struct{ Chat, Turn, Worktree string } // ChatFork and ChatRewind are at proto.ChatForkLevel (40)
type ChatRewind struct{ Chat, Turn string }
type ChatArchive struct{ Chat string; Delete bool }
type ChatUnarchive struct{ Chat string }
type ChatList struct{ Archived bool } // archived chats are not in State; this lists them

// Daemon to client.

type ChatResult struct {
	Chat    string
	Turn    string // ChatSend: the turn it started, "" when queued
	Code    string // "" on success; not-found, conflict, busy, unsupported, provider, invalid
	Message string
}

type ChatDeltas struct {
	Chat     string
	Snapshot []byte   // JSON Snapshot when the client needs a new base, else nil
	Seq      int64    // Snapshot's seq
	Deltas   [][]byte // JSON Deltas, in seq order, after Seq
}

type ChatListResult struct{ Chats []model.Chat }
```

Snapshots and deltas cross the socket as their JSON, so the GUI, the CLI
and the web protocol decode one encoding with `internal/session`.

A chat is named by its id, a unique id prefix, or its tab (`#N`, as other
commands take a tab).

```
pitwall chat new [--provider claude|codex] [--model M] [--effort E] [--mode M]
                 [--dir D | --worktree NAME [--base B] | --branch B] [--name N] [--image F]... [-- prompt]
pitwall chat ls [--archived] [--json]
pitwall chat send <chat> [--steer|--queue] [--image F]... -- text
pitwall chat approve <chat> [<request>] allow|allow-session|deny [--reason R]
pitwall chat answer <chat> <request> -- answer...
pitwall chat interrupt <chat>
pitwall chat set <chat> [--model M] [--effort E] [--mode M]
pitwall chat read <chat> [--json] [--follow]
pitwall chat wait <chat> [--until done|blocked] [--timeout D]   # exit codes as pitwall wait
pitwall chat fork <chat> <turn> [--worktree NAME]
pitwall chat rewind <chat> <turn>
pitwall chat archive <chat> [--delete]
pitwall chat unarchive <chat>
```

`approve` without a request answers the only pending one and fails when
there are several. `read` prints the transcript as text: prompts,
assistant text, one line per tool call, approvals with their ids.

<a id="gui"></a>
## GUI

- A chat tab has a sidebar row like any tab: the provider mark, the
  Activity's pill, the hover card.
- Selecting it draws the transcript in the main area from a
  `ChatSubscribe`: user prompts, assistant text, tool calls as one-line
  summaries that expand, reasoning folded, approvals and input requests
  with their buttons (Allow, Allow for session, Deny, a reason field), and
  an "Open in aide" button when aide is installed. No composer beyond a
  single-line send box.
- The agent panel (Ctrl+Shift+L) takes todos from `plan.update`, changes
  from the turn checkpoints, and the timeline from `turns`.
- The GUI keeps one subscription, for the chat on screen; it unsubscribes
  when the tab is not shown. 2,000 items render within the frame budget:
  items are measured once and only the visible ones are laid out.
- ENG-2684 adds a placeholder in place of the transcript ("Chat: read it
  with pitwall chat read <id>"), so a GUI at `ChatLevel` handles a chat tab
  before ENG-2689 lands.
- As built in ENG-2689: no `ChatSubscribe` exists yet, so the window reads
  the chat on screen with `ChatRead` after its last seq every 150 ms while
  the chat is connecting, running or waiting, and on any State change
  otherwise. It keeps only that chat's snapshot. An approval has Allow,
  Allow for session and Deny (`ChatApprove` with `By` "gui"); an input
  request shows the `pitwall chat answer` command instead, and there is no
  reason field or send box. "Open in aide" opens or focuses aide, found as
  aIDE.app on macOS or an `aide.desktop` entry on Linux; aide cannot open
  one chat until its bridge (M6).

<a id="web"></a>
## Web protocol constraints

ENG-2693 wrote [web-protocol.md](web-protocol.md). These parts are fixed here, so the
socket and the web protocol never disagree:

- The session list is `[]model.Chat` as JSON; a session's contents are a
  `Snapshot` with its `seq`, then `Delta`s, exactly as `ChatDeltas` carries
  them. The JSON Schema validates aide's fixtures and pitwall's.
- Every command maps to the same chat-loop operation as the socket message
  of the same name, with the same result codes.
- Resync: the client sends its last `seq`. The loop keeps the last 1,024
  deltas of a chat in memory and sends the ones after it, or a snapshot
  when they are gone. The log on disk is not read for resync.
- Coalescing is the loop's, before numbering ([Delta](#events)). A client
  that falls behind (its queue past 256 messages or 4 MB) has its queue
  dropped and gets a fresh snapshot. No per-client delta merging: it would
  break aide's rule that `seq` steps by one.
- Auth, transport and the listener are ENG-2694's. The phone keeps its
  device tokens; aide uses the token file. Every client answers approvals
  under [first wins](#approvals).
- The daemon serves the aide bundle (ENG-2697) from the same listener; the
  bundle needs nothing from this design beyond the protocol.

<a id="out"></a>
## What stays out

pitwall stays simple. A chat is one agent in one tab, driven by the user.

- **No orchestration engine.** A chat never starts another chat. pitwall
  does not plan, route, retry or split work between agents. Agents start
  other agents the way they do today, with `pitwall new` and tasks, and
  the chat queue holds only the user's own messages, first in, first out.
- **No plugin system.** Drivers are Go packages compiled into pitwall. A
  new agent (ACP, OpenCode) is a new package behind `session.Provider`.
  There is no driver loading, no driver config beyond the command
  override, no scripting hooks into the chat loop.
- **No event sourcing.** The log is one chat's deltas, kept so the chat
  reloads. Nothing projects across chats, no event is replayed through new
  code, and a format change rewrites the snapshot instead of migrating
  events.
- **No chat UI in pitwall's window** beyond the read-only transcript,
  approvals and a send box. aide is the chat UI.
- **No automatic approval**, except aide's Jev tool gate: off by default,
  as in aide, and when on it answers only for chats aide started. Every
  other approval is a person's answer.
- **No reading of provider transcripts** (`~/.claude/projects`, Codex
  rollouts) to rebuild a chat. pitwall's log is the record.
- **No Node helper** and no Agent SDK; the drivers speak the wire from Go.
- **No chats on remote hosts.** A chat runs on the daemon's machine; ssh
  hosts ([remote.md](remote.md)) stay PTY-only.
- **None of aide's extras in the snapshot**: fleet state, multi-agent
  routing, Jev gate overrides, orchestrator tools. They stay aide's.

<a id="open"></a>
## Left open on purpose

These need a capture or a decision that belongs to a later ticket.

1. Claude steering mid-turn: untested; `Steer` is `ErrUnsupported` and the
   message queues until a capture shows it joins the turn.
2. Claude "allow for session" through `updatedPermissions`: written from
   `sdk.d.ts`, never captured.
3. A Claude `rate_limit_event` with `status: rejected` and the `result`
   that goes with it: never captured. ENG-2688 adds the fixture when one
   happens, and the screen-based reading stays the fallback for panes.
4. Codex deny with a reason: the decision has no field; the driver steers
   "I declined that request: <reason>" in after `decline`. ENG-2685 tests
   it against a recorded transcript only; whether the model acts on it is
   unchecked against a live Codex.
5. Whether `codex app-server` runs pitwall's Codex hooks: ENG-2681 checks.
   If it does not, the guard does not cover Codex chats, and the chat's
   tab says so on its hover card.
6. `paneId` and `workspaceId` in the snapshot are pitwall's ids; aide's
   bridge (M6, ENG-2704) maps them to aide's.
7. Idle processes: a live chat keeps its process until archive or daemon
   exit. Closing processes idle for a long time can come later if memory
   matters.
8. Images inline in the log: fine within aide's limits; move bytes out to
   `images/` only if logs grow large.
9. `--resume` on a Claude session another process holds open, and macOS
   and Windows runs: untested; ENG-2687 and the platform CI runs cover
   them.

<a id="tickets"></a>
## Ticket map

| Ticket | Builds | Sections |
| --- | --- | --- |
| [ENG-2679](https://linear.app/quantic/issue/ENG-2679) event model | `internal/session`: `Event`, `Delta`, `Snapshot`, `Reduce`, `Apply`, fixtures | [Event model](#events), [Names](#names) |
| [ENG-2680](https://linear.app/quantic/issue/ENG-2680) event log store | `internal/session/eventlog` | [Storage](#storage), [Lifecycle](#lifecycle) (archive, delete) |
| [ENG-2681](https://linear.app/quantic/issue/ENG-2681) Codex driver | `internal/session/codex`: start, turns, streaming, crash restart | [Codex driver](#codex), [Driver interface](#driver) |
| [ENG-2682](https://linear.app/quantic/issue/ENG-2682) Claude driver | `internal/session/claude`: start, turns, streaming | [Claude driver](#claude), [Driver interface](#driver) |
| [ENG-2683](https://linear.app/quantic/issue/ENG-2683) fake providers | `internal/session/fake`, `PITWALL_FAKE_PROVIDERS` | [Fake providers](#fake) |
| [ENG-2684](https://linear.app/quantic/issue/ENG-2684) socket and CLI | `model.Chat`, `Workspace.Chat`, `proto.Chat*`, `pitwall chat`, the chat loop | [Placement](#placement), [Lifecycle](#lifecycle), [Socket and CLI](#socket) |
| [ENG-2685](https://linear.app/quantic/issue/ENG-2685) Codex approvals and interrupt | approvals, input, interrupt, plan updates for Codex | [Codex driver](#codex), [Approvals](#approvals), [Lifecycle](#lifecycle) |
| [ENG-2686](https://linear.app/quantic/issue/ENG-2686) Claude approvals and interrupt | `can_use_tool`, plan mode, interrupt, steering for Claude | [Claude driver](#claude), [Approvals](#approvals), [Lifecycle](#lifecycle) |
| [ENG-2687](https://linear.app/quantic/issue/ENG-2687) resume | lazy resume, fallback | [Resume](#resume) |
| [ENG-2688](https://linear.app/quantic/issue/ENG-2688) attachments | worktree, ports, checkpoints, triage, limits, notifications | [Attach](#attach), [Placement](#placement) (Activity) |
| [ENG-2689](https://linear.app/quantic/issue/ENG-2689) GUI | sidebar row, transcript view, agent panel | [GUI](#gui), [Placement](#placement) |
| [ENG-2690](https://linear.app/quantic/issue/ENG-2690) reconcile | load-time and exit-time repair, continue setting | [Reconciliation](#reconcile) |
| [ENG-2691](https://linear.app/quantic/issue/ENG-2691) fork and rewind | `Origin`, `Points`, file restore | [Fork and rewind](#fork-rewind) |
| [ENG-2692](https://linear.app/quantic/issue/ENG-2692) phone approvals | the phone page's chat list and answers | [Approvals](#approvals), [Attach](#attach) (notifications) |
| [ENG-2693](https://linear.app/quantic/issue/ENG-2693) web protocol spec | `docs/web-protocol.md`, JSON Schema | [Web protocol](#web), [Event model](#events) |
| [ENG-2694](https://linear.app/quantic/issue/ENG-2694) localhost listener and auth | the listener, token file | [Web protocol](#web) |
| [ENG-2695](https://linear.app/quantic/issue/ENG-2695) snapshots and deltas to clients | the fanout, ring buffer, slow clients | [Web protocol](#web), [Event model](#events) (coalescing) |
| [ENG-2696](https://linear.app/quantic/issue/ENG-2696) client commands | web command handlers | [Web protocol](#web), [Socket and CLI](#socket), [Approvals](#approvals) |
| [ENG-2697](https://linear.app/quantic/issue/ENG-2697) serve the aide bundle | static serving | [Web protocol](#web) |
| [ENG-2698](https://linear.app/quantic/issue/ENG-2698) conformance fixtures | `testdata/web-protocol/` | [Fake providers](#fake), [Event model](#events) |

ENG-2712 adds [`internal/session/opencode`](../internal/session/opencode/README.md)
behind [the driver interface](#driver). It uses OpenCode v1.19.4's HTTP
server and SSE events, including permissions and session-ID resume. Its
fake-server restart test covers a fresh driver reattaching to a saved ID;
the daemon's lazy restart loop remains ENG-2687.

Later tickets that build on the same contract: ENG-2711 (ACP) adds a driver;
ENG-2707
imports aide's old sessions as archived chats in [Storage](#storage)'s
layout.
