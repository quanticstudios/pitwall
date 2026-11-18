package app

import (
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func TestDoneListKeepsNewestPerTab(t *testing.T) {
	var ib doneList
	ib.add(model.Activity{WorkspaceID: "a"}, "a")
	ib.add(model.Activity{WorkspaceID: "a"}, "a again")
	if len(ib.items) != 1 || ib.items[0].title != "a again" {
		t.Fatalf("one entry per tab, newest title: %+v", ib.items)
	}
	for _, ws := range []string{"b", "c", "d", "e"} {
		ib.add(model.Activity{WorkspaceID: ws}, ws)
	}
	if len(ib.items) != doneMax || ib.items[0].ws != "b" {
		t.Fatalf("cap at %d, oldest dropped: %+v", doneMax, ib.items)
	}
	ib.drop("c")
	if len(ib.items) != doneMax-1 {
		t.Fatalf("drop removes one tab: %+v", ib.items)
	}
}

// Use the real Gio router, with a pane behind the chips and the same
// offset and clip as the app's content area. No OS window is needed.
func TestDonePointerRouting(t *testing.T) {
	for _, action := range []string{"open", "close", "pane"} {
		t.Run(action, func(t *testing.T) {
			u := &ui{th: theme.Dark()}
			u.doneTabs.add(model.Activity{WorkspaceID: "a", PaneID: "p"}, "Finished tab")
			var r input.Router
			var ops op.Ops
			var pane int
			paneClicks := 0
			frame := func() {
				ops.Reset()
				gtx := gl.Context{Ops: &ops, Source: r.Source(), Now: time.Now(),
					Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(image.Pt(400, 250))}
				defer clip.Rect(image.Rect(100, 50, 500, 300)).Push(&ops).Pop()
				defer op.Offset(image.Pt(100, 50)).Push(&ops).Pop()
				for {
					e, ok := gtx.Event(pointer.Filter{Target: &pane, Kinds: pointer.Press | pointer.Release | pointer.Drag | pointer.Move | pointer.Scroll | pointer.Cancel | pointer.Leave})
					if !ok {
						break
					}
					if e.(pointer.Event).Kind == pointer.Press {
						paneClicks++
					}
				}
				area := clip.Rect{Max: image.Pt(400, 250)}.Push(&ops)
				event.Op(&ops, &pane)
				area.Pop()
				u.drawDone(gtx)
			}
			var wake bool
			draw := func() {
				frame()
				r.Frame(&ops)
				_, wake = r.WakeupTime()
			}
			draw()
			at := f32.Pt(100+400-16-24-12, 50+250-24-10)
			if action == "close" {
				at.X += 24
			} else if action == "pane" {
				at = f32.Pt(120, 70)
			}
			r.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: at})
			draw()
			r.Queue(pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: at})
			draw()
			if action == "pane" {
				if paneClicks != 1 || len(u.doneTabs.items) != 1 || u.jumpReq != nil {
					t.Fatalf("pane click: clicks=%d notices=%d jump=%v", paneClicks, len(u.doneTabs.items), u.jumpReq)
				}
				return
			}
			if paneClicks != 0 || len(u.doneTabs.items) != 0 {
				t.Fatalf("chip click: pane clicks=%d notices=%d", paneClicks, len(u.doneTabs.items))
			}
			if action == "open" {
				if u.jumpReq == nil || u.jumpReq.PaneID != "p" {
					t.Fatalf("jump = %v", u.jumpReq)
				}
				if !wake {
					t.Fatal("queued navigation needs another frame")
				}
			} else if u.jumpReq != nil {
				t.Fatal("close also queued a jump")
			}
		})
	}
}

func TestDoneLongTitleFitsContent(t *testing.T) {
	u := &ui{th: theme.Dark()}
	u.doneTabs.add(model.Activity{WorkspaceID: "a"}, strings.Repeat("Long tab title ", 30))
	var r input.Router
	var ops op.Ops
	u.drawDone(gl.Context{Ops: &ops, Source: r.Source(), Now: time.Now(),
		Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(image.Pt(300, 200))})
	r.Frame(&ops)
	for _, n := range r.AppendSemantics(nil) {
		if n.Desc.Gestures&input.ClickGesture != 0 && (n.Desc.Bounds.Min.X < 0 || n.Desc.Bounds.Max.X > 300) {
			t.Errorf("click area exceeds the content: %v", n.Desc.Bounds)
		}
	}
}

// A tab on screen in a window in the background keeps its notice until the
// window has focus: the user has not seen it yet.
func TestDoneKeepsShownTabUntilFocus(t *testing.T) {
	var u ui
	u.nav.workspace = "a"
	u.queueDone(model.Activity{WorkspaceID: "a"}, "a")
	u.applyDone(&model.State{})
	if len(u.doneTabs.items) != 1 {
		t.Fatalf("unfocused window dropped the shown tab: %+v", u.doneTabs.items)
	}
	u.winFocused = true
	u.applyDone(&model.State{})
	if len(u.doneTabs.items) != 0 {
		t.Fatalf("focused window kept the shown tab: %+v", u.doneTabs.items)
	}
}

func TestDoneKeepsCoveredTab(t *testing.T) {
	for _, cover := range []string{"settings", "reader", "review", "board", "monitor", "peek"} {
		t.Run(cover, func(t *testing.T) {
			u := &ui{winFocused: true}
			u.nav.workspace = "a"
			st := model.State{Panes: []model.Pane{{ID: "other", WorkspaceID: "b"}}}
			switch cover {
			case "settings":
				u.settings.Show(filepath.Join(t.TempDir(), "config.toml"))
			case "reader":
				path := filepath.Join(t.TempDir(), "notice.md")
				if err := os.WriteFile(path, []byte("# Test"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := u.reader.Open(path, ""); err != nil {
					t.Fatal(err)
				}
				defer u.reader.Close()
			case "review":
				u.review.on = true
			case "board":
				u.boardOn = true
			case "monitor":
				u.mon.on = true
			case "peek":
				u.nav.peek = "other"
			}
			u.queueDone(model.Activity{WorkspaceID: "a"}, "a")
			u.applyDone(&st)
			if len(u.doneTabs.items) != 1 || u.doneView(&st) == "a" {
				t.Fatal("covered tab was treated as seen")
			}
			u.settings.Hide()
			u.reader.Close()
			u.review.on, u.boardOn, u.mon.on = false, false, false
			u.nav.peek = ""
			u.applyDone(&st)
			if len(u.doneTabs.items) != 0 || u.doneView(&st) != "a" {
				t.Fatal("visible tab was not treated as seen")
			}
		})
	}
}

func TestDoneJumpUncoversSelectedTab(t *testing.T) {
	b := NewFakeBackend()
	st := b.State()
	u := &ui{b: b, boardOn: true}
	u.nav.sync(&st)
	u.mon.on, u.review.on = true, true
	u.nav.peek = "d"
	a := model.Activity{WorkspaceID: u.nav.workspace, PaneID: u.nav.focused()}
	u.queueJump(a)
	u.applyJump(&st)
	if u.boardOn || u.mon.on || u.review.on || u.nav.peek != "" || u.nav.workspace != a.WorkspaceID || u.nav.focused() != a.PaneID {
		t.Fatal("notification jump left the selected tab covered")
	}
}

func TestDoneShortContentKeepsOlderNotes(t *testing.T) {
	u := &ui{th: theme.Dark()}
	for _, ws := range []string{"a", "b", "c", "d"} {
		u.doneTabs.add(model.Activity{WorkspaceID: ws}, ws)
	}
	var r input.Router
	var ops op.Ops
	u.drawDone(gl.Context{Ops: &ops, Source: r.Source(), Now: time.Now(),
		Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(image.Pt(300, 100))})
	r.Frame(&ops)
	clicks := 0
	for _, n := range r.AppendSemantics(nil) {
		if n.Desc.Gestures&input.ClickGesture != 0 {
			clicks++
			if n.Desc.Bounds.Min.Y < 0 {
				t.Errorf("click area exceeds the content: %v", n.Desc.Bounds)
			}
		}
	}
	if clicks == 0 || clicks >= 2*doneMax || len(u.doneTabs.items) != doneMax {
		t.Fatalf("click areas = %d, retained notes = %d", clicks, len(u.doneTabs.items))
	}
}

// An answered approval takes its notice with it; a finished turn's stays.
func TestDoneDropsAnsweredWaitingNotice(t *testing.T) {
	var u ui
	wait := model.Activity{PaneID: "p1", WorkspaceID: "a", State: model.StatePendingApproval}
	done := model.Activity{PaneID: "p2", WorkspaceID: "b", State: model.StateCompleted}
	u.queueDone(wait, "a")
	u.queueDone(done, "b")
	st := &model.State{Activities: []model.Activity{wait, done}}
	u.applyDone(st)
	if len(u.doneTabs.items) != 2 {
		t.Fatalf("both notices should show: %+v", u.doneTabs.items)
	}
	st.Activities[0].State = model.StateWorking
	st.Activities[1].State = model.StateWorking
	u.applyDone(st)
	if len(u.doneTabs.items) != 1 || u.doneTabs.items[0].ws != "b" {
		t.Fatalf("want only the finished turn's notice: %+v", u.doneTabs.items)
	}
}
