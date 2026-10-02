package app

import (
	"context"
	"log"
	"maps"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

const notificationInterval = 3 * time.Second

type notificationKey struct {
	pane    string
	state   model.AgentState
	updated time.Time
}

func activityKey(a model.Activity) notificationKey {
	return notificationKey{a.PaneID, a.State, a.UpdatedAt.UTC()}
}

// notificationHistory carries the previous activities and delivery history.
// A zero value starts a new connection and seeds its first snapshot silently.
type notificationHistory struct {
	activities map[string]model.Activity
	seen       map[notificationKey]bool
	last       map[string]time.Time
	pending    map[string]model.Activity
}

// decideNotifications is pure: it copies history before updating it. Returned
// activities are deliveries; pending activities wait until the workspace's
// three-second interval expires. Looking at a workspace consumes its activity.
func decideNotifications(previous notificationHistory, next []model.Activity, focused bool, activeWorkspace string, now time.Time) (notificationHistory, []model.Activity) {
	h := notificationHistory{
		activities: make(map[string]model.Activity, len(next)),
		seen:       maps.Clone(previous.seen),
		last:       maps.Clone(previous.last),
		pending:    maps.Clone(previous.pending),
	}
	if h.seen == nil {
		h.seen = map[notificationKey]bool{}
		h.last = map[string]time.Time{}
		h.pending = map[string]model.Activity{}
	}
	for _, a := range next {
		h.activities[a.PaneID] = a
	}
	// Expire removed panes and requests that the agent no longer needs.
	for ws, a := range h.pending {
		current, ok := h.activities[a.PaneID]
		if !ok || current.State != a.State || current.WorkspaceID != ws || focused && ws == activeWorkspace {
			delete(h.pending, ws)
		}
	}
	ordered := slices.Clone(next)
	slices.SortStableFunc(ordered, func(a, b model.Activity) int { return a.UpdatedAt.Compare(b.UpdatedAt) })
	for _, a := range ordered {
		key := activityKey(a)
		seen := h.seen[key]
		h.seen[key] = true
		old := previous.activities[a.PaneID]
		if previous.activities == nil || seen || old.State == a.State || focused && a.WorkspaceID == activeWorkspace {
			continue
		}
		switch a.State {
		case model.StateAwaitingInput, model.StatePendingApproval, model.StatePlanReady, model.StateError:
		case model.StateCompleted:
			if old.State != model.StateWorking {
				continue
			}
		default:
			continue
		}
		if pending, ok := h.pending[a.WorkspaceID]; !ok || !pending.UpdatedAt.After(a.UpdatedAt) {
			h.pending[a.WorkspaceID] = a
		}
	}
	var out []model.Activity
	for ws, a := range h.pending {
		last, sent := h.last[ws]
		if !sent || !now.Before(last.Add(notificationInterval)) {
			out = append(out, a)
			h.last[ws] = now
			delete(h.pending, ws)
		}
	}
	slices.SortFunc(out, func(a, b model.Activity) int { return strings.Compare(a.WorkspaceID, b.WorkspaceID) })
	return h, out
}

type notification struct {
	activity model.Activity
	title    string
}

func notificationCommand(ctx context.Context, n notification) *exec.Cmd {
	urgency := "normal"
	if n.activity.State == model.StatePendingApproval || n.activity.State == model.StateError {
		urgency = "critical"
	}
	body := model.PillLabel(n.activity)
	if detail := []rune(n.activity.Detail); len(detail) > 0 {
		body += ": " + string(detail[:min(120, len(detail))])
	}
	return exec.CommandContext(ctx, "notify-send", "--app-name=pitwall", "--urgency="+urgency,
		"--hint=string:x-canonical-private-synchronous:pitwall-"+n.activity.WorkspaceID, "--", n.title, body)
}

// desktopSender runs only on the notifier goroutine. A missing executable
// disables delivery for this window and logs once.
func desktopSender() func(context.Context, notification) {
	disabled := false
	return func(ctx context.Context, n notification) {
		if disabled {
			return
		}
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		cmd := notificationCommand(ctx, n)
		if cmd.Err != nil {
			disabled = true
			log.Printf("pitwall: notify-send unavailable: %v", cmd.Err)
			return
		}
		if err := cmd.Run(); err != nil && ctx.Err() != context.Canceled {
			log.Printf("pitwall: notify-send: %v", err)
		}
	}
}

type notifier struct {
	mu      sync.Mutex
	focused bool
	active  string
	wake    chan struct{}
	cancel  context.CancelFunc
	done    chan struct{}
}

func newNotifier(b Backend, invalidate func(), send func(context.Context, notification)) *notifier {
	ctx, cancel := context.WithCancel(context.Background())
	n := &notifier{wake: make(chan struct{}, 1), cancel: cancel, done: make(chan struct{})}
	st := b.State()
	if ws := ordered(&st); len(ws) > 0 {
		n.active = ws[0].ID
	}
	go n.run(ctx, b, st, invalidate, send)
	return n
}

func (n *notifier) close() { n.cancel(); <-n.done }

func (n *notifier) setView(focused *bool, active string) {
	n.mu.Lock()
	changed := false
	if focused != nil {
		changed = n.focused != *focused
		n.focused = *focused
	} else {
		changed = n.active != active
		n.active = active
	}
	n.mu.Unlock()
	if changed {
		select {
		case n.wake <- struct{}{}:
		default:
		}
	}
}

func (n *notifier) run(ctx context.Context, b Backend, initial model.State, invalidate func(), send func(context.Context, notification)) {
	defer close(n.done)
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	var h notificationHistory
	version := initial.Version
	ready := initial.Version != 0 || len(initial.Projects) > 0 || len(initial.Activities) > 0
	if ready {
		h, _ = decideNotifications(h, initial.Activities, false, "", time.Now())
	}
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-b.Changed():
			if !ok {
				return
			}
			invalidate()
			ready = true
		case <-n.wake:
		case <-timer.C:
		}
		if !ready {
			continue
		}
		st := b.State()
		if h.activities != nil && st.Version == version && len(h.pending) == 0 {
			continue
		}
		// A lower version starts a new baseline without notifying its snapshot.
		if st.Version < version {
			h.activities = nil
			h.pending = map[string]model.Activity{}
		}
		version = st.Version
		n.mu.Lock()
		focused, active := n.focused, n.active
		n.mu.Unlock()
		now := time.Now()
		var deliveries []model.Activity
		h, deliveries = decideNotifications(h, st.Activities, focused, active, now)
		for _, a := range deliveries {
			n.mu.Lock()
			viewing := n.focused && n.active == a.WorkspaceID
			n.mu.Unlock()
			ws := findWorkspace(&st, a.WorkspaceID)
			if !viewing && ws != nil && !ws.Archived {
				h.last[a.WorkspaceID] = time.Now()
				send(ctx, notification{a, projectName(&st, ws.ProjectID) + " / " + ws.Name})
			}
		}
		timer.Stop()
		var due time.Time
		for ws := range h.pending {
			t := h.last[ws].Add(notificationInterval)
			if due.IsZero() || t.Before(due) {
				due = t
			}
		}
		if !due.IsZero() {
			timer.Reset(time.Until(due))
		}
	}
}
