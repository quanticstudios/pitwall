package app

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/panel"
)

// A Claude pane chat rewinds but does not fork, a chat tab does both, and
// a Codex pane chat offers neither, nor side questions.
func TestChatTools(t *testing.T) {
	u := &ui{}
	st := &model.State{
		Panes: []model.Pane{{ID: "claude", Provider: model.ProviderClaude, Transcript: "/x/a.jsonl"},
			{ID: "codex", Provider: model.ProviderCodex, Transcript: "/x/rollout.jsonl"}},
		Chats: []model.Chat{{ID: "tab", Provider: model.ProviderClaude}},
	}
	for _, tc := range []struct {
		id                   string
		rewind, fork, asides bool
	}{{"claude", true, false, true}, {"tab", true, true, true}, {"codex", false, false, false}} {
		var tr panel.Transcript
		aside := u.chatTools(st, tc.id, &tr)
		if tr.CanRewind != tc.rewind || tr.CanFork != tc.fork || aside != tc.asides {
			t.Errorf("%s: rewind %v fork %v aside %v", tc.id, tr.CanRewind, tr.CanFork, aside)
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
