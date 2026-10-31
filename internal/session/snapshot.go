package session

import (
	"bytes"
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/quanticstudios/pitwall/internal/model"
)

// Snapshot is aide's ManagedSessionSnapshot. Fields aide marks optional
// and pitwall never sets (fleet, agents, jevGates, orchestratorSelection,
// pendingProviderSelection, preparingActivityId) are not fields; a
// snapshot or session.patch from aide that carries them keeps them in
// Extra, so they round-trip.
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

	// Extra holds the keys of aide's snapshot that pitwall has no field for.
	Extra map[string]json.RawMessage `json:"-"`
}

// snapshotKeys are the JSON keys Snapshot has fields for.
var snapshotKeys = func() map[string]bool {
	keys := map[string]bool{}
	t := reflect.TypeFor[Snapshot]()
	for i := range t.NumField() {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ","); name != "" && name != "-" {
			keys[name] = true
		}
	}
	return keys
}()

type snapshotFields Snapshot

func (s Snapshot) MarshalJSON() ([]byte, error) {
	// aide reads these as arrays, never null.
	s.Content = nonNil(s.Content)
	s.Turns = nonNil(s.Turns)
	s.PendingApprovals = nonNil(s.PendingApprovals)
	s.PendingUserInputs = nonNil(s.PendingUserInputs)
	b, err := json.Marshal(snapshotFields(s))
	if err != nil || len(s.Extra) == 0 {
		return b, err
	}
	var buf bytes.Buffer
	buf.Write(b[:len(b)-1])
	for _, k := range slices.Sorted(maps.Keys(s.Extra)) {
		if snapshotKeys[k] {
			continue
		}
		key, _ := json.Marshal(k)
		buf.WriteByte(',')
		buf.Write(key)
		buf.WriteByte(':')
		buf.Write(s.Extra[k])
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func (s *Snapshot) UnmarshalJSON(b []byte) error {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return err
	}
	var f snapshotFields
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	f.Extra = nil
	for k, v := range all {
		if !snapshotKeys[k] {
			if f.Extra == nil {
				f.Extra = map[string]json.RawMessage{}
			}
			f.Extra[k] = v
		}
	}
	*s = Snapshot(f)
	return nil
}

// Item is aide's ManagedSessionContentItem: one entry of the transcript.
type Item struct {
	Attachments []Attachment    `json:"attachments,omitzero"`
	CreatedAt   string          `json:"createdAt,omitempty"` // the item's first event; absent on older items
	ID          string          `json:"id"`
	Kind        ItemKind        `json:"kind"`
	Phase       string          `json:"phase,omitempty"`
	Model       *string         `json:"model,omitempty"`
	Provider    *model.Provider `json:"provider"`
	Text        string          `json:"text"`
	Tool        *ToolCall       `json:"tool,omitempty"` // tool-call items only
	TurnID      *string         `json:"turnId"`
	UpdatedAt   string          `json:"updatedAt"`
}

// Attachment is aide's ManagedSessionAttachment: an image (Data, MimeType,
// Name, SizeBytes) or a workspace file (Path).
type Attachment struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"` // image, workspace-file
	Data      string `json:"data,omitempty"`
	MimeType  string `json:"mimeType,omitempty"`
	Name      string `json:"name,omitempty"`
	SizeBytes int64  `json:"sizeBytes,omitempty"`
	Path      string `json:"path,omitempty"`
}

// ToolCall is aide's ManagedProviderRuntimeToolCall.
type ToolCall struct {
	CallID    string          `json:"callId"`
	Command   string          `json:"command,omitempty"`
	Diff      string          `json:"diff,omitempty"`
	EndedAt   string          `json:"endedAt,omitempty"` // set by tool.end
	ExitCode  *int            `json:"exitCode,omitempty"`
	Files     []ToolFile      `json:"files,omitzero"`
	Input     json.RawMessage `json:"input"`
	Output    string          `json:"output,omitempty"`
	Progress  string          `json:"progress,omitempty"`  // a one-line status while the call runs
	StartedAt string          `json:"startedAt,omitempty"` // set by tool.end
	Status    string          `json:"status"`              // running, completed, failed
	Tasks     []Task          `json:"tasks,omitzero"`      // task-list calls only
	ToolName  string          `json:"toolName"`
}

type ToolFile struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

// Task is aide's ManagedProviderRuntimeTask.
type Task struct {
	Status  string `json:"status"` // pending, in_progress, completed
	Subject string `json:"subject"`
}

// TurnSummary is aide's ManagedSessionTurnSummary. CheckpointID is
// pitwall's turn checkpoint commit.
type TurnSummary struct {
	CheckpointID   string         `json:"checkpointId,omitempty"`
	RecipientAlias string         `json:"recipientAlias,omitempty"`
	RequestText    string         `json:"requestText,omitempty"`
	CompletedAt    *string        `json:"completedAt"`
	ID             string         `json:"id"`
	Provider       model.Provider `json:"provider"`
	StartedAt      string         `json:"startedAt"`
	State          string         `json:"state"`
}

// Approval is aide's ManagedSessionPendingApproval.
type Approval struct {
	Detail      *string      `json:"detail"`
	ID          string       `json:"id"`
	Request     *ToolRequest `json:"request,omitempty"`
	RequestType RequestType  `json:"requestType"`
}

// PlanApproval is aide's ManagedSessionPlanApproval.
type PlanApproval struct {
	Detail *string `json:"detail"`
	ID     string  `json:"id"`
}

// ToolRequest is aide's ManagedProviderRuntimeToolRequest: the tool call
// behind an approval.
type ToolRequest struct {
	Command  string          `json:"command,omitempty"`
	Cwd      string          `json:"cwd,omitempty"`
	Diff     string          `json:"diff,omitempty"`
	FilePath string          `json:"filePath,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
	ToolName string          `json:"toolName"`
}

// InputRequest is aide's ManagedSessionPendingUserInput.
type InputRequest struct {
	Detail    *string    `json:"detail"`
	ID        string     `json:"id"`
	Questions []Question `json:"questions"`
}

// Question is aide's ManagedSessionUserQuestion.
type Question struct {
	Header  string         `json:"header,omitempty"`
	ID      string         `json:"id"`
	Options []PromptOption `json:"options"`
	Prompt  string         `json:"prompt"`
}

type PromptOption struct {
	Description string `json:"description,omitempty"`
	ID          string `json:"id"`
	Label       string `json:"label"`
}

// Usage is aide's ManagedSessionUsage.
type Usage struct {
	CachedInputTokens *int64 `json:"cachedInputTokens,omitempty"`
	ContextTokens     *int64 `json:"contextTokens,omitempty"`
	InputTokens       int64  `json:"inputTokens"`
	MaxTokens         *int64 `json:"maxTokens,omitempty"`
	OutputTokens      int64  `json:"outputTokens"`
}

// UsageReport is aide's ManagedUsageReport. Provider is claude, codex or
// jev. RuntimeGeneration is absent in an event's report; Reduce fills it.
type UsageReport struct {
	ID                string       `json:"id"`
	ScopeID           string       `json:"scopeId"`
	Provider          string       `json:"provider"`
	RuntimeGeneration int          `json:"runtimeGeneration,omitempty"`
	TurnID            *string      `json:"turnId"`
	CreatedAt         string       `json:"createdAt"`
	Models            []ModelUsage `json:"models"`
	EstimatedCostUSD  *float64     `json:"estimatedCostUsd"`
}

// ModelUsage is aide's ManagedModelUsage.
type ModelUsage struct {
	Model                    *string  `json:"model"`
	InputTokens              int64    `json:"inputTokens"`
	OutputTokens             int64    `json:"outputTokens"`
	CachedInputTokens        *int64   `json:"cachedInputTokens"`
	CacheCreationInputTokens *int64   `json:"cacheCreationInputTokens"`
	EstimatedCostUSD         *float64 `json:"estimatedCostUsd"`
}

// UsageSummary is aide's ManagedUsageSummary.
type UsageSummary struct {
	InputTokens              int64               `json:"inputTokens"`
	OutputTokens             int64               `json:"outputTokens"`
	CachedInputTokens        *int64              `json:"cachedInputTokens"`
	CacheCreationInputTokens *int64              `json:"cacheCreationInputTokens"`
	EstimatedCostUSD         float64             `json:"estimatedCostUsd"`
	IsCostComplete           bool                `json:"isCostComplete"`
	Models                   []UsageSummaryModel `json:"models"`
}

type UsageSummaryModel struct {
	ModelUsage
	Provider string `json:"provider"`
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
