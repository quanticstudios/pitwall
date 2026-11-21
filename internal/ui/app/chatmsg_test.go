package app

import (
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/chatin"
	"github.com/quanticstudios/pitwall/internal/ui/panel"
)

// A Claude pane chat rewinds but does not fork, a chat tab does both, and
// a Codex pane chat offers neither, nor side questions. Codex and
// Antigravity chat tabs rewind from RewindFilesLevel, and every chat that
// rewinds offers the files from it too.
func TestChatTools(t *testing.T) {
	st := &model.State{
		Panes: []model.Pane{{ID: "claude", Provider: model.ProviderClaude, Transcript: "/x/a.jsonl"},
			{ID: "codex", Provider: model.ProviderCodex, Transcript: "/x/rollout.jsonl"}},
		Chats: []model.Chat{{ID: "tab", Provider: model.ProviderClaude}, {ID: "codextab", Provider: model.ProviderCodex},
			{ID: "agytab", Provider: model.ProviderAntigravity}},
	}
	for _, tc := range []struct {
		id                          string
		level                       int
		rewind, fork, asides, files bool
	}{
		{"claude", proto.Level, true, false, true, true},
		{"tab", proto.Level, true, true, true, true},
		{"codex", proto.Level, false, false, false, false},
		{"codextab", proto.Level, true, false, false, true},
		{"agytab", proto.Level, true, false, false, true},
		{"tab", proto.RewindFilesLevel - 1, true, true, true, false},
		{"claude", proto.RewindFilesLevel - 1, true, false, true, false},
		{"codextab", proto.RewindFilesLevel - 1, false, false, false, false},
	} {
		u := &ui{b: &linkedFake{FakeBackend: NewFakeBackend(), level: tc.level}}
		var tr panel.Transcript
		aside := u.chatTools(st, tc.id, &tr)
		if tr.CanRewind != tc.rewind || tr.CanFork != tc.fork || aside != tc.asides || tr.FilesRewind != tc.files {
			t.Errorf("%s at %d: rewind %v fork %v aside %v files %v", tc.id, tc.level, tr.CanRewind, tr.CanFork, aside, tr.FilesRewind)
		}
	}
}

func TestAsideOf(t *testing.T) {
	for _, tc := range []struct {
		m    any
		q    string
		want bool
	}{
		{proto.ChatSend{Text: "/btw why?"}, "why?", true},
		{proto.ChatSend{Text: "why?", Skill: &session.Skill{Name: "btw"}}, "why?", true},
		{proto.ChatSend{Text: "/model x"}, "", false},
		{proto.ChatInterrupt{}, "", false},
	} {
		if q, ok := asideOf(tc.m); ok != tc.want || q != tc.q {
			t.Errorf("asideOf(%+v) = %q, %v", tc.m, q, ok)
		}
	}
}

// A prompt sent mid-turn shares its turn's id: a pane chat rewinds to the
// prompt itself, by its own id, and a chat tab by its turn, the only way
// it rewinds. The check asks by the same name, and the rewind carries the
// button the confirm took.
func TestChatActionsRewind(t *testing.T) {
	for _, tc := range []struct {
		pane bool
		turn string
	}{{true, "p2"}, {false, "t1"}} {
		b := NewFakeBackend()
		u := &ui{b: b}
		var cmp chatin.Composer
		u.chatActions("c", []panel.MessageAction{{Kind: panel.ActRewindCheck, Turn: "t1", Item: "p2"}}, tc.pane, &cmp)
		acts := []panel.MessageAction{{Kind: panel.ActRewind, Turn: "t1", Item: "p2", Text: "and also this", Stash: true}}
		u.chatActions("c", acts, tc.pane, &cmp)
		rewind := proto.ChatRewind{Chat: "c", Turn: tc.turn, Stash: true}
		if got := b.Sent(); len(got) != 2 || got[0] != (proto.ChatRewindCheck{Chat: "c", Turn: tc.turn}) || got[1] != rewind {
			t.Errorf("pane %v: sent %v, want a check and a rewind of %s", tc.pane, got, tc.turn)
		}
		// The prompt comes back only once the rewind went through: a
		// refused one leaves the box as it was.
		cmp.SetText("my draft")
		cmp.Result(chatin.Result{Req: rewind, Message: "Claude Code's rewind list shows this message like a later one"}, time.Now())
		if cmp.Text() != "my draft" {
			t.Errorf("pane %v: composer %q after a refused rewind, want the draft kept", tc.pane, cmp.Text())
		}
		u.chatActions("c", acts, tc.pane, &cmp)
		cmp.Result(chatin.Result{Req: rewind}, time.Now())
		if cmp.Text() != "my draft\nand also this" {
			t.Errorf("pane %v: composer %q after the rewind went through, want the draft above the prompt", tc.pane, cmp.Text())
		}
	}
}

// A daemon at ChatFindLevel finds for the chat's find bar; below it the
// bar finds in what the window holds.
func TestChatFindLevel(t *testing.T) {
	st := &model.State{Chats: []model.Chat{{ID: "tab", Provider: model.ProviderCodex}}}
	for _, level := range []int{proto.ChatFindLevel - 1, proto.ChatFindLevel} {
		b := &linkedFake{FakeBackend: NewFakeBackend(), level: level}
		u := &ui{b: b}
		var tr panel.Transcript
		u.chatTools(st, "tab", &tr)
		if (tr.Find != nil) != (level >= proto.ChatFindLevel) {
			t.Errorf("level %d: Find set %v", level, tr.Find != nil)
			continue
		}
		if tr.Find == nil {
			continue
		}
		tr.Find("needle")
		if got := b.Sent(); len(got) != 1 || got[0] != (proto.ChatFind{Chat: "tab", Query: "needle"}) {
			t.Errorf("sent %v", got)
		}
	}
}
