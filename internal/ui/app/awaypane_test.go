package app

import (
	"slices"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// TestAwayPaneChatTerminal: a chat tab showing its agent's TUI in pane tp
// gets the chat's card over that pane; Escape typed into the pane closes
// it instead of reaching the agent, and the dismiss names the chat; other
// keys pass.
func TestAwayPaneChatTerminal(t *testing.T) {
	b := NewFakeBackend()
	u := &ui{b: b, panes: map[string]*paneUI{}}
	at := time.Date(2026, 11, 27, 11, 0, 0, 0, time.UTC)
	st := &model.State{Chats: []model.Chat{{ID: "ch", Terminal: "tp"}}, Away: map[string]model.Away{"ch": {Chat: "ch", Since: at.Add(-time.Hour), At: at}}}
	a, _, ok := u.awayOf(st, "tp")
	if !ok || a.Chat != "ch" {
		t.Fatalf("no chat card over its TUI pane: %+v %v", a, ok)
	}
	if got := u.awayEsc(st, "tp", []byte("y")); string(got) != "y" {
		t.Fatalf("a key other than Escape was taken: %q", got)
	}
	if got := u.awayEsc(st, "tp", []byte{0x1b}); got != nil {
		t.Fatalf("Escape reached the agent: %q", got)
	}
	if !slices.Contains(b.Sent(), any(proto.ChatAwayDismiss{Chat: "ch"})) {
		t.Fatalf("no dismiss for the chat: %v", b.Sent())
	}
	if _, _, ok := u.awayOf(st, "tp"); ok {
		t.Fatal("the card shows again before the daemon drops it")
	}
	if got := u.awayEsc(st, "tp", []byte{0x1b}); string(got) != "\x1b" {
		t.Fatal("Escape with no card was taken")
	}
	if _, _, ok := u.awayOf(st, "other"); ok {
		t.Fatal("a card over a pane that is no chat's")
	}
}
