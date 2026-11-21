package app

import (
	"image"
	"slices"
	"strings"
	"testing"
	"time"

	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// Add to chat goes to the pane's own chat, else its tab's pane chat, else
// the chat last shown while it still shows; the excerpt is named by the
// command block it starts in once the daemon says, a pane's own command
// at once, and a selection with no chat to take it says so.
func TestAddToChat(t *testing.T) {
	old := findAide
	findAide = func() []string { return nil }
	t.Cleanup(func() { findAide = old })
	b := &chatBackend{FakeBackend: NewFakeBackend()}
	b.mu.Lock()
	i := slices.IndexFunc(b.st.Panes, func(p model.Pane) bool { return p.ID == "h" })
	b.st.Panes[i].Provider, b.st.Panes[i].Transcript = model.ProviderClaude, "/tmp/s.jsonl"
	b.st.Panes = append(b.st.Panes, model.Pane{ID: "z", WorkspaceID: "w6"}, model.Pane{ID: "y", WorkspaceID: "w6", Cmd: []string{"npm", "run", "dev"}})
	b.mu.Unlock()
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: aide}}
	u.cfg.Chat.Claude = true
	st := b.State()
	var ops op.Ops
	gtx := gl.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(image.Pt(1600, 800)), Now: time.Now()}
	if got := u.chatFor(&st, "z"); got != "h" {
		t.Fatalf("a shell's selection goes to %q, want its tab's pane chat h", got)
	}
	if got := u.chatFor(&st, "h"); got != "h" {
		t.Fatalf("the agent pane's own goes to %q", got)
	}
	sel := vt.Selection{A: vt.Pos{Line: 41}, B: vt.Pos{Line: 43, Col: 3}}
	u.toChat(gtx, &st, "z", sel, "ok  ./a\nFAIL ./b", image.Rect(0, 0, 100, 100))
	cmp := u.composer(&st, "h")
	if cmp.Text() != "[Terminal #1] " {
		t.Fatalf("composer %q", cmp.Text())
	}
	var ask proto.Commands
	for _, m := range b.Sent() {
		if c, ok := m.(proto.Commands); ok {
			ask = c
		}
	}
	if ask.Pane != "z" || ask.ID < chatAddBase {
		t.Fatalf("asked %+v", ask)
	}
	if u.takeCommands(&st, proto.CommandResult{Pane: "z", ID: 7}) {
		t.Fatal("took the browser's result")
	}
	if !u.takeCommands(&st, proto.CommandResult{Pane: "z", ID: ask.ID, Blocks: []vt.CommandBlock{
		{Prompt: vt.Pos{Line: 10}, Command: "ls"}, {Prompt: vt.Pos{Line: 40}, Command: "go test ./...\n--more"}, {Prompt: vt.Pos{Line: 50}, Command: "git status"}}}) {
		t.Fatal("did not take its result")
	}
	if len(u.adds.waiting) != 0 {
		t.Fatal("still waiting")
	}
	if ps := cmp.Parts(); len(ps) != 1 || ps[0].Label != "go test ./..." || ps[0].Text != "ok  ./a\nFAIL ./b" {
		t.Fatalf("parts %+v", ps)
	}

	// A pane's own command names it at once, and asks nothing.
	before := len(b.Sent())
	u.toChat(gtx, &st, "y", sel, "listening on :3000", image.Rect(0, 0, 100, 100))
	if n := len(b.Sent()); n != before {
		t.Fatalf("asked the daemon %v", b.Sent()[before:])
	}
	if ps := cmp.Parts(); len(ps) != 2 || ps[1].Label != "npm run dev" {
		t.Fatalf("parts %+v", ps)
	}

	u.flipPaneChat("h") // to the TUI: no chat shows in the tab
	u.adds.last = "h"
	if got := u.chatFor(&st, "z"); got != "" {
		t.Fatalf("went to %q, a pane chat out of sight", got)
	}
	u.toChat(gtx, &st, "z", sel, "x", image.Rect(0, 0, 100, 100))
	if !strings.Contains(u.notice, "No agent chat") || !u.noticeErr {
		t.Fatalf("notice %q", u.notice)
	}
}

// commandAt is the block a line falls in: the last prompt at or above it.
func TestCommandAt(t *testing.T) {
	bs := []vt.CommandBlock{{Prompt: vt.Pos{Line: 5}, Command: "a"}, {Prompt: vt.Pos{Line: 9}, Command: " b \nc"}}
	for line, want := range map[uint64]string{0: "", 5: "a", 8: "a", 9: "b", 100: "b"} {
		if got := commandAt(bs, line); got != want {
			t.Errorf("line %d: %q, want %q", line, got, want)
		}
	}
}
