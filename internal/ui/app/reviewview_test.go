package app

import (
	"context"
	"image"
	"strings"
	"sync"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/review"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// agentBackend is one tab with an agent in pane "p", whose terminal has
// bracketed paste on, and records what the window sends.
type agentBackend struct {
	mu   sync.Mutex
	st   model.State
	sent []proto.Input
}

func newAgentBackend(state model.AgentState) *agentBackend {
	return &agentBackend{st: model.State{
		Sessions:   []model.Session{{ID: "s", Name: "s"}},
		Workspaces: []model.Workspace{{ID: "w", SessionID: "s", Branch: "feature", Path: "/repo", Tabs: []model.Tab{{ID: "t", Layout: layout.Leaf("p")}}}},
		Panes:      []model.Pane{{ID: "p", WorkspaceID: "w", Provider: model.ProviderClaude}},
		Activities: []model.Activity{{PaneID: "p", WorkspaceID: "w", Provider: model.ProviderClaude, State: state}},
		Stats:      map[string]model.BranchStats{"w": {Base: "refs/heads/main", MergeStatus: model.MergeClean}},
	}}
}

func (b *agentBackend) State() model.State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.st
}
func (b *agentBackend) Frame(string) (vt.Grid, vt.Modes, bool) {
	return vt.Grid{}, vt.Modes{BracketedPaste: true}, true
}
func (b *agentBackend) Send(msg any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if in, ok := msg.(proto.Input); ok {
		b.sent = append(b.sent, in)
	}
	return nil
}
func (b *agentBackend) Changed() <-chan struct{} { return nil }

// TestSendNotes: comments to a busy agent wait; once it finishes its turn
// they go in as one bracketed paste, then Enter, and read as sent. A
// delivery whose pane closed puts its comments back to pending.
func TestSendNotes(t *testing.T) {
	b := newAgentBackend(model.StateWorking)
	u := &ui{b: b}
	u.review.ws = "w"
	line := review.Line{Kind: '+', New: 7, Text: "x := 2"}
	u.review.notes = map[string][]*note{"/repo": {
		{Comment: review.Comment{Path: "a.go", Lines: []review.Line{line}, Text: "Why 2?"}},
		{Comment: review.Comment{Path: "b.go", Lines: []review.Line{{Kind: ' ', Old: 1, New: 1, Text: "y"}}, Text: "Rename y."}},
	}}
	now := time.Now()
	st := b.State()
	u.sendNotes(&st, "/repo", now)
	if len(b.sent) != 0 || len(u.review.queue) != 1 || len(u.review.pending("/repo")) != 0 {
		t.Fatalf("busy agent: sent %q, queue %d", b.sent, len(u.review.queue))
	}
	b.st.Activities[0].State = model.StateCompleted
	st = b.State()
	at := u.deliver(&st, now)
	if len(b.sent) != 1 || at != now.Add(replyDelay) {
		t.Fatalf("ready agent: sent %q, next frame at %v", b.sent, at)
	}
	paste := string(b.sent[0].Data)
	if !strings.HasPrefix(paste, "\x1b[200~Review comments") || !strings.HasSuffix(paste, "Rename y.\x1b[201~") ||
		!strings.Contains(paste, "a.go:7\n> +x := 2\nWhy 2?") || b.sent[0].Pane != "p" {
		t.Errorf("paste %q", paste)
	}
	u.deliver(&st, now.Add(replyDelay/2))
	if len(b.sent) != 1 {
		t.Fatalf("Enter came before replyDelay: %q", b.sent)
	}
	u.deliver(&st, now.Add(replyDelay))
	if len(b.sent) != 2 || string(b.sent[1].Data) != "\r" {
		t.Fatalf("no Enter after the paste: %q", b.sent)
	}
	for _, n := range u.review.notes["/repo"] {
		if n.state != noteSent {
			t.Errorf("%s: state %d, want sent", n.Path, n.state)
		}
	}

	u.review.notes["/repo"] = append(u.review.notes["/repo"], &note{Comment: review.Comment{Path: "c.go", Lines: []review.Line{line}, Text: "c"}})
	b.st.Activities[0].State = model.StatePendingApproval
	st = b.State()
	u.sendNotes(&st, "/repo", now)
	b.st.Panes[0].Exited = true
	st = b.State()
	u.deliver(&st, now)
	if len(u.review.queue) != 0 || len(u.review.pending("/repo")) != 1 || len(b.sent) != 2 {
		t.Errorf("closed pane: queue %d, pending %d, sent %d", len(u.review.queue), len(u.review.pending("/repo")), len(b.sent))
	}
}

// TestMarkReviewed: a mark holds for the change it was made on; the file
// changing again unmarks it, and marks of files gone from the diff drop.
func TestMarkReviewed(t *testing.T) {
	u := &ui{}
	a, b := review.File{Path: "a", Hash: "1"}, review.File{Path: "b", Hash: "2"}
	u.markReviewed("/r", []review.File{a, b}, &a, true)
	u.markReviewed("/r", []review.File{a, b}, &b, true)
	if !u.reviewed("/r", &a) || !u.reviewed("/r", &b) || u.reviewed("/other", &a) {
		t.Fatalf("marks %v", u.gui.Reviewed)
	}
	a2 := review.File{Path: "a", Hash: "3"}
	if u.reviewed("/r", &a2) {
		t.Error("a changed file is still reviewed")
	}
	u.markReviewed("/r", []review.File{a2}, &a2, false)
	if _, ok := u.gui.Reviewed["/r"]; ok {
		t.Errorf("marks of files gone from the diff stay: %v", u.gui.Reviewed)
	}
}

// TestReviewNav: n and p go between hunks, n past the last hunk moves to
// the next file's first, and the arrows with Shift select a range.
func TestReviewNav(t *testing.T) {
	var ls []review.Line
	for i := 1; i <= 30; i++ {
		k := byte(' ')
		if i == 5 || i == 20 {
			k = '+'
		}
		ls = append(ls, review.Line{Kind: k, Old: i, New: i, Text: "l"})
	}
	files := []review.File{{Path: "a", Lines: ls}, {Path: "b", Lines: ls}}
	r := &reviewView{cur: -1, anchor: -1, scrollTo: -1}
	r.selectFile(files, 0)
	f := r.file(files)
	rows := r.rowsOf(f)
	if !r.hunk(f, rows, 1) || r.cur != 4 {
		t.Fatalf("first hunk: line %d", r.cur)
	}
	if !r.hunk(f, rows, 1) || r.cur != 19 {
		t.Fatalf("second hunk: line %d", r.cur)
	}
	if r.hunk(f, rows, 1) {
		t.Fatal("a hunk past the last")
	}
	if !r.hunk(f, rows, -1) || r.cur != 4 {
		t.Fatalf("back: line %d", r.cur)
	}
	r.moveLine(rows, 1, true)
	r.moveLine(rows, 1, true)
	if lo, hi := r.span(); lo != 4 || hi != 6 {
		t.Errorf("range %d-%d, want 4-6", lo, hi)
	}
	r.selectFile(files, 1)
	if r.cur != -1 || r.selPath != "b" {
		t.Errorf("another file kept line %d", r.cur)
	}
}

// TestReviewLayout opens the view on a fake diff and draws it with a
// comment field open and a comment saved, then closes it with Escape.
func TestReviewLayout(t *testing.T) {
	old := readDiff
	t.Cleanup(func() { readDiff = old })
	patch := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,2 @@\n x\n-y\n+z\n" +
		"diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -1 +1 @@\n-p\n+q\n"
	readDiff = func(context.Context, string) (gitstat.Patch, error) {
		return gitstat.Patch{Base: "refs/heads/main", Root: "/repo", Diff: []byte(patch)}, nil
	}
	b := newAgentBackend(model.StateCompleted)
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: conventional}}
	var r input.Router
	var ops op.Ops
	frame := func() {
		ops.Reset()
		gtx := gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: gl.Exact(image.Pt(1280, 800)), Now: time.Now()}
		u.layout(gtx)
		r.Frame(&ops)
	}
	frame()
	// The key opens it after the view polled its keys this frame.
	r.Queue(key.Event{Name: "R", Modifiers: key.ModCtrl | key.ModShift, State: key.Press})
	frame()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, _, _, _, loaded := u.review.snapshot(); loaded || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	frame()
	files, _, _, _, _ := u.review.snapshot()
	if !u.review.on || u.review.file(files) == nil {
		t.Fatalf("view on %v, files %v", u.review.on, files)
	}
	press := func(name key.Name) {
		r.Queue(key.Event{Name: name, State: key.Press})
		frame()
	}
	press("J")
	if u.review.selPath != "b.go" {
		t.Fatalf("j selected %q", u.review.selPath)
	}
	press("K")
	press(key.NameDownArrow) // the first line
	press(key.NameDownArrow)
	press("C")
	if !u.review.editing {
		t.Fatal("c opened no comment field")
	}
	u.review.edit.SetText("Keep y.")
	press(key.NameReturn)
	if n := u.review.pending("/repo"); len(n) != 1 || n[0].Ref() != "a.go, deleted line 2" || n[0].Text != "Keep y." {
		t.Fatalf("pending %v", n)
	}
	r.Queue(key.Event{Name: key.NameEscape, State: key.Press})
	frame()
	if u.review.on {
		t.Error("Escape left the view open")
	}
}
