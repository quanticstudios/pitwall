package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
)

// DeltaVersion is aide's SESSION_DELTA_VERSION.
const DeltaVersion = 1

// DeltaType names one of aide's SessionDelta shapes. What each does to a
// snapshot is the table in testdata/session-deltas/README.md (aide's).
type DeltaType string

const (
	DeltaSnapshot         DeltaType = "snapshot"
	DeltaItemAdd          DeltaType = "item.add"
	DeltaItemTextAppend   DeltaType = "item.text.append"
	DeltaItemPatch        DeltaType = "item.patch"
	DeltaToolStart        DeltaType = "tool.start"
	DeltaToolOutputAppend DeltaType = "tool.output.append"
	DeltaToolEnd          DeltaType = "tool.end"
	DeltaReasoningAppend  DeltaType = "reasoning.append"
	DeltaPlanUpdate       DeltaType = "plan.update"
	DeltaApprovalAdd      DeltaType = "approval.add"
	DeltaApprovalResolve  DeltaType = "approval.resolve"
	DeltaInputAdd         DeltaType = "input.add"
	DeltaInputResolve     DeltaType = "input.resolve"
	DeltaTurn             DeltaType = "turn"
	DeltaStatus           DeltaType = "status"
	DeltaLimit            DeltaType = "limit"
	DeltaUsage            DeltaType = "usage"
	DeltaUsageReports     DeltaType = "usage.reports"
	DeltaError            DeltaType = "error"
	DeltaSessionPatch     DeltaType = "session.patch"
	DeltaLive             DeltaType = "live" // pitwall's: Live, the snapshot's whole Live
)

// Delta is aide's SessionDelta: the envelope and the fields of every type,
// flat. MarshalJSON writes the envelope and only Type's fields, with null
// where aide's type is `T | null`; json.Unmarshal reads any of them.
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
	Patch         json.RawMessage `json:"patch"`  // item.patch: a partial Item
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

	Limit        *UsageLimit   `json:"limit"`
	Usage        *Usage        `json:"usage"`
	UsageReports []UsageReport `json:"usageReports"`
	UsageSummary *UsageSummary `json:"usageSummary"`
	LastError    *string       `json:"lastError"`
	LastErrorAt  *string       `json:"lastErrorAt"`

	Set   json.RawMessage `json:"set"`   // session.patch: a partial Snapshot of patchable keys
	Unset []string        `json:"unset"` // session.patch

	Live []Item `json:"live"`
}

type deltaEnvelope struct {
	Version   int       `json:"version"`
	SessionID string    `json:"sessionId"`
	Seq       int64     `json:"seq"`
	Revision  int64     `json:"revision"`
	UpdatedAt string    `json:"updatedAt"`
	Type      DeltaType `json:"type"`
}

func (d Delta) MarshalJSON() ([]byte, error) {
	env := deltaEnvelope{
		Version:   d.Version,
		SessionID: d.SessionID,
		Seq:       d.Seq,
		Revision:  d.Revision,
		UpdatedAt: d.UpdatedAt,
		Type:      d.Type,
	}
	switch d.Type {
	case DeltaSnapshot:
		return json.Marshal(struct {
			deltaEnvelope
			Snapshot *Snapshot `json:"snapshot"`
		}{env, d.Snapshot})
	case DeltaItemAdd, DeltaToolStart:
		return json.Marshal(struct {
			deltaEnvelope
			Item *Item `json:"item"`
		}{env, d.Item})
	case DeltaItemTextAppend, DeltaReasoningAppend, DeltaToolOutputAppend:
		return json.Marshal(struct {
			deltaEnvelope
			ItemID        string `json:"itemId"`
			Text          string `json:"text"`
			ItemUpdatedAt string `json:"itemUpdatedAt"`
		}{env, d.ItemID, d.Text, d.ItemUpdatedAt})
	case DeltaItemPatch:
		return json.Marshal(struct {
			deltaEnvelope
			ItemID string          `json:"itemId"`
			Patch  json.RawMessage `json:"patch"`
		}{env, d.ItemID, rawObject(d.Patch)})
	case DeltaToolEnd:
		return json.Marshal(struct {
			deltaEnvelope
			ItemID    string `json:"itemId"`
			Status    string `json:"status"`
			ExitCode  *int   `json:"exitCode,omitempty"`
			StartedAt string `json:"startedAt"`
			EndedAt   string `json:"endedAt"`
		}{env, d.ItemID, d.Status, d.ExitCode, d.StartedAt, d.EndedAt})
	case DeltaPlanUpdate:
		return json.Marshal(struct {
			deltaEnvelope
			ItemID        string `json:"itemId"`
			Tasks         []Task `json:"tasks"`
			ItemUpdatedAt string `json:"itemUpdatedAt"`
		}{env, d.ItemID, nonNil(d.Tasks), d.ItemUpdatedAt})
	case DeltaApprovalAdd:
		return json.Marshal(struct {
			deltaEnvelope
			Approval *Approval `json:"approval"`
		}{env, d.Approval})
	case DeltaInputAdd:
		return json.Marshal(struct {
			deltaEnvelope
			Input *InputRequest `json:"input"`
		}{env, d.Input})
	case DeltaApprovalResolve, DeltaInputResolve:
		return json.Marshal(struct {
			deltaEnvelope
			RequestID string `json:"requestId"`
		}{env, d.RequestID})
	case DeltaTurn:
		return json.Marshal(struct {
			deltaEnvelope
			Turn *TurnSummary `json:"turn"`
		}{env, d.Turn})
	case DeltaStatus:
		return json.Marshal(struct {
			deltaEnvelope
			Status          string  `json:"status"`
			SessionState    string  `json:"sessionState"`
			Running         bool    `json:"running"`
			Detail          *string `json:"detail"`
			LastCompletedAt *string `json:"lastCompletedAt"`
		}{env, d.Status, d.SessionState, d.Running, d.Detail, d.LastCompletedAt})
	case DeltaLimit:
		return json.Marshal(struct {
			deltaEnvelope
			Limit *UsageLimit `json:"limit"`
		}{env, d.Limit})
	case DeltaUsage:
		return json.Marshal(struct {
			deltaEnvelope
			Usage *Usage `json:"usage"`
		}{env, d.Usage})
	case DeltaUsageReports:
		return json.Marshal(struct {
			deltaEnvelope
			UsageReports []UsageReport `json:"usageReports"`
			UsageSummary *UsageSummary `json:"usageSummary"`
		}{env, nonNil(d.UsageReports), d.UsageSummary})
	case DeltaError:
		return json.Marshal(struct {
			deltaEnvelope
			LastError   *string `json:"lastError"`
			LastErrorAt *string `json:"lastErrorAt"`
		}{env, d.LastError, d.LastErrorAt})
	case DeltaLive:
		return json.Marshal(struct {
			deltaEnvelope
			Live []Item `json:"live"`
		}{env, nonNil(d.Live)})
	case DeltaSessionPatch:
		if d.Unset != nil {
			return json.Marshal(struct {
				deltaEnvelope
				Set   json.RawMessage `json:"set"`
				Unset []string        `json:"unset"`
			}{env, rawObject(d.Set), d.Unset})
		}
		return json.Marshal(struct {
			deltaEnvelope
			Set json.RawMessage `json:"set"`
		}{env, rawObject(d.Set)})
	default:
		return json.Marshal(env)
	}
}

// rawObject is r, or {} when r is empty.
func rawObject(r json.RawMessage) json.RawMessage {
	if len(r) == 0 {
		return json.RawMessage("{}")
	}
	return r
}

// ResyncError reasons are aide's: version, no-base, session, seq-gap, item.
// Each means the consumer should ask for a fresh snapshot.
type ResyncError struct{ Reason, Message string }

func (e *ResyncError) Error() string { return "session: resync (" + e.Reason + "): " + e.Message }

// Apply is aide's applyDelta: it returns the snapshot after d, given s and
// the seq of the last delta applied to it, or a *ResyncError when d cannot
// apply. With a nil s it accepts only a snapshot delta. A delta missing the
// payload its type needs is an error too. It never changes s.
func Apply(s *Snapshot, seq int64, d Delta) (Snapshot, error) {
	if d.Version != DeltaVersion {
		return Snapshot{}, &ResyncError{"version", fmt.Sprintf("unsupported session delta version %d", d.Version)}
	}
	if d.Type == DeltaSnapshot {
		if d.Snapshot == nil {
			return Snapshot{}, fmt.Errorf("session: snapshot delta %d has no snapshot", d.Seq)
		}
		return *d.Snapshot, nil
	}
	if s == nil {
		return Snapshot{}, &ResyncError{"no-base", fmt.Sprintf("delta %d arrived before any snapshot", d.Seq)}
	}
	if d.SessionID != s.SessionID {
		return Snapshot{}, &ResyncError{"session", fmt.Sprintf("delta for %s applied to %s", d.SessionID, s.SessionID)}
	}
	if d.Seq != seq+1 {
		return Snapshot{}, &ResyncError{"seq-gap", fmt.Sprintf("expected seq %d, got %d", seq+1, d.Seq)}
	}

	n := *s
	n.Revision = d.Revision
	n.UpdatedAt = d.UpdatedAt
	missing := func(what string) (Snapshot, error) {
		return Snapshot{}, fmt.Errorf("session: %s delta %d has no %s", d.Type, d.Seq, what)
	}
	updateItem := func(update func(*Item) error) error {
		i := -1
		for j := len(s.Content) - 1; j >= 0; j-- {
			if s.Content[j].ID == d.ItemID {
				i = j
				break
			}
		}
		if i < 0 {
			return &ResyncError{"item", fmt.Sprintf("delta %d names unknown item %s", d.Seq, d.ItemID)}
		}
		it := s.Content[i]
		if err := update(&it); err != nil {
			return err
		}
		n.Content = slices.Clone(s.Content)
		n.Content[i] = it
		return nil
	}
	updateTool := func(updatedAt string, update func(*ToolCall)) error {
		return updateItem(func(it *Item) error {
			if it.Tool == nil {
				return &ResyncError{"item", fmt.Sprintf("delta %d names item %s, which has no tool call", d.Seq, d.ItemID)}
			}
			t := *it.Tool
			update(&t)
			it.Tool = &t
			it.UpdatedAt = updatedAt
			return nil
		})
	}

	var err error
	switch d.Type {
	case DeltaItemAdd, DeltaToolStart:
		if d.Item == nil {
			return missing("item")
		}
		n.Content = append(slices.Clip(s.Content), *d.Item)
	case DeltaItemTextAppend, DeltaReasoningAppend:
		err = updateItem(func(it *Item) error {
			it.Text += d.Text
			it.UpdatedAt = d.ItemUpdatedAt
			return nil
		})
	case DeltaItemPatch:
		err = updateItem(func(it *Item) error { return mergeJSON(it, d.Patch, nil) })
	case DeltaToolOutputAppend:
		err = updateTool(d.ItemUpdatedAt, func(t *ToolCall) { t.Output += d.Text })
	case DeltaToolEnd:
		err = updateTool(d.EndedAt, func(t *ToolCall) {
			if d.ExitCode != nil {
				t.ExitCode = d.ExitCode
			}
			t.Status, t.StartedAt, t.EndedAt = d.Status, d.StartedAt, d.EndedAt
		})
	case DeltaPlanUpdate:
		err = updateTool(d.ItemUpdatedAt, func(t *ToolCall) { t.Tasks = d.Tasks })
	case DeltaApprovalAdd:
		if d.Approval == nil {
			return missing("approval")
		}
		n.PendingApprovals = append(slices.Clip(s.PendingApprovals), *d.Approval)
		n.PlanApproval = planApproval(n.PendingApprovals)
	case DeltaApprovalResolve:
		n.PendingApprovals = slices.DeleteFunc(slices.Clone(s.PendingApprovals), func(a Approval) bool { return a.ID == d.RequestID })
		n.PlanApproval = planApproval(n.PendingApprovals)
	case DeltaInputAdd:
		if d.Input == nil {
			return missing("input")
		}
		n.PendingUserInputs = append(slices.Clip(s.PendingUserInputs), *d.Input)
	case DeltaInputResolve:
		n.PendingUserInputs = slices.DeleteFunc(slices.Clone(s.PendingUserInputs), func(in InputRequest) bool { return in.ID == d.RequestID })
	case DeltaTurn:
		if d.Turn == nil {
			return missing("turn")
		}
		i := -1
		for j := len(s.Turns) - 1; j >= 0; j-- {
			if s.Turns[j].ID == d.Turn.ID {
				i = j
				break
			}
		}
		if i >= 0 {
			n.Turns = slices.Clone(s.Turns)
			n.Turns[i] = *d.Turn
		} else {
			n.Turns = append(slices.Clip(s.Turns), *d.Turn)
		}
	case DeltaStatus:
		n.SessionState = d.SessionState
		n.Running = d.Running
		n.Detail = d.Detail
		n.LastCompletedAt = d.LastCompletedAt
	case DeltaLimit:
		n.UsageLimit = d.Limit
	case DeltaUsage:
		n.Usage = d.Usage
	case DeltaUsageReports:
		n.UsageReports = d.UsageReports
		n.UsageSummary = d.UsageSummary
	case DeltaError:
		n.LastError = d.LastError
		n.LastErrorAt = d.LastErrorAt
	case DeltaLive:
		n.Live = nil
		if len(d.Live) > 0 {
			n.Live = d.Live
		}
	case DeltaSessionPatch:
		if !isEmptyObject(d.Set) || len(d.Unset) > 0 {
			err = patchSnapshot(&n, d.Set, d.Unset)
		}
	}
	// Any other type is newer than this schema: like aide, only the
	// revision and updatedAt move.
	if err != nil {
		return Snapshot{}, err
	}
	return n, nil
}

// patchSnapshot applies set and unset directly to Snapshot fields without
// re-marshaling untouched fields such as Content.
func patchSnapshot(n *Snapshot, set json.RawMessage, unset []string) error {
	extraCloned := false
	cloneExtra := func() {
		if !extraCloned {
			n.Extra = maps.Clone(n.Extra)
			extraCloned = true
		}
	}
	if !isEmptyObject(set) {
		var patch map[string]json.RawMessage
		if err := json.Unmarshal(set, &patch); err != nil {
			return fmt.Errorf("session: patch is not an object: %w", err)
		}
		for k, val := range patch {
			if err := setSnapshotField(n, k, val, cloneExtra); err != nil {
				return err
			}
		}
	}
	for _, k := range unset {
		unsetSnapshotField(n, k, cloneExtra)
	}
	return nil
}

func setSnapshotField(n *Snapshot, k string, val json.RawMessage, cloneExtra func()) error {
	switch k {
	case "sessionId":
		n.SessionID = ""
		return json.Unmarshal(val, &n.SessionID)
	case "paneId":
		n.PaneID = ""
		return json.Unmarshal(val, &n.PaneID)
	case "workspaceId":
		n.WorkspaceID = ""
		return json.Unmarshal(val, &n.WorkspaceID)
	case "provider":
		n.Provider = ""
		return json.Unmarshal(val, &n.Provider)
	case "content":
		n.Content = nil
		return json.Unmarshal(val, &n.Content)
	case "turns":
		n.Turns = nil
		return json.Unmarshal(val, &n.Turns)
	case "pendingApprovals":
		n.PendingApprovals = nil
		return json.Unmarshal(val, &n.PendingApprovals)
	case "pendingUserInputs":
		n.PendingUserInputs = nil
		return json.Unmarshal(val, &n.PendingUserInputs)
	case "planApproval":
		n.PlanApproval = nil
		return json.Unmarshal(val, &n.PlanApproval)
	case "sessionState":
		n.SessionState = ""
		return json.Unmarshal(val, &n.SessionState)
	case "running":
		n.Running = false
		return json.Unmarshal(val, &n.Running)
	case "detail":
		n.Detail = nil
		return json.Unmarshal(val, &n.Detail)
	case "lastCompletedAt":
		n.LastCompletedAt = nil
		return json.Unmarshal(val, &n.LastCompletedAt)
	case "lastError":
		n.LastError = nil
		return json.Unmarshal(val, &n.LastError)
	case "lastErrorAt":
		n.LastErrorAt = nil
		return json.Unmarshal(val, &n.LastErrorAt)
	case "model":
		n.Model = nil
		return json.Unmarshal(val, &n.Model)
	case "effort":
		n.Effort = nil
		return json.Unmarshal(val, &n.Effort)
	case "serviceTier":
		n.ServiceTier = nil
		return json.Unmarshal(val, &n.ServiceTier)
	case "permissionMode":
		n.PermissionMode = ""
		return json.Unmarshal(val, &n.PermissionMode)
	case "interactionMode":
		n.InteractionMode = ""
		return json.Unmarshal(val, &n.InteractionMode)
	case "selectionNotice":
		n.SelectionNotice = nil
		return json.Unmarshal(val, &n.SelectionNotice)
	case "runtimeGeneration":
		n.RuntimeGeneration = 0
		return json.Unmarshal(val, &n.RuntimeGeneration)
	case "revision":
		n.Revision = 0
		return json.Unmarshal(val, &n.Revision)
	case "updatedAt":
		n.UpdatedAt = ""
		return json.Unmarshal(val, &n.UpdatedAt)
	case "queuedInputs":
		n.QueuedInputs = nil
		return json.Unmarshal(val, &n.QueuedInputs)
	case "queuePaused":
		n.QueuePaused = false
		return json.Unmarshal(val, &n.QueuePaused)
	case "queueReason":
		n.QueueReason = ""
		return json.Unmarshal(val, &n.QueueReason)
	case "usageLimit":
		n.UsageLimit = nil
		return json.Unmarshal(val, &n.UsageLimit)
	case "usage":
		n.Usage = nil
		return json.Unmarshal(val, &n.Usage)
	case "usageReports":
		n.UsageReports = nil
		return json.Unmarshal(val, &n.UsageReports)
	case "usageSummary":
		n.UsageSummary = nil
		return json.Unmarshal(val, &n.UsageSummary)
	case "commands":
		n.Commands = nil
		return json.Unmarshal(val, &n.Commands)
	case "live":
		n.Live = nil
		return json.Unmarshal(val, &n.Live)
	default:
		cloneExtra()
		if n.Extra == nil {
			n.Extra = map[string]json.RawMessage{}
		}
		n.Extra[k] = slices.Clone(val)
		return nil
	}
}

func unsetSnapshotField(n *Snapshot, k string, cloneExtra func()) {
	switch k {
	case "sessionId":
		n.SessionID = ""
	case "paneId":
		n.PaneID = ""
	case "workspaceId":
		n.WorkspaceID = ""
	case "provider":
		n.Provider = ""
	case "content":
		n.Content = nil
	case "turns":
		n.Turns = nil
	case "pendingApprovals":
		n.PendingApprovals = nil
	case "pendingUserInputs":
		n.PendingUserInputs = nil
	case "planApproval":
		n.PlanApproval = nil
	case "sessionState":
		n.SessionState = ""
	case "running":
		n.Running = false
	case "detail":
		n.Detail = nil
	case "lastCompletedAt":
		n.LastCompletedAt = nil
	case "lastError":
		n.LastError = nil
	case "lastErrorAt":
		n.LastErrorAt = nil
	case "model":
		n.Model = nil
	case "effort":
		n.Effort = nil
	case "serviceTier":
		n.ServiceTier = nil
	case "permissionMode":
		n.PermissionMode = ""
	case "interactionMode":
		n.InteractionMode = ""
	case "selectionNotice":
		n.SelectionNotice = nil
	case "runtimeGeneration":
		n.RuntimeGeneration = 0
	case "revision":
		n.Revision = 0
	case "updatedAt":
		n.UpdatedAt = ""
	case "queuedInputs":
		n.QueuedInputs = nil
	case "queuePaused":
		n.QueuePaused = false
	case "queueReason":
		n.QueueReason = ""
	case "usageLimit":
		n.UsageLimit = nil
	case "usage":
		n.Usage = nil
	case "usageReports":
		n.UsageReports = nil
	case "usageSummary":
		n.UsageSummary = nil
	case "commands":
		n.Commands = nil
	case "live":
		n.Live = nil
	default:
		if _, ok := n.Extra[k]; ok {
			cloneExtra()
			delete(n.Extra, k)
			if len(n.Extra) == 0 {
				n.Extra = nil
			}
		}
	}
}

// mergeJSON shallow-merges the JSON object set into *v and deletes the
// keys in unset, as aide's object spread and delete do. It decodes into a
// fresh value, so slices *v shares with an older snapshot stay untouched.
func mergeJSON[T any](v *T, set json.RawMessage, unset []string) error {
	cur, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(cur, &obj); err != nil {
		return err
	}
	if !isEmptyObject(set) {
		var patch map[string]json.RawMessage
		if err := json.Unmarshal(set, &patch); err != nil {
			return fmt.Errorf("session: patch is not an object: %w", err)
		}
		for k, val := range patch {
			obj[k] = val
		}
	}
	for _, k := range unset {
		delete(obj, k)
	}
	merged, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	var fresh T
	if err := json.Unmarshal(merged, &fresh); err != nil {
		return err
	}
	*v = fresh
	return nil
}

func isEmptyObject(r json.RawMessage) bool {
	r = bytes.TrimSpace(r)
	return len(r) == 0 || bytes.Equal(r, []byte("{}")) || bytes.Equal(r, []byte("null"))
}

// planApproval is the first pending plan approval, as aide's
// findPlanApproval.
func planApproval(approvals []Approval) *PlanApproval {
	for _, a := range approvals {
		if a.RequestType == RequestPlan {
			return &PlanApproval{Detail: a.Detail, ID: a.ID}
		}
	}
	return nil
}
