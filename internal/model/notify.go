package model

import (
	"maps"
	"slices"
	"strings"
	"time"
)

// NotificationInterval is the least time between two notifications of a
// workspace.
const NotificationInterval = 3 * time.Second

type notificationKey struct {
	pane    string
	state   AgentState
	updated time.Time
}

func activityKey(a Activity) notificationKey {
	return notificationKey{a.PaneID, a.State, a.UpdatedAt.UTC()}
}

// Notifications carries the previous activities and delivery history of
// one notifier, the desktop's or the phone's. A zero value starts a new
// connection and seeds its first snapshot silently.
type Notifications struct {
	activities map[string]Activity
	seen       map[notificationKey]bool
	last       map[string]time.Time
	pending    map[string]Activity
}

// DecideNotifications is pure: it copies history before updating it.
// Returned activities are deliveries, by workspace id; pending activities
// wait until the workspace's NotificationInterval expires. Only unseen
// activities (Activity.Unseen) notify, so a notification and the attention
// ring agree; looking at a workspace consumes its activity too.
func DecideNotifications(previous Notifications, next []Activity, focused bool, activeWorkspace string, now time.Time) (Notifications, []Activity) {
	h := Notifications{
		activities: make(map[string]Activity, len(next)),
		seen:       maps.Clone(previous.seen),
		last:       maps.Clone(previous.last),
		pending:    maps.Clone(previous.pending),
	}
	if h.seen == nil {
		h.seen = map[notificationKey]bool{}
		h.last = map[string]time.Time{}
		h.pending = map[string]Activity{}
	}
	for _, a := range next {
		h.activities[a.PaneID] = a
	}
	// Expire removed panes and requests that the agent no longer needs.
	for ws, a := range h.pending {
		current, ok := h.activities[a.PaneID]
		if !ok || current.State != a.State || !current.Unseen || current.WorkspaceID != ws || focused && ws == activeWorkspace {
			delete(h.pending, ws)
		} else {
			h.pending[ws] = current
		}
	}
	ordered := slices.Clone(next)
	slices.SortStableFunc(ordered, func(a, b Activity) int { return a.UpdatedAt.Compare(b.UpdatedAt) })
	for _, a := range ordered {
		key := activityKey(a)
		seen := h.seen[key]
		h.seen[key] = true
		old := previous.activities[a.PaneID]
		if previous.activities == nil || seen || !a.Unseen || old.State == a.State || focused && a.WorkspaceID == activeWorkspace {
			continue
		}
		switch a.State {
		case StateAwaitingInput, StatePendingApproval, StatePlanReady, StateError:
		case StateCompleted, StatePROpen:
			if old.State != StateWorking {
				continue
			}
		default:
			continue
		}
		if pending, ok := h.pending[a.WorkspaceID]; !ok || !pending.UpdatedAt.After(a.UpdatedAt) {
			h.pending[a.WorkspaceID] = a
		}
	}
	var out []Activity
	for ws, a := range h.pending {
		if !now.Before(notifyDue(h, ws)) {
			out = append(out, a)
			h.last[ws] = now
			delete(h.pending, ws)
		}
	}
	slices.SortFunc(out, func(a, b Activity) int { return strings.Compare(a.WorkspaceID, b.WorkspaceID) })
	return h, out
}

// notifyDue is when workspace ws's pending activity may go out: after the
// workspace's interval, at once when none has gone out.
func notifyDue(h Notifications, ws string) time.Time {
	if last, sent := h.last[ws]; sent {
		return last.Add(NotificationInterval)
	}
	return time.Time{}
}

// Next is when the earliest pending notification may go out, zero when
// none waits.
func (h Notifications) Next() time.Time {
	var due time.Time
	for ws := range h.pending {
		if t := notifyDue(h, ws); due.IsZero() || t.Before(due) {
			due = t
		}
	}
	return due
}

// Idle reports a history past its first snapshot with nothing pending.
func (h Notifications) Idle() bool { return h.activities != nil && len(h.pending) == 0 }

// Sent records a notification of workspace ws at t, which starts its
// interval. DecideNotifications records its own deliveries; a notifier
// calls Sent when it delivers later than that.
func (h *Notifications) Sent(ws string, t time.Time) { h.last[ws] = t }

// Rebase makes the next snapshot a new baseline that notifies nothing,
// for a state that went back in version.
func (h *Notifications) Rebase() {
	h.activities = nil
	h.pending = map[string]Activity{}
}

// Urgent reports a notification that interrupts: an approval or an error.
func Urgent(a Activity) bool {
	return a.State == StatePendingApproval || a.State == StateError
}
