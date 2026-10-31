package session

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	DeltaUsage            DeltaType = "usage"
	DeltaUsageReports     DeltaType = "usage.reports"
	DeltaError            DeltaType = "error"
	DeltaSessionPatch     DeltaType = "session.patch"
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

	Usage        *Usage        `json:"usage"`
	UsageReports []UsageReport `json:"usageReports"`
	UsageSummary *UsageSummary `json:"usageSummary"`
	LastError    *string       `json:"lastError"`
	LastErrorAt  *string       `json:"lastErrorAt"`

	Set   json.RawMessage `json:"set"`   // session.patch: a partial Snapshot of patchable keys
	Unset []string        `json:"unset"` // session.patch
}

func (d Delta) MarshalJSON() ([]byte, error) {
	f := []any{
		"version", d.Version, "sessionId", d.SessionID, "seq", d.Seq,
		"revision", d.Revision, "updatedAt", d.UpdatedAt, "type", d.Type,
	}
	switch d.Type {
	case DeltaSnapshot:
		f = append(f, "snapshot", d.Snapshot)
	case DeltaItemAdd, DeltaToolStart:
		f = append(f, "item", d.Item)
	case DeltaItemTextAppend, DeltaReasoningAppend, DeltaToolOutputAppend:
		f = append(f, "itemId", d.ItemID, "text", d.Text, "itemUpdatedAt", d.ItemUpdatedAt)
	case DeltaItemPatch:
		f = append(f, "itemId", d.ItemID, "patch", rawObject(d.Patch))
	case DeltaToolEnd:
		f = append(f, "itemId", d.ItemID, "status", d.Status)
		if d.ExitCode != nil {
			f = append(f, "exitCode", d.ExitCode)
		}
		f = append(f, "startedAt", d.StartedAt, "endedAt", d.EndedAt)
	case DeltaPlanUpdate:
		f = append(f, "itemId", d.ItemID, "tasks", nonNil(d.Tasks), "itemUpdatedAt", d.ItemUpdatedAt)
	case DeltaApprovalAdd:
		f = append(f, "approval", d.Approval)
	case DeltaInputAdd:
		f = append(f, "input", d.Input)
	case DeltaApprovalResolve, DeltaInputResolve:
		f = append(f, "requestId", d.RequestID)
	case DeltaTurn:
		f = append(f, "turn", d.Turn)
	case DeltaStatus:
		f = append(f, "status", d.Status, "sessionState", d.SessionState, "running", d.Running,
			"detail", d.Detail, "lastCompletedAt", d.LastCompletedAt)
	case DeltaUsage:
		f = append(f, "usage", d.Usage)
	case DeltaUsageReports:
		f = append(f, "usageReports", nonNil(d.UsageReports), "usageSummary", d.UsageSummary)
	case DeltaError:
		f = append(f, "lastError", d.LastError, "lastErrorAt", d.LastErrorAt)
	case DeltaSessionPatch:
		f = append(f, "set", rawObject(d.Set))
		if d.Unset != nil {
			f = append(f, "unset", d.Unset)
		}
	}
	var buf bytes.Buffer
	for i := 0; i < len(f); i += 2 {
		if i == 0 {
			buf.WriteByte('{')
		} else {
			buf.WriteByte(',')
		}
		k, _ := json.Marshal(f[i])
		v, err := json.Marshal(f[i+1])
		if err != nil {
			return nil, err
		}
		buf.Write(k)
		buf.WriteByte(':')
		buf.Write(v)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
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
		i := slices.IndexFunc(s.Content, func(it Item) bool { return it.ID == d.ItemID })
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
		if i := slices.IndexFunc(s.Turns, func(t TurnSummary) bool { return t.ID == d.Turn.ID }); i >= 0 {
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
	case DeltaUsage:
		n.Usage = d.Usage
	case DeltaUsageReports:
		n.UsageReports = d.UsageReports
		n.UsageSummary = d.UsageSummary
	case DeltaError:
		n.LastError = d.LastError
		n.LastErrorAt = d.LastErrorAt
	case DeltaSessionPatch:
		if !isEmptyObject(d.Set) || len(d.Unset) > 0 {
			err = mergeJSON(&n, d.Set, d.Unset)
		}
	}
	// Any other type is newer than this schema: like aide, only the
	// revision and updatedAt move.
	if err != nil {
		return Snapshot{}, err
	}
	return n, nil
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
