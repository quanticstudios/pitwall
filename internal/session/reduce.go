package session

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"

	"github.com/quanticstudios/pitwall/internal/model"
)

// Reduce turns one driver event into the deltas that bring s up to date,
// without envelopes; the chat loop stamps Version, SessionID, Seq,
// Revision and UpdatedAt. It is pure: same s and e, same deltas.
//
// The rules are aide's ManagedSessionManager and its runtime projector.
// An event that changes nothing still returns an empty session.patch, as
// aide publishes a revision for it; limit, session.resume-id and unknown
// types return nil. When a change has no delta form, the one delta is a
// snapshot; the loop sets that snapshot's Revision and UpdatedAt too.
func Reduce(s Snapshot, e Event) []Delta {
	n, ok := next(s, e)
	if !ok {
		return nil
	}
	ds, ok := diff(s, n)
	if !ok {
		return []Delta{{Type: DeltaSnapshot, Snapshot: &n}}
	}
	if len(ds) == 0 {
		return []Delta{{Type: DeltaSessionPatch}}
	}
	return ds
}

// next is the snapshot after e, or false for an event that never touches
// one.
func next(s Snapshot, e Event) (Snapshot, bool) {
	n := s
	// The active turn is the last one started and not ended.
	active := len(s.Turns) - 1
	for active >= 0 && s.Turns[active].State != TurnRunning {
		active--
	}

	ended := ""
	switch {
	case e.Type == EventError,
		e.Type == EventState && e.State == StateError,
		e.Type == EventExited && e.ExitCode != nil && *e.ExitCode != 0:
		ended = TurnFailed
	case e.Type == EventExited, e.Type == EventState && e.State == StateStopped:
		ended = TurnInterrupted
	}
	if ended != "" {
		if active >= 0 {
			n.Turns = slices.Clone(s.Turns)
			n.Turns[active].CompletedAt = ptr(e.CreatedAt)
			n.Turns[active].State = ended
			active = -1
		}
		n.PendingApprovals = nil
		n.PendingUserInputs = nil
	}
	pending := func() bool { return len(n.PendingApprovals)+len(n.PendingUserInputs) > 0 }
	settle := func() {
		n.Detail = pendingDetail(n)
		if !pending() {
			n.SessionState = StateReady
			if active >= 0 {
				n.SessionState = StateRunning
			}
		}
	}

	switch e.Type {
	case EventStarted:
		n.SessionState = StateConnecting
	case EventState:
		n.SessionState = e.State
		if e.Detail != "" {
			n.Detail = ptr(e.Detail)
		} else if e.State != StateWaiting {
			n.Detail = nil
		}
	case EventExited:
		n.SessionState = StateStopped
		if e.ExitCode != nil && *e.ExitCode != 0 {
			n.SessionState = StateError
		}
	case EventTurnStarted:
		n.LastError, n.LastErrorAt = nil, nil
		n.SessionState = StateRunning
		turn := TurnSummary{ID: e.TurnID, Provider: s.Provider, StartedAt: e.CreatedAt, State: TurnRunning}
		if i := slices.IndexFunc(s.Turns, func(t TurnSummary) bool { return t.ID == e.TurnID }); i >= 0 {
			n.Turns = slices.Clone(n.Turns)
			n.Turns[i] = turn
		} else {
			n.Turns = append(slices.Clip(n.Turns), turn)
		}
		n.Content = bindPrompts(s.Content, e)
	case EventTurnCompleted:
		if i := slices.IndexFunc(s.Turns, func(t TurnSummary) bool { return t.ID == e.TurnID }); i >= 0 {
			n.Turns = slices.Clone(s.Turns)
			n.Turns[i].CompletedAt = ptr(e.CreatedAt)
			n.Turns[i].State = e.State
		}
		if e.State == TurnCompleted {
			n.LastCompletedAt = ptr(e.CreatedAt)
			if !pending() {
				n.SessionState = StateReady
			}
		} else {
			msg := e.ErrorMessage
			if msg == "" {
				msg = "Turn " + e.State
			}
			n.LastError, n.LastErrorAt = ptr(msg), ptr(e.CreatedAt)
			if !pending() {
				n.SessionState = StateError
			}
		}
	case EventRequestOpened:
		a := Approval{ID: e.RequestID, Request: e.Request, RequestType: e.RequestType}
		if e.Detail != "" {
			a.Detail = ptr(e.Detail)
		}
		if a.RequestType == "" {
			a.RequestType = RequestUnknown
		}
		n.PendingApprovals = upsert(s.PendingApprovals, a, func(a Approval) string { return a.ID })
		n.Detail = a.Detail
		n.SessionState = StateWaiting
	case EventRequestResolved:
		n.PendingApprovals = slices.DeleteFunc(slices.Clone(s.PendingApprovals), func(a Approval) bool { return a.ID == e.RequestID })
		settle()
	case EventInputRequested:
		in := InputRequest{ID: e.RequestID, Questions: nonNil(e.Questions)}
		if e.Detail != "" {
			in.Detail = ptr(e.Detail)
		}
		n.PendingUserInputs = upsert(s.PendingUserInputs, in, func(in InputRequest) string { return in.ID })
		n.Detail = in.Detail
		n.SessionState = StateWaiting
	case EventInputResolved:
		n.PendingUserInputs = slices.DeleteFunc(slices.Clone(s.PendingUserInputs), func(in InputRequest) bool { return in.ID == e.RequestID })
		settle()
	case EventError:
		n.LastError, n.LastErrorAt = ptr(e.Message), ptr(e.CreatedAt)
		n.Detail = ptr(e.Message)
		n.SessionState = StateError
	case EventUsage:
		n.Usage = &Usage{
			InputTokens:       e.InputTokens,
			OutputTokens:      e.OutputTokens,
			CachedInputTokens: e.CachedInputTokens,
			ContextTokens:     e.ContextTokens,
			MaxTokens:         e.MaxTokens,
		}
		if e.Report != nil {
			r := *e.Report
			r.RuntimeGeneration = s.RuntimeGeneration
			n.UsageReports = upsert(s.UsageReports, r, func(r UsageReport) [4]any {
				return [4]any{r.Provider, r.RuntimeGeneration, r.ScopeID, r.ID}
			})
			sum := summarize(n.UsageReports)
			n.UsageSummary = &sum
		}
	case EventContent:
		var turnID *string
		if active >= 0 {
			turnID = ptr(s.Turns[active].ID)
		}
		n.Content = addContent(s, e, turnID)
	default:
		return s, false
	}
	n.Running = n.SessionState != StateStopped && n.SessionState != StateError
	n.PlanApproval = planApproval(n.PendingApprovals)
	return n, true
}

// bindPrompts gives the user prompts a starting turn takes its id: the
// ones sent before any turn took them, and the last QueuedSendCount ones,
// sent during the previous turn and held by the agent for this one.
func bindPrompts(content []Item, e Event) []Item {
	out := slices.Clone(content)
	held := e.QueuedSendCount
	for i := len(out) - 1; i >= 0; i-- {
		it := &out[i]
		if it.Kind != KindUserPrompt {
			continue
		}
		switch {
		case it.TurnID == nil:
			it.TurnID = ptr(e.TurnID)
			it.UpdatedAt = e.CreatedAt
		case held > 0:
			it.TurnID = ptr(e.TurnID)
			held--
		}
	}
	return out
}

// addContent is s's content after a content.delta. Items are keyed by
// kind and id, as aide keys them. A new assistant text item that follows
// another in the same turn, from the same provider and model, joins it,
// as aide's coalesceContiguousAssistantContent does for every provider
// but Codex, whose separate messages stay apart.
func addContent(s Snapshot, e Event, turnID *string) []Item {
	content := slices.Clone(s.Content)
	if i := slices.IndexFunc(content, func(it Item) bool { return it.ID == e.ItemID && it.Kind == e.Kind }); i >= 0 {
		it := &content[i]
		if e.Phase != "" {
			it.Phase = e.Phase
		}
		if e.Tool != nil {
			it.Tool = ptr(*e.Tool)
		}
		it.Text += e.Text
		it.UpdatedAt = e.CreatedAt
		return content
	}
	it := Item{
		CreatedAt: e.CreatedAt,
		ID:        e.ItemID,
		Kind:      e.Kind,
		Phase:     e.Phase,
		Model:     s.Model,
		Provider:  ptr(s.Provider),
		Text:      e.Text,
		TurnID:    turnID,
		UpdatedAt: e.CreatedAt,
	}
	if e.Model != "" {
		it.Model = ptr(e.Model)
	}
	if e.Tool != nil {
		it.Tool = ptr(*e.Tool)
	}
	// shortcut: the snapshot does not keep a joined item's own id, so a later
	// delta for it joins again only while it is still the last item.
	if last := len(content) - 1; last >= 0 && it.Kind == KindAssistantText && s.Provider != model.ProviderCodex && it.TurnID != nil {
		p := &content[last]
		if p.Kind == KindAssistantText && eq(p.TurnID, it.TurnID) && eq(p.Provider, it.Provider) && eq(p.Model, it.Model) {
			p.Text += it.Text
			p.UpdatedAt = it.UpdatedAt
			return content
		}
	}
	return append(content, it)
}

// pendingDetail is the first pending approval's detail, else the first
// pending input's, as aide's projector reads it.
func pendingDetail(s Snapshot) *string {
	if len(s.PendingApprovals) > 0 && s.PendingApprovals[0].Detail != nil && *s.PendingApprovals[0].Detail != "" {
		return s.PendingApprovals[0].Detail
	}
	if len(s.PendingUserInputs) > 0 {
		return s.PendingUserInputs[0].Detail
	}
	return nil
}

// diff is the deltas that take s to n, aide's diffSnapshots with the tool
// and reasoning types its fixtures use. False means n has no delta form.
func diff(s, n Snapshot) ([]Delta, bool) {
	if s.SessionID != n.SessionID || !prefix(s.Content, n.Content, func(it Item) string { return it.ID }) ||
		!prefix(s.Turns, n.Turns, func(t TurnSummary) string { return t.ID }) {
		return nil, false
	}
	// Everything diff does not handle must be unchanged.
	rest := func(x Snapshot) Snapshot {
		x.Content, x.Turns, x.PendingApprovals, x.PendingUserInputs, x.PlanApproval = nil, nil, nil, nil, nil
		x.SessionState, x.Running, x.Detail, x.LastCompletedAt = "", false, nil, nil
		x.Usage, x.UsageReports, x.UsageSummary, x.LastError, x.LastErrorAt = nil, nil, nil, nil, nil
		return x
	}
	if !reflect.DeepEqual(rest(s), rest(n)) {
		return nil, false
	}

	var ds []Delta
	for i, it := range n.Content {
		if i >= len(s.Content) {
			t := DeltaItemAdd
			if it.Tool != nil {
				t = DeltaToolStart
			}
			ds = append(ds, Delta{Type: t, Item: ptr(it)})
			continue
		}
		d, ok := diffItem(s.Content[i], it)
		if !ok {
			return nil, false
		}
		ds = append(ds, d...)
	}
	d, ok := diffByID(s.PendingApprovals, n.PendingApprovals, func(a Approval) string { return a.ID },
		func(a Approval) Delta { return Delta{Type: DeltaApprovalAdd, Approval: ptr(a)} },
		func(id string) Delta { return Delta{Type: DeltaApprovalResolve, RequestID: id} })
	if !ok {
		return nil, false
	}
	ds = append(ds, d...)
	d, ok = diffByID(s.PendingUserInputs, n.PendingUserInputs, func(in InputRequest) string { return in.ID },
		func(in InputRequest) Delta { return Delta{Type: DeltaInputAdd, Input: ptr(in)} },
		func(id string) Delta { return Delta{Type: DeltaInputResolve, RequestID: id} })
	if !ok {
		return nil, false
	}
	ds = append(ds, d...)
	for i, t := range n.Turns {
		if i >= len(s.Turns) || !reflect.DeepEqual(s.Turns[i], t) {
			ds = append(ds, Delta{Type: DeltaTurn, Turn: ptr(t)})
		}
	}
	if s.SessionState != n.SessionState || s.Running != n.Running || !eq(s.Detail, n.Detail) || !eq(s.LastCompletedAt, n.LastCompletedAt) {
		ds = append(ds, Delta{
			Type: DeltaStatus, Status: Status(n.SessionState), SessionState: n.SessionState,
			Running: n.Running, Detail: n.Detail, LastCompletedAt: n.LastCompletedAt,
		})
	}
	if !reflect.DeepEqual(s.Usage, n.Usage) {
		if n.Usage == nil {
			return nil, false
		}
		ds = append(ds, Delta{Type: DeltaUsage, Usage: n.Usage})
	}
	if !reflect.DeepEqual(s.UsageReports, n.UsageReports) || !reflect.DeepEqual(s.UsageSummary, n.UsageSummary) {
		if n.UsageReports == nil || n.UsageSummary == nil {
			return nil, false
		}
		ds = append(ds, Delta{Type: DeltaUsageReports, UsageReports: n.UsageReports, UsageSummary: n.UsageSummary})
	}
	if !eq(s.LastError, n.LastError) || !eq(s.LastErrorAt, n.LastErrorAt) {
		ds = append(ds, Delta{Type: DeltaError, LastError: n.LastError, LastErrorAt: n.LastErrorAt})
	}
	return ds, true
}

// diffItem is the deltas that take item p to q: appends for text and tool
// output, plan.update for tasks, tool.end for a finished call, and one
// item.patch for anything else.
func diffItem(p, q Item) ([]Delta, bool) {
	if reflect.DeepEqual(p, q) {
		return nil, true
	}
	var ds []Delta
	patch := false
	pc, qc := p, q
	pc.Text, pc.Tool, pc.UpdatedAt = "", nil, ""
	qc.Text, qc.Tool, qc.UpdatedAt = "", nil, ""
	patch = !reflect.DeepEqual(pc, qc)

	if q.Text != p.Text {
		if !strings.HasPrefix(q.Text, p.Text) {
			patch = true
		}
		t := DeltaItemTextAppend
		if q.Kind == KindReasoningText || q.Kind == KindReasoningSummaryText {
			t = DeltaReasoningAppend
		}
		ds = append(ds, Delta{Type: t, ItemID: q.ID, Text: strings.TrimPrefix(q.Text, p.Text), ItemUpdatedAt: q.UpdatedAt})
	}
	if !reflect.DeepEqual(p.Tool, q.Tool) {
		if p.Tool == nil || q.Tool == nil {
			patch = true
		} else {
			pt, qt := *p.Tool, *q.Tool
			if qt.Output != pt.Output {
				if !strings.HasPrefix(qt.Output, pt.Output) {
					patch = true
				}
				ds = append(ds, Delta{Type: DeltaToolOutputAppend, ItemID: q.ID, Text: strings.TrimPrefix(qt.Output, pt.Output), ItemUpdatedAt: q.UpdatedAt})
			}
			if !reflect.DeepEqual(pt.Tasks, qt.Tasks) {
				ds = append(ds, Delta{Type: DeltaPlanUpdate, ItemID: q.ID, Tasks: qt.Tasks, ItemUpdatedAt: q.UpdatedAt})
			}
			if pt.Status != qt.Status || !eq(pt.ExitCode, qt.ExitCode) {
				if (qt.Status != "completed" && qt.Status != "failed") || qt.ExitCode == nil && pt.ExitCode != nil {
					patch = true
				}
				// shortcut: an item keeps no start time, so StartedAt is when the
				// item last changed before the end. Add one to ToolCall if a
				// consumer needs the exact start.
				ds = append(ds, Delta{Type: DeltaToolEnd, ItemID: q.ID, Status: qt.Status, ExitCode: qt.ExitCode, StartedAt: p.UpdatedAt, EndedAt: q.UpdatedAt})
			}
			pt.Output, pt.Tasks, pt.Status, pt.ExitCode = "", nil, "", nil
			qt.Output, qt.Tasks, qt.Status, qt.ExitCode = "", nil, "", nil
			patch = patch || !reflect.DeepEqual(pt, qt)
		}
	}
	if !patch && len(ds) > 0 {
		return ds, true
	}

	pm, qm := jsonObject(p), jsonObject(q)
	set := map[string]json.RawMessage{}
	for k, v := range qm {
		if string(pm[k]) != string(v) {
			set[k] = v
		}
	}
	for k := range pm {
		if _, ok := qm[k]; !ok {
			return nil, false // a patch cannot remove a key
		}
	}
	b, _ := json.Marshal(set)
	return []Delta{{Type: DeltaItemPatch, ItemID: q.ID, Patch: b}}, true
}

// diffByID is aide's diffById: resolves for the entries n dropped, then
// adds for the ones it appended. False when n changed or reordered a kept
// entry.
func diffByID[T any](s, n []T, id func(T) string, add func(T) Delta, resolve func(string) Delta) ([]Delta, bool) {
	ids := map[string]bool{}
	for _, x := range n {
		ids[id(x)] = true
	}
	var ds []Delta
	var kept []T
	for _, x := range s {
		if ids[id(x)] {
			kept = append(kept, x)
		} else {
			ds = append(ds, resolve(id(x)))
		}
	}
	if len(kept) > len(n) || !reflect.DeepEqual(nonNil(kept), nonNil(n[:len(kept)])) {
		return nil, false
	}
	for _, x := range n[len(kept):] {
		ds = append(ds, add(x))
	}
	return ds, true
}

// Status is aide's toSessionDeltaStatus: the coarse label of a status
// delta.
func Status(sessionState string) string {
	switch sessionState {
	case StateConnecting, StateRunning:
		return "working"
	case StateWaiting:
		return "waiting"
	case StateReady:
		return "done"
	case StateStopped:
		return "interrupted"
	}
	return "error"
}

// summarize is aide's summarizeManagedUsage: reports are cumulative within
// a scope, so each scope counts its largest numbers once.
func summarize(reports []UsageReport) UsageSummary {
	type scope struct {
		provider string
		cost     *float64
		models   []ModelUsage
	}
	var scopes []*scope
	byScope := map[[3]any]*scope{}
	for _, r := range reports {
		key := [3]any{r.Provider, r.RuntimeGeneration, r.ScopeID}
		sc := byScope[key]
		if sc == nil {
			sc = &scope{provider: r.Provider}
			byScope[key] = sc
			scopes = append(scopes, sc)
		}
		sc.cost = maxKnown(sc.cost, r.EstimatedCostUSD)
		for _, u := range r.Models {
			i := slices.IndexFunc(sc.models, func(m ModelUsage) bool { return eq(m.Model, u.Model) })
			if i < 0 {
				sc.models = append(sc.models, u)
				continue
			}
			p := sc.models[i]
			sc.models[i] = ModelUsage{
				Model:                    u.Model,
				InputTokens:              max(p.InputTokens, u.InputTokens),
				OutputTokens:             max(p.OutputTokens, u.OutputTokens),
				CachedInputTokens:        maxKnown(p.CachedInputTokens, u.CachedInputTokens),
				CacheCreationInputTokens: maxKnown(p.CacheCreationInputTokens, u.CacheCreationInputTokens),
				EstimatedCostUSD:         maxKnown(p.EstimatedCostUSD, u.EstimatedCostUSD),
			}
		}
	}

	sum := UsageSummary{CachedInputTokens: ptr[int64](0), CacheCreationInputTokens: ptr[int64](0), IsCostComplete: len(scopes) > 0, Models: []UsageSummaryModel{}}
	for _, sc := range scopes {
		if sc.cost == nil {
			sum.IsCostComplete = false
		} else {
			sum.EstimatedCostUSD += *sc.cost
		}
		for _, u := range sc.models {
			i := slices.IndexFunc(sum.Models, func(m UsageSummaryModel) bool { return m.Provider == sc.provider && eq(m.Model, u.Model) })
			if i < 0 {
				sum.Models = append(sum.Models, UsageSummaryModel{ModelUsage: u, Provider: sc.provider})
				continue
			}
			p := sum.Models[i]
			sum.Models[i] = UsageSummaryModel{Provider: sc.provider, ModelUsage: ModelUsage{
				Model:                    u.Model,
				InputTokens:              p.InputTokens + u.InputTokens,
				OutputTokens:             p.OutputTokens + u.OutputTokens,
				CachedInputTokens:        sumKnown(p.CachedInputTokens, u.CachedInputTokens),
				CacheCreationInputTokens: sumKnown(p.CacheCreationInputTokens, u.CacheCreationInputTokens),
				EstimatedCostUSD:         sumKnown(p.EstimatedCostUSD, u.EstimatedCostUSD),
			}}
		}
	}
	for _, m := range sum.Models {
		sum.InputTokens += m.InputTokens
		sum.OutputTokens += m.OutputTokens
		sum.CachedInputTokens = sumKnown(sum.CachedInputTokens, m.CachedInputTokens)
		sum.CacheCreationInputTokens = sumKnown(sum.CacheCreationInputTokens, m.CacheCreationInputTokens)
	}
	return sum
}

func maxKnown[T int64 | float64](a, b *T) *T {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	}
	return ptr(max(*a, *b))
}

func sumKnown[T int64 | float64](a, b *T) *T {
	if a == nil || b == nil {
		return nil
	}
	return ptr(*a + *b)
}

// upsert replaces the entry with x's key in place, as a JavaScript Map's
// set does, or appends x.
func upsert[T any, K comparable](list []T, x T, key func(T) K) []T {
	if i := slices.IndexFunc(list, func(y T) bool { return key(y) == key(x) }); i >= 0 {
		out := slices.Clone(list)
		out[i] = x
		return out
	}
	return append(slices.Clip(list), x)
}

func prefix[T any](s, n []T, id func(T) string) bool {
	if len(s) > len(n) {
		return false
	}
	for i := range s {
		if id(s[i]) != id(n[i]) {
			return false
		}
	}
	return true
}

func jsonObject(v any) map[string]json.RawMessage {
	b, _ := json.Marshal(v)
	var m map[string]json.RawMessage
	_ = json.Unmarshal(b, &m)
	return m
}

func ptr[T any](v T) *T { return &v }

// eq reports whether two optional values are both absent or equal.
func eq[T comparable](a, b *T) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
