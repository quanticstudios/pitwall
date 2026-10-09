package app

import (
	"context"
	"log"
	"os/exec"
	"slices"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/remote"
)

type notification struct {
	activity model.Activity
	title    string
	sound    string       // [notifications] sound to play, "" for none
	answer   bool         // Allow and Deny answer it
	act      func(string) // runs a clicked action (see notifySendArgs); nil for none
}

// notificationCommand shows n, with its actions when actions is set
// (notifySendArgs).
func notificationCommand(ctx context.Context, n notification, actions bool) *exec.Cmd {
	urgent := model.Urgent(n.activity)
	body := model.PillLabel(n.activity)
	if detail := []rune(n.activity.Detail); len(detail) > 0 {
		body += ": " + string(detail[:min(120, len(detail))])
		// A terminal notification is its own message; "Input:" adds nothing.
		if n.activity.Provider == model.ProviderTerminal {
			body = string(detail[:min(120, len(detail))])
		}
	}
	if actions {
		return exec.CommandContext(ctx, "notify-send", notifySendArgs(urgent, true, n.answer, n.activity.WorkspaceID, n.title, body)...)
	}
	return desktopCommand(ctx, urgent, n.activity.WorkspaceID, n.title, body)
}

// desktopSender runs only on the notifier goroutine. A missing executable
// disables delivery for this window and logs once. Where notify-send takes
// actions, a notification with act waits for a click in the background.
func desktopSender() func(context.Context, notification) {
	preloadToasts()
	disabled, soundFailed := false, false
	var actions *bool
	return func(ctx context.Context, n notification) {
		if n.sound != "" {
			if err := playSound(n.sound); err != nil && !soundFailed {
				soundFailed = true
				log.Printf("sound %q: %q", n.sound, err)
			}
		}
		if disabled {
			return
		}
		if actions == nil {
			ok := notifyActions()
			actions = &ok
		}
		if *actions && n.act != nil {
			ctx, cancel := context.WithTimeout(ctx, actionWait)
			waitAction(notificationCommand(ctx, n, true), n.act, cancel)
			return
		}
		ctx, cancel := context.WithTimeout(ctx, notifyTimeout)
		defer cancel()
		cmd := notificationCommand(ctx, n, false)
		if cmd.Err != nil {
			disabled = true
			log.Printf("%q unavailable: %q", cmd.Args[0], cmd.Err)
			return
		}
		if err := runDesktop(cmd); err != nil && ctx.Err() != context.Canceled {
			log.Printf("%q: %q", cmd.Args[0], err)
		}
	}
}

type notifier struct {
	mu      sync.Mutex
	focused bool
	active  string
	session string                // the window's session
	rules   config.NotifySettings // [notifications]
	jump    func(model.Activity)  // shows a's pane and raises the window; nil for none
	wake    chan struct{}
	cancel  context.CancelFunc
	done    chan struct{}
}

func newNotifier(b Backend, invalidate func(), send func(context.Context, notification), jump func(model.Activity)) *notifier {
	ctx, cancel := context.WithCancel(context.Background())
	n := &notifier{wake: make(chan struct{}, 1), cancel: cancel, done: make(chan struct{}), jump: jump, rules: config.DefaultNotifySettings()}
	st := b.State()
	go n.run(ctx, b, st, invalidate, send)
	return n
}

func (n *notifier) close() { n.cancel(); <-n.done }

// setView records the window's focus, or else the tab and session it
// shows.
func (n *notifier) setView(focused *bool, active, session string) {
	n.mu.Lock()
	changed := false
	if focused != nil {
		changed = n.focused != *focused
		n.focused = *focused
	} else {
		changed = n.active != active || n.session != session
		n.active, n.session = active, session
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
	var h model.Notifications
	version := initial.Version
	ready := initial.Version != 0 || len(initial.Projects) > 0 || len(initial.Activities) > 0
	if ready {
		h, _ = model.DecideNotifications(h, initial.Activities, false, "", time.Now())
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
		if h.Idle() && st.Version == version {
			continue
		}
		// A lower version starts a new baseline without notifying its snapshot.
		if st.Version < version {
			h.Rebase()
		}
		version = st.Version
		n.mu.Lock()
		focused, active, session := n.focused, n.active, n.session
		n.mu.Unlock()
		now := time.Now()
		var deliveries []model.Activity
		h, deliveries = model.DecideNotifications(h, st.Activities, focused, active, now)
		for _, a := range deliveries {
			n.mu.Lock()
			viewing := n.focused && n.active == a.WorkspaceID
			rules := n.rules
			n.mu.Unlock()
			ws := findWorkspace(&st, a.WorkspaceID)
			show, sound := notifyRule(rules, a, now)
			if show && !viewing && ws != nil && !ws.Detached && notifies(&st, session, ws.SessionID) {
				h.Sent(a.WorkspaceID, time.Now())
				nt := notification{activity: a, title: notificationTitle(&st, *ws), answer: remote.Answerable(a) && linkLevel(b) >= proto.Since(proto.Answer{})}
				if sound {
					nt.sound = rules.Sound
				}
				nt.act = func(action string) { n.act(b, a, action) }
				send(ctx, nt)
			}
		}
		timer.Stop()
		if due := h.Next(); !due.IsZero() {
			timer.Reset(time.Until(due))
		}
	}
}

// notifies reports whether the window showing session mine notifies about
// session s: its own, and one no window shows when mine sorts first among
// the sessions windows show, so each notification comes once.
func notifies(st *model.State, mine, s string) bool {
	if s == mine {
		return true
	}
	if ss := st.Session(s); ss != nil && ss.Windows > 0 {
		return false
	}
	return !slices.ContainsFunc(st.Sessions, func(x model.Session) bool { return x.Windows > 0 && x.ID < mine })
}

// notificationTitle is "<session> · <group> / <tab>", without the group
// for an ungrouped tab.
func notificationTitle(st *model.State, w model.Workspace) string {
	t := tabTitle(w)
	if w.ProjectID != "" {
		t = groupName(st, w) + " / " + t
	}
	if s := st.Session(w.SessionID); s != nil {
		t = s.Name + " · " + t
	}
	return t
}
