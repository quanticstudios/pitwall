package app

import (
	"context"
	"fmt"
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
	grid vt.Grid
	st   model.State
	sent []proto.Input

	delivered []proto.Deliver
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
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.grid, vt.Modes{BracketedPaste: true}, true
}
func (b *agentBackend) Send(msg any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch m := msg.(type) {
	case proto.Input:
		b.sent = append(b.sent, m)
	case proto.Deliver:
		b.delivered = append(b.delivered, m)
	}
	return nil
}

// oldDaemon is agentBackend's daemon from before proto.Deliver.
type oldDaemon struct{ *agentBackend }

func (oldDaemon) Link() Link                     { return Link{Level: proto.Since(proto.Deliver{}) - 1} }
func (oldDaemon) Reconnect()                     {}
func (oldDaemon) Restart()                       {}
func (b *agentBackend) Changed() <-chan struct{} { return nil }

// TestSendNotes: comments to a busy agent wait; once it finishes its turn a
// daemon too old for proto.Deliver gets them as one bracketed paste, then
// Enter, and they read as sent. A delivery whose pane closed puts its
// comments back to pending.
func TestSendNotes(t *testing.T) {
	b := newAgentBackend(model.StateWorking)
	u := &ui{b: oldDaemon{b}}
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

// The window never types past a draft itself: a ready agent's delivery goes
// to the daemon, whatever frame the window shows, and reads Queued while the
// daemon holds it.
func TestDeliverGoesThroughDaemon(t *testing.T) {
	b := newAgentBackend(model.StateWorking)
	u := &ui{b: b}
	u.review.ws = "w"
	n := &note{Comment: review.Comment{Path: "a.go", Lines: []review.Line{{Kind: '+', New: 7, Text: "x"}}, Text: "Why?"}}
	u.review.notes = map[string][]*note{"/repo": {n}}
	st := b.State()
	now := time.Now()
	u.sendNotes(&st, "/repo", now)
	if len(b.delivered) != 0 || n.state != noteQueued {
		t.Fatalf("busy agent: delivered %+v, state %d", b.delivered, n.state)
	}
	b.st.Activities[0].State = model.StateCompleted
	b.st.Drafting = map[string]bool{"p": true}
	st = b.State()
	u.deliver(&st, now)
	if len(b.sent) != 0 || len(b.delivered) != 1 || b.delivered[0].Pane != "p" || !strings.Contains(b.delivered[0].Text, "Why?") {
		t.Fatalf("typed %q, delivered %+v", b.sent, b.delivered)
	}
	if n.state != noteQueued || n.to != "p" || len(u.review.queue) != 0 {
		t.Fatalf("after the hand over: state %d, to %q, queue %d", n.state, n.to, len(u.review.queue))
	}
	if u.review.status != "Goes to claude once its input box is empty" {
		t.Errorf("status %q", u.review.status)
	}

	// Held by the daemon, it stays Queued however long that takes.
	b.st.Queued = map[string]int{"p": 1}
	st = b.State()
	u.deliver(&st, now.Add(time.Hour))
	if n.state != noteQueued {
		t.Fatalf("held: state %d", n.state)
	}

	// Typed, even with another message left behind it, it reads Sent.
	b.st.Delivered = map[string]uint64{"p": b.delivered[0].Seq}
	st = b.State()
	u.deliver(&st, now.Add(time.Hour))
	if n.state != noteSent {
		t.Fatalf("typed: state %d", n.state)
	}
}

// A comment the daemon took on but never typed goes back to pending, whatever
// else is in State: the pane closed, or the daemon lost its queue.
func TestDeliverRestoresUntypedNotes(t *testing.T) {
	line := review.Line{Kind: '+', New: 7, Text: "x"}
	for _, tc := range []struct {
		name  string
		after time.Duration
		then  func(st *model.State)
		want  noteState
	}{
		{"pane closed", 0, func(st *model.State) { st.Panes = nil; st.Activities = nil }, notePending},
		{"pane exited", 0, func(st *model.State) { st.Panes[0].Exited = true }, notePending},
		{"not yet in the queue", deliverAck - time.Second, func(st *model.State) {}, noteQueued},
		{"daemon restarted", deliverAck, func(st *model.State) {}, notePending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newAgentBackend(model.StateCompleted)
			u := &ui{b: b}
			u.review.ws = "w"
			n := &note{Comment: review.Comment{Path: "a.go", Lines: []review.Line{line}, Text: "Why?"}}
			u.review.notes = map[string][]*note{"/repo": {n}}
			st := b.State()
			now := time.Now()
			u.sendNotes(&st, "/repo", now)
			if len(b.delivered) != 1 || n.state != noteQueued {
				t.Fatalf("delivered %+v, state %d", b.delivered, n.state)
			}
			st = b.State()
			tc.then(&st)
			at := u.deliver(&st, now.Add(tc.after))
			if n.state != tc.want {
				t.Fatalf("state %d, want %d", n.state, tc.want)
			}
			if tc.want == noteQueued && !at.Equal(now.Add(deliverAck)) {
				t.Errorf("next frame at %v, want %v", at, now.Add(deliverAck))
			}
			if tc.want == notePending && len(u.review.pending("/repo")) != 1 {
				t.Errorf("the comment cannot be sent again")
			}
		})
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

// The header shows the branch's pull request, as its sidebar row does,
// and nothing for a branch without one.
func TestReviewPRStatus(t *testing.T) {
	u := &ui{th: theme.Dark()}
	u.review.ws = "w"
	st := &model.State{PRs: map[string]model.PR{"w": {Number: 12, State: model.PROpen, Review: model.ReviewApproved}}}
	var ops op.Ops
	gtx := gl.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Constraints{Max: image.Pt(400, 100)}}
	if d := u.reviewPRStatus(gtx, st); d.Size.X < 60 || d.Size.Y != 28 {
		t.Errorf("PR status %v", d.Size)
	}
	st.PRs = nil
	if d := u.reviewPRStatus(gtx, st); d.Size != (image.Point{}) {
		t.Errorf("no PR drew %v", d.Size)
	}
}

// TestReviewTurnScope: a tab whose agent finished opens on This turn, the
// latest checkpoint's diff; Branch reads the branch's again; one that is
// working opens on Branch. Undo runs on the tab's folder, once while it
// runs, and its error stays shown after the next read.
func TestReviewTurnScope(t *testing.T) {
	oldDiff, oldTurn, oldTurnDiff, oldUndo := readDiff, readTurn, readTurnDiff, undoTurn
	t.Cleanup(func() { readDiff, readTurn, readTurnDiff, undoTurn = oldDiff, oldTurn, oldTurnDiff, oldUndo })
	file := func(name string) []byte {
		return []byte("diff --git a/" + name + " b/" + name + "\n--- a/" + name + "\n+++ b/" + name + "\n@@ -1 +1 @@\n-p\n+q\n")
	}
	readDiff = func(context.Context, string) (gitstat.Patch, error) {
		return gitstat.Patch{Base: "refs/heads/main", Root: "/repo", Diff: file("branch.go")}, nil
	}
	readTurn = func(_ context.Context, dir, ws string) (gitstat.Turn, error) {
		return gitstat.Turn{Rev: "c" + ws, N: 3}, nil
	}
	readTurnDiff = func(_ context.Context, _, rev string) (gitstat.Patch, error) {
		return gitstat.Patch{Root: "/repo", Diff: file(rev + ".go")}, nil
	}
	var mu sync.Mutex
	var undone []string
	undoTurn = func(_ context.Context, dir, ws string) error {
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		undone = append(undone, dir+" "+ws)
		return gitstat.ErrHeadMoved
	}
	paths := func(u *ui) string {
		deadline := time.Now().Add(5 * time.Second)
		for {
			files, _, _, _, loaded := u.review.snapshot()
			if loaded || time.Now().After(deadline) {
				var ps []string
				for _, f := range files {
					ps = append(ps, f.Path)
				}
				return strings.Join(ps, " ")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	b := newAgentBackend(model.StateCompleted)
	u := &ui{b: b, th: theme.Dark()}
	st := b.State()
	u.openReview(&st, "w", "")
	t.Cleanup(u.review.hide)
	if got := paths(u); got != "cw.go" {
		t.Fatalf("a finished turn opened on %q", got)
	}
	if turn, in, _ := u.review.turnState(); !in || turn.N != 3 {
		t.Errorf("turn %+v, in %v", turn, in)
	}
	u.review.setScope(false, false)
	if got := paths(u); got != "branch.go" {
		t.Errorf("Branch shows %q", got)
	}

	u.undoTurnFiles(false)
	u.undoTurnFiles(false) // while the first runs: ignored
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, _, failed := u.review.turnState(); failed != "" || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	u.review.reload()
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	if _, _, failed := u.review.turnState(); !strings.Contains(failed, "HEAD moved") || len(undone) != 1 || undone[0] != "/repo w" {
		t.Errorf("undo: ran %q, failed %q", undone, failed)
	}
	mu.Unlock()

	b = newAgentBackend(model.StateWorking)
	u = &ui{b: b, th: theme.Dark()}
	st = b.State()
	u.openReview(&st, "w", "")
	t.Cleanup(u.review.hide)
	if got := paths(u); got != "branch.go" {
		t.Errorf("a working agent's tab opened on %q", got)
	}
}

// TestReviewStaging: Staging lists the unstaged files under one heading
// and the staged ones under another, a.go in both; s on the unstaged copy
// applies its selected hunk alone to the index, Shift+U on the staged copy
// unstages the whole file, s on a staged file does nothing, and an
// unmerged file comes first and stages whole.
func TestReviewStaging(t *testing.T) {
	oldRead, oldApply, oldFiles, oldTurn := readStaging, applyCached, stageFiles, readTurn
	t.Cleanup(func() { readStaging, applyCached, stageFiles, readTurn = oldRead, oldApply, oldFiles, oldTurn })
	readTurn = func(context.Context, string, string) (gitstat.Turn, error) { return gitstat.Turn{}, nil }
	var lines []string
	for i := 1; i <= 12; i++ {
		lines = append(lines, " l"+string(rune('a'+i)))
	}
	lines[1], lines[10] = "-old2\n+new2", "-old11\n+new11"
	unstaged := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,12 +1,12 @@\n" + strings.Join(lines, "\n") + "\n"
	staged := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-p\n+q\n"
	readStaging = func(context.Context, string) (gitstat.Staging, error) {
		return gitstat.Staging{Root: "/repo", Unstaged: []byte(unstaged), Staged: []byte(staged), Untracked: []string{"new.go"}, Unmerged: []string{"c.go"}}, nil
	}
	var mu sync.Mutex
	var calls []string
	applyCached = func(_ context.Context, dir string, patch []byte, reverse bool) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, fmt.Sprintf("apply %s %v %q", dir, reverse, patch))
		return nil
	}
	stageFiles = func(_ context.Context, dir string, paths []string, unstage bool) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, fmt.Sprintf("files %s %v %v", dir, paths, unstage))
		return nil
	}
	b := newAgentBackend(model.StateWorking)
	u := &ui{b: b, th: theme.Dark()}
	st := b.State()
	u.openReview(&st, "w", "")
	t.Cleanup(u.review.hide)
	u.review.setScope(false, true)
	var files []review.File
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if files, _, _, _, _ = u.review.snapshot(); len(files) == 4 && u.review.staged() {
			break
		}
	}
	if len(files) != 4 || files[0].Status != 'U' || files[1].Key() != "a.go" || files[2].Path != "new.go" || !files[3].Staged {
		t.Fatalf("files %+v", files)
	}
	files = files[1:] // c.go is checked last
	if got := stageItems(files, true); fmt.Sprint(got) != "[-1 0 1 -2 2]" {
		t.Errorf("stageItems %v", got)
	}
	wait := func(n int) []string {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); u.review.restoring() && time.Now().Before(deadline); {
			time.Sleep(5 * time.Millisecond)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(calls) != n {
			t.Fatalf("calls %q", calls)
		}
		return calls
	}

	u.review.selectFile(files, 0)
	u.review.setLine(12, false) // past the first hunk: the second
	u.stageHunk(&files[0], false, false)
	got := wait(1)[0]
	if !strings.Contains(got, `apply /repo false`) || !strings.Contains(got, `+new11`) || strings.Contains(got, `new2`) {
		t.Errorf("s applied %s", got)
	}
	u.stageHunk(&files[2], false, false) // s on a staged file
	u.stageHunk(&files[2], true, true)
	if got := wait(2)[1]; got != "files /repo [a.go] true" {
		t.Errorf("Shift+U ran %s", got)
	}
	u.stageHunk(&review.File{Path: "c.go", Status: 'U'}, false, false) // s on an unmerged file: whole
	if got := wait(3)[2]; got != "files /repo [c.go] false" {
		t.Errorf("s on an unmerged file ran %s", got)
	}
}

// Reply, Request changes and review comments all go through the daemon, which
// holds them for a form; the note says so when the window knows of one.
func TestDeliveriesReachTheDaemonPastAMenu(t *testing.T) {
	b := newAgentBackend(model.StateAwaitingInput)
	b.st.Activities[0].Menu = true
	u := &ui{b: b}
	st := b.State()
	now := time.Now()
	u.review.enqueue("p", "a reply", nil)
	u.deliver(&st, now)
	u.review.enqueue("p", "failed checks", nil).send = true
	u.deliver(&st, now)
	u.review.ws = "w"
	u.review.notes = map[string][]*note{"/repo": {{Comment: review.Comment{Path: "a.go", Lines: []review.Line{{Kind: '+', New: 7, Text: "x"}}, Text: "Why?"}}}}
	u.sendNotes(&st, "/repo", now)
	if len(b.sent) != 0 || len(b.delivered) != 3 {
		t.Fatalf("typed %q, delivered %+v", b.sent, b.delivered)
	}
	if want := "Goes to claude once its menu is answered"; u.review.status != want {
		t.Errorf("status %q, want %q", u.review.status, want)
	}
}
