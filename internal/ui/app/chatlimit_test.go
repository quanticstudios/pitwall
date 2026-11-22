package app

import (
	"image"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/panel"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// limitBackend is a chat backend whose chats return snap.
type limitBackend struct {
	*FakeBackend
	snap session.Snapshot
}

func (b *limitBackend) Chat(id string) (session.Snapshot, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.snap
	s.SessionID = id
	return s, true
}

// TestChatLimitResume: on a pane chat and on a New task chat tab of
// Claude, Codex and Antigravity stopped on a usage limit, the transcript
// gets the window's tool call mode, code scale and the limit notice's
// state; Continue at queues continue for the daemon to send at the reset,
// Snooze until then snoozes the tab, Wake now wakes it, and Cancel drops
// the daemon's resume, or the queued continue once there is one. A pane
// chat reads the limit from its pane's activity, as its TUI writes it.
func TestChatLimitResume(t *testing.T) {
	old := findAide
	findAide = func() []string { return nil }
	t.Cleanup(func() { findAide = old })
	reset := time.Now().Add(2 * time.Hour).Truncate(time.Minute)
	msg := "You've hit your usage limit. Upgrade to Pro, or try again at " + reset.Format("15:04") + "."
	for _, prov := range []model.Provider{model.ProviderClaude, model.ProviderCodex, model.ProviderAntigravity} {
		for _, tab := range []bool{false, true} {
			name, id, ws := string(prov)+" pane chat", "h", "w6"
			b := &limitBackend{FakeBackend: NewFakeBackend()}
			b.snap = session.Snapshot{Provider: prov, SessionState: session.StateError,
				Content: []session.Item{{ID: "p", Kind: session.KindUserPrompt, Text: "Fix it"}}}
			b.mu.Lock()
			i := slices.IndexFunc(b.st.Panes, func(p model.Pane) bool { return p.ID == "h" })
			b.st.Panes[i].Provider, b.st.Panes[i].Transcript = prov, "/tmp/s.jsonl"
			if tab {
				name, id, ws = string(prov)+" chat tab", "c1", "wc"
				b.st.Workspaces = append(b.st.Workspaces, model.Workspace{ID: "wc", SessionID: "s1", Name: "chat", Path: t.TempDir(), Chat: "c1"})
				s1 := &b.st.Sessions[slices.IndexFunc(b.st.Sessions, func(s model.Session) bool { return s.ID == "s1" })]
				s1.Order = append(s1.Order, "wc")
				b.st.Chats = []model.Chat{{ID: "c1", WorkspaceID: "wc", Provider: prov, State: session.StateError}}
				at := reset.UTC().Format(time.RFC3339)
				b.snap.UsageLimit = &session.UsageLimit{Provider: prov, Label: "Session limit", ResetsAt: &at}
			} else {
				b.st.Activities = append(b.st.Activities, model.Activity{PaneID: "h", WorkspaceID: "w6", Provider: prov, State: model.StateError, Detail: msg})
			}
			b.mu.Unlock()
			u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: aide}}
			u.cfg.Chat.Claude, u.cfg.Chat.Codex, u.cfg.Chat.Antigravity = true, true, true
			u.cfg.Chat.ToolCalls, u.cfg.Font.UISize, u.cfg.Font.ChatSize, u.cfg.Font.ChatCodeSize = config.ToolsZen, 13, 13, 19.5
			var r input.Router
			var ops op.Ops
			frame := func() {
				ops.Reset()
				u.layout(gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
					Constraints: gl.Exact(image.Pt(1600, 800)), Now: time.Now()})
				r.Frame(&ops)
			}
			st := b.State()
			u.nav.sync(&st)
			u.nav.selectWorkspace(&st, ws, "")
			if !tab {
				u.nav.setFocus("h")
			}
			frame()
			frame()
			tr := u.transcripts[id]
			if tr == nil {
				t.Fatalf("%s: no transcript drawn", name)
			}
			if tr.ToolCalls != config.ToolsZen || tr.CodeScale != 1.5 {
				t.Errorf("%s: tool calls %q, code scale %v", name, tr.ToolCalls, tr.CodeScale)
			}
			l := tr.Limit
			if l == nil || !l.CanContinue || !l.CanSnooze || !l.At.IsZero() {
				t.Fatalf("%s: limit state %+v", name, l)
			}
			at := reset.Add(panel.ResumeDelay)
			l.Do(panel.LimitContinue, at)
			l.Do(panel.LimitSnooze, at)
			l.Do(panel.LimitWake, time.Time{})
			l.Do(panel.LimitCancel, time.Time{})
			sent := b.Sent()
			want := []any{
				proto.ChatSend{Chat: id, Text: "continue", How: "queue"},
				proto.SetTabInbox{WorkspaceID: ws, SnoozedUntil: at},
				proto.SetTabInbox{WorkspaceID: ws},
				proto.CancelResume{Pane: id},
			}
			for _, w := range want {
				if !slices.ContainsFunc(sent, func(m any) bool { return equalMsg(m, w) }) {
					t.Errorf("%s: did not send %#v in %#v", name, w, sent)
				}
			}
			// The daemon queued it: the notice says when, and Cancel removes it.
			b.mu.Lock()
			b.snap.QueuedInputs = []session.QueuedInput{{ID: "q1", Text: "Continue"}}
			b.mu.Unlock()
			frame()
			if l := u.transcripts[id].Limit; !l.At.Equal(at) {
				t.Errorf("%s: queued continue at %v, want %v", name, l.At, at)
			} else {
				l.Do(panel.LimitCancel, time.Time{})
				if !slices.ContainsFunc(b.Sent(), func(m any) bool { return equalMsg(m, proto.ChatQueue{Chat: id, Action: "remove", ID: "q1"}) }) {
					t.Errorf("%s: Cancel did not remove the queued continue", name)
				}
			}
		}
	}
}

// equalMsg reports whether two messages are the same.
func equalMsg(a, b any) bool { return reflect.DeepEqual(a, b) }

// TestLimitResumeOlder: a daemon before LimitQueueLevel would type
// continue into an idle pane at once, so the notice offers no Continue
// at; the daemon's own resume still shows with Cancel.
func TestLimitResumeOlder(t *testing.T) {
	at := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	s := &session.Snapshot{UsageLimit: &session.UsageLimit{Label: "Usage limit", ResetsAt: &at}}
	resumes := time.Now().Add(61 * time.Minute)
	st := &model.State{Activities: []model.Activity{{PaneID: "c1", Resumes: resumes}}}
	u := &ui{b: &levelBackend{FakeBackend: NewFakeBackend(), level: proto.LimitQueueLevel - 1}}
	r := u.limitResume(st, "c1", &model.Workspace{ID: "wc"}, s)
	if r == nil || r.CanContinue || !r.At.Equal(resumes) || !r.CanSnooze {
		t.Errorf("older daemon: %+v", r)
	}
	if u.limitResume(st, "c1", nil, &session.Snapshot{}) != nil {
		t.Error("a notice with no limit")
	}
}

// levelBackend is a backend linked at level.
type levelBackend struct {
	*FakeBackend
	level int
}

func (b *levelBackend) Link() Link                           { return Link{Level: b.level} }
func (b *levelBackend) Reconnect()                           {}
func (b *levelBackend) Restart()                             {}
func (b *levelBackend) Chat(string) (session.Snapshot, bool) { return session.Snapshot{}, false }

// TestPaneLimit: a pane chat's snapshot takes the limit its pane's
// activity names, with its reset; one the transcript names stays; a
// finished turn or another pane's limit gives none.
func TestPaneLimit(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.Local)
	st := &model.State{Activities: []model.Activity{
		{PaneID: "a", State: model.StateError, Provider: model.ProviderCodex, Detail: "■ You've hit your usage limit. Try again at 15:30."},
		{PaneID: "b", State: model.StateCompleted, Detail: "You've hit your session limit · resets 3pm"},
		{PaneID: "c", State: model.StateError, Detail: "API error 500"},
		{PaneID: "d", State: model.StateError, Detail: "You've hit your session limit"},
	}}
	var s session.Snapshot
	paneLimit(st, "a", &s, now)
	if s.UsageLimit == nil || s.UsageLimit.ResetsAt == nil || s.UsageLimit.Provider != model.ProviderCodex {
		t.Fatalf("a: %+v", s.UsageLimit)
	}
	if at, _ := time.Parse(time.RFC3339, *s.UsageLimit.ResetsAt); !at.Equal(time.Date(2026, 11, 21, 15, 30, 0, 0, time.Local)) {
		t.Errorf("a resets %v", at)
	}
	for _, id := range []string{"b", "c", "x"} {
		var s session.Snapshot
		if paneLimit(st, id, &s, now); s.UsageLimit != nil {
			t.Errorf("%s: %+v", id, s.UsageLimit)
		}
	}
	var d session.Snapshot
	if paneLimit(st, "d", &d, now); d.UsageLimit == nil || d.UsageLimit.ResetsAt != nil {
		t.Errorf("d, no reset: %+v", d.UsageLimit)
	}
	own := &session.UsageLimit{Label: "Weekly limit"}
	s = session.Snapshot{UsageLimit: own}
	if paneLimit(st, "a", &s, now); s.UsageLimit != own {
		t.Error("the transcript's own limit was replaced")
	}
}

// TestQueuedContinue: only a queued "continue" that did not fail stands
// for the resume.
func TestQueuedContinue(t *testing.T) {
	s := &session.Snapshot{QueuedInputs: []session.QueuedInput{{ID: "1", Text: "continue please"}, {ID: "2", Text: " continue ", Error: "may have been sent"}, {ID: "3", Text: "CONTINUE\n"}}}
	if q := queuedContinue(s); q == nil || q.ID != "3" {
		t.Errorf("got %+v", q)
	}
	if queuedContinue(&session.Snapshot{}) != nil {
		t.Error("empty queue")
	}
}

// TestSetTools: setTools applies every mode (all, failed, none, zen) at
// once and writes config.toml, removing the key for all; the modes match
// config.ToolModes and panel.Tools*.
func TestSetTools(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte("[chat]\nwidth = \"full\"\n"), 0o600)
	u := &ui{}
	u.cfg.Path = path
	for _, want := range []string{config.ToolsFailed, config.ToolsNone, config.ToolsZen, config.ToolsAll} {
		u.setTools(want)
		b, _ := os.ReadFile(path)
		s, _ := config.LoadFile(path)
		if u.cfg.Chat.ToolCalls != want || s.Chat.ToolCalls != want || !strings.Contains(string(b), `width = "full"`) {
			t.Fatalf("after setTools(%q): %q, file %q:\n%s", want, u.cfg.Chat.ToolCalls, s.Chat.ToolCalls, b)
		}
	}
	if b, _ := os.ReadFile(path); strings.Contains(string(b), "tool_calls") {
		t.Errorf("all left the key:\n%s", b)
	}
	if toolsIndex("") != 0 || toolsIndex("bogus") != 0 {
		t.Error("an unset or unknown mode is all")
	}
	for i, m := range []string{panel.ToolsAll, panel.ToolsFailed, panel.ToolsNone, panel.ToolsZen} {
		if config.ToolModes[i] != m || toolChoices[i].mode != m {
			t.Errorf("mode %d: config %q, panel %q, choice %q", i, config.ToolModes[i], m, toolChoices[i].mode)
		}
	}
	if got := toolsTip(config.ToolsZen); got != "Tool calls: zen" {
		t.Errorf("tip %q", got)
	}
	if toolsLabel("") != "Tools" || toolsLabel(config.ToolsNone) != "No tools" {
		t.Error("labels")
	}
}

// TestToolsDropdown: the chat header's Tools button, on a New task chat
// tab and a pane chat, opens a dropdown instead of cycling on click;
// clicking a choice, pressing Up/Down/Enter/Esc, clicking outside and
// clicking the button again all behave as expected and persist config.toml.
func TestToolsDropdown(t *testing.T) {
	old := findAide
	findAide = func() []string { return nil }
	t.Cleanup(func() { findAide = old })
	for _, tab := range []bool{true, false} {
		path := filepath.Join(t.TempDir(), "config.toml")
		os.WriteFile(path, nil, 0o600)
		b := &limitBackend{FakeBackend: NewFakeBackend(), snap: session.Snapshot{Provider: model.ProviderCodex, SessionState: session.StateReady,
			Content: []session.Item{{ID: "p", Kind: session.KindUserPrompt, Text: "Fix it"}}}}
		b.mu.Lock()
		i := slices.IndexFunc(b.st.Panes, func(p model.Pane) bool { return p.ID == "h" })
		b.st.Panes[i].Provider, b.st.Panes[i].Transcript = model.ProviderCodex, "/tmp/s.jsonl"
		ws := "w6"
		if tab {
			ws = "wc"
			b.st.Workspaces = append(b.st.Workspaces, model.Workspace{ID: "wc", SessionID: "s1", Name: "chat", Path: t.TempDir(), Chat: "c1"})
			s1 := &b.st.Sessions[slices.IndexFunc(b.st.Sessions, func(s model.Session) bool { return s.ID == "s1" })]
			s1.Order = append(s1.Order, "wc")
			b.st.Chats = []model.Chat{{ID: "c1", WorkspaceID: "wc", Provider: model.ProviderCodex, State: session.StateReady}}
		}
		b.mu.Unlock()
		u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: aide}}
		u.cfg.Chat.Claude, u.cfg.Chat.Codex, u.cfg.Chat.Antigravity = true, true, true
		u.cfg.Path, u.cfg.Chat.ToolCalls = path, config.ToolsAll
		var r input.Router
		var ops op.Ops
		now := time.Now()
		frame := func(evs ...event.Event) {
			r.Queue(evs...)
			ops.Reset()
			now = now.Add(200 * time.Millisecond)
			u.layout(gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
				Constraints: gl.Exact(image.Pt(1600, 800)), Now: now})
			r.Frame(&ops)
		}
		st := b.State()
		u.nav.sync(&st)
		u.nav.selectWorkspace(&st, ws, "")
		if !tab {
			u.nav.setFocus("h")
		}
		frame()
		frame()
		head := func() *chatHead {
			if tab {
				return &u.chatHead
			}
			return u.paneHeads["h"]
		}
		findClick := func(target func() *widget.Clickable, y0, y1 int) (f32.Point, bool) {
			for y := y0; y < y1; y += 6 {
				for x := 300; x < 1600; x += 10 {
					p := f32.Pt(float32(x), float32(y))
					frame(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: p})
					if c := target(); c == nil || !c.Hovered() {
						continue
					}
					frame(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p})
					frame(pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p})
					frame()
					return p, true
				}
			}
			return f32.Point{}, false
		}
		// 1. Clicking Tools opens the menu without cycling the mode.
		triggerPos, clicked := findClick(func() *widget.Clickable {
			if h := head(); h != nil {
				return &h.tools
			}
			return nil
		}, 4, 120)
		h := head()
		if !clicked || h == nil || !h.toolsOpen || u.cfg.Chat.ToolCalls != config.ToolsAll {
			t.Fatalf("tab %v: click trigger: clicked=%v open=%v mode=%q", tab, clicked, h != nil && h.toolsOpen, u.cfg.Chat.ToolCalls)
		}
		// 2. Clicking a menu item (None, index 2) picks it, persists config.toml, updates transcript, and closes.
		if _, ok := findClick(func() *widget.Clickable { return &h.toolsItems[2] }, 40, 360); !ok {
			t.Fatalf("tab %v: could not click menu item None", tab)
		}
		file, _ := os.ReadFile(path)
		if h.toolsOpen || u.cfg.Chat.ToolCalls != config.ToolsNone || !strings.Contains(string(file), `tool_calls = "none"`) {
			t.Fatalf("tab %v: after picking None: open=%v mode=%q file=%q", tab, h.toolsOpen, u.cfg.Chat.ToolCalls, file)
		}
		chatID := map[bool]string{true: "c1", false: "h"}[tab]
		if tr := u.transcripts[chatID]; tr == nil || tr.ToolCalls != config.ToolsNone {
			t.Fatalf("tab %v: transcript did not follow pick", tab)
		}
		// 3. Reopen via trigger, navigate with Down/Up/Enter: None(2) -> Down -> Zen(3) -> Down -> All(0) -> Up -> Zen(3) -> Enter.
		clickTrigger := func() f32.Point {
			p, ok := findClick(func() *widget.Clickable {
				if h := head(); h != nil {
					return &h.tools
				}
				return nil
			}, 4, 120)
			if !ok {
				t.Fatalf("tab %v: could not click Tools trigger", tab)
			}
			return p
		}
		_ = triggerPos
		clickTrigger()
		if !h.toolsOpen || h.toolsSel != 2 {
			t.Fatalf("tab %v: reopen: open=%v sel=%d", tab, h.toolsOpen, h.toolsSel)
		}
		// Move pointer off the menu so hover does not override keyboard selection.
		frame(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(400, 400)})
		press := func(name key.Name) {
			frame(key.Event{Name: name, State: key.Press})
		}
		press(key.NameDownArrow) // -> 3 (Zen)
		if h.toolsSel != 3 {
			t.Fatalf("tab %v: after Down sel=%d, want 3", tab, h.toolsSel)
		}
		press(key.NameDownArrow) // -> 0 (All, wraps)
		if h.toolsSel != 0 {
			t.Fatalf("tab %v: after wrap Down sel=%d, want 0", tab, h.toolsSel)
		}
		press(key.NameUpArrow) // -> 3 (Zen, wraps back)
		if h.toolsSel != 3 {
			t.Fatalf("tab %v: after wrap Up sel=%d, want 3", tab, h.toolsSel)
		}
		press(key.NameReturn)
		frame()
		if h.toolsOpen || u.cfg.Chat.ToolCalls != config.ToolsZen {
			t.Fatalf("tab %v: after Enter: open=%v mode=%q, want zen", tab, h.toolsOpen, u.cfg.Chat.ToolCalls)
		}
		// 4. Reopen, press Down, then Esc: closes without changing mode.
		clickTrigger()
		frame(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(400, 400)})
		press(key.NameDownArrow)
		press(key.NameEscape)
		frame()
		if h.toolsOpen || u.cfg.Chat.ToolCalls != config.ToolsZen {
			t.Fatalf("tab %v: after Esc: open=%v mode=%q, want zen", tab, h.toolsOpen, u.cfg.Chat.ToolCalls)
		}
		// 5. Reopen and click trigger again: toggles closed.
		p := clickTrigger()
		if !h.toolsOpen {
			t.Fatalf("tab %v: failed to reopen", tab)
		}
		frame(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p})
		frame(pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p})
		frame()
		if h.toolsOpen {
			t.Fatalf("tab %v: second click on trigger did not close", tab)
		}
		// 6. Reopen and click outside the menu: closes without changing mode.
		clickTrigger()
		if !h.toolsOpen {
			t.Fatalf("tab %v: failed to reopen for outside click", tab)
		}
		outside := f32.Pt(420, 420)
		frame(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: outside})
		frame(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: outside})
		frame(pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: outside})
		frame()
		if h.toolsOpen || u.cfg.Chat.ToolCalls != config.ToolsZen {
			t.Fatalf("tab %v: after outside click: open=%v mode=%q", tab, h.toolsOpen, u.cfg.Chat.ToolCalls)
		}
		// 7. After the dropdown closes, the composer regains keyboard focus.
		frame(key.EditEvent{Text: "hi"})
		if cmp := u.composers[chatID]; cmp == nil || cmp.Text() != "hi" {
			t.Fatalf("tab %v: composer did not regain focus after closing dropdown: %q", tab, cmp.Text())
		}
		// 8. Esc while the dropdown is open on a running turn closes the menu and does not interrupt.
		b.mu.Lock()
		b.snap.Running = true
		b.mu.Unlock()
		clickTrigger()
		if !h.toolsOpen {
			t.Fatalf("tab %v: failed to open while running", tab)
		}
		sentBefore := len(b.Sent())
		press(key.NameEscape)
		frame()
		if h.toolsOpen {
			t.Fatalf("tab %v: Esc did not close dropdown while running", tab)
		}
		for _, m := range b.Sent()[sentBefore:] {
			if _, ok := m.(proto.ChatInterrupt); ok {
				t.Fatalf("tab %v: Esc on Tools dropdown interrupted the turn", tab)
			}
		}
	}
}
