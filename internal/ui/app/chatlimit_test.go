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
	"gioui.org/io/input"
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

// TestCycleTools: the header's Tools button steps all, failed, none,
// zen and back to all, at once and in config.toml, all by removing the
// key; the modes match the transcript's.
func TestCycleTools(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte("[chat]\nwidth = \"full\"\n"), 0o600)
	u := &ui{}
	u.cfg.Path = path
	for _, want := range []string{config.ToolsFailed, config.ToolsNone, config.ToolsZen, config.ToolsAll} {
		u.cycleTools()
		b, _ := os.ReadFile(path)
		s, _ := config.LoadFile(path)
		if u.cfg.Chat.ToolCalls != want || s.Chat.ToolCalls != want || !strings.Contains(string(b), `width = "full"`) {
			t.Fatalf("after a click: %q, file %q:\n%s", u.cfg.Chat.ToolCalls, s.Chat.ToolCalls, b)
		}
	}
	if b, _ := os.ReadFile(path); strings.Contains(string(b), "tool_calls") {
		t.Errorf("all left the key:\n%s", b)
	}
	if nextTools("") != config.ToolsFailed || nextTools("bogus") != config.ToolsFailed {
		t.Error("an unset mode is all")
	}
	for i, m := range []string{panel.ToolsAll, panel.ToolsFailed, panel.ToolsNone, panel.ToolsZen} {
		if config.ToolModes[i] != m {
			t.Errorf("mode %d: config %q, panel %q", i, config.ToolModes[i], m)
		}
	}
	if got := toolsTip(config.ToolsZen); got != "Tool calls: zen, your messages and final replies. Click for all" {
		t.Errorf("tip %q", got)
	}
	if toolsLabel("") != "Tools" || toolsLabel(config.ToolsNone) != "No tools" {
		t.Error("labels")
	}
}

// TestToolsButton: the chat header's Tools button, on a New task chat tab
// and a pane chat, cycles [chat] tool_calls from a real click and writes it.
func TestToolsButton(t *testing.T) {
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
		frame := func(evs ...pointer.Event) {
			for _, e := range evs {
				r.Queue(e)
			}
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
		btn := func() *widget.Clickable {
			if tab {
				return &u.chatHead.tools
			}
			if h := u.paneHeads["h"]; h != nil {
				return &h.tools
			}
			return nil
		}
		clicked := false
		for y := 4; y < 120 && !clicked; y += 4 {
			for x := 300; x < 1600 && !clicked; x += 8 {
				p := f32.Pt(float32(x), float32(y))
				frame(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: p})
				if c := btn(); c == nil || !c.Hovered() {
					continue
				}
				frame(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p})
				frame(pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p})
				frame()
				clicked = true
			}
		}
		file, _ := os.ReadFile(path)
		if !clicked || u.cfg.Chat.ToolCalls != config.ToolsFailed || !strings.Contains(string(file), `tool_calls = "failed"`) {
			t.Errorf("tab %v: clicked %v, mode %q, file:\n%s", tab, clicked, u.cfg.Chat.ToolCalls, file)
		}
		if tr := u.transcripts[map[bool]string{true: "c1", false: "h"}[tab]]; tr == nil || tr.ToolCalls != config.ToolsFailed {
			t.Errorf("tab %v: the transcript did not follow the click", tab)
		}
	}
}
