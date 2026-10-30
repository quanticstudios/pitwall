// Package session is the event model for managed sessions (chats): what a
// driver reports (Event), the changes the daemon numbers and sends (Delta),
// and the state they build (Snapshot). The JSON shapes are aide's
// (src/shared/session-delta.ts and its fixtures), so aide reads pitwall's
// chats unchanged. docs/managed-sessions.md is the design.
package session

import (
	"encoding/json"
	"time"
)

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

// Session states, aide's ManagedProviderRuntimeSessionState.
const (
	StateConnecting = "connecting"
	StateReady      = "ready"
	StateRunning    = "running"
	StateWaiting    = "waiting"
	StateStopped    = "stopped"
	StateError      = "error"
)

// Turn states, aide's ManagedSessionTurnState. A driver reports only the
// last four; running is the state of a turn that has not ended.
const (
	TurnRunning     = "running"
	TurnCompleted   = "completed"
	TurnFailed      = "failed"
	TurnCancelled   = "cancelled"
	TurnInterrupted = "interrupted"
)

// ItemKind is aide's ManagedSessionContentKind.
type ItemKind string

const (
	KindAssistantText        ItemKind = "assistant-text"
	KindReasoningText        ItemKind = "reasoning-text"
	KindReasoningSummaryText ItemKind = "reasoning-summary-text"
	KindCommandOutput        ItemKind = "command-output"
	KindFileChangeOutput     ItemKind = "file-change-output"
	KindSystem               ItemKind = "system"
	KindToolCall             ItemKind = "tool-call"
	KindUserPrompt           ItemKind = "user-prompt" // the chat loop's, never a driver's
)

// RequestType is aide's ManagedSessionRequestType.
type RequestType string

const (
	RequestCommand       RequestType = "command-execution-approval"
	RequestFileChange    RequestType = "file-change-approval"
	RequestFileRead      RequestType = "file-read-approval"
	RequestPlan          RequestType = "plan-approval"
	RequestDynamicTool   RequestType = "dynamic-tool-call"
	RequestToolUserInput RequestType = "tool-user-input"
	RequestUnknown       RequestType = "unknown"
)

// Event is one thing a driver saw. JSON tags match aide's runtime events,
// so a fixture's runtimeEvents decode into []Event. Only the fields of
// Type are set.
type Event struct {
	Type      EventType `json:"type"`
	CreatedAt string    `json:"createdAt"` // Stamp(time)

	State  string `json:"state,omitempty"` // session.state.changed: a session state; turn.completed: a turn state
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
	Capabilities []string `json:"capabilities,omitempty"` // the CLI's reported capabilities
}

type eventFields Event

// MarshalJSON writes the keys aide requires for e's type even when they are
// zero: content.delta's text, the token counts of a usage update and a
// killed process's null exitCode.
func (e Event) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(eventFields(e))
	if err != nil {
		return nil, err
	}
	add := func(key, value string) {
		b = append(b[:len(b)-1], `,"`+key+`":`+value+`}`...)
	}
	switch {
	case e.Type == EventContent && e.Text == "":
		add("text", `""`)
	case e.Type == EventUsage:
		if e.InputTokens == 0 {
			add("inputTokens", "0")
		}
		if e.OutputTokens == 0 {
			add("outputTokens", "0")
		}
	case e.Type == EventExited && e.ExitCode == nil:
		add("exitCode", "null")
	}
	return b, nil
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
