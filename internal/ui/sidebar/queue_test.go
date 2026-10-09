package sidebar

import (
	"image"
	"reflect"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// A group's queued tasks show under its tabs, the rest at the end, and a
// hovered task's buttons start it now or take it off the queue.
func TestQueueSection(t *testing.T) {
	st := model.State{
		Sessions:   []model.Session{{ID: "s", Name: "main"}},
		Projects:   []model.Project{{ID: "g", SessionID: "s", Name: "repo", Root: "/r", Kind: model.ProjectGit}},
		Workspaces: []model.Workspace{{ID: "w", SessionID: "s", ProjectID: "g", Name: "tab"}, {ID: "x", SessionID: "s", Name: "loose"}},
		Tasks: []model.Task{
			{ID: "t1", SessionID: "s", GroupID: "g", Cmd: []string{"codex", "fix the tests\nmore"}},
			{ID: "t2", SessionID: "s", Cmd: []string{"claude", "write docs"}},
			{ID: "t3", SessionID: "other", Cmd: []string{"pi", "elsewhere"}},
		},
	}
	var s Sidebar
	var r input.Router
	var ops op.Ops
	frame := func() []Event {
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: layout.Exact(image.Pt(288, 800)), Now: time.Now()}
		_, events := s.Layout(gtx, theme.Dark(), &st, "s", "w")
		r.Frame(&ops)
		return events
	}
	frame()
	if len(s.queue) != 2 || s.queue[0].group != "g" || s.queue[0].tasks[0].ID != "t1" || s.queue[1].group != "" || s.queue[1].tasks[0].ID != "t2" {
		t.Fatalf("blocks %+v", s.queue)
	}
	tabBot := 0
	for _, e := range s.elems {
		if e.id == "w" {
			tabBot = e.bot
		}
	}
	if s.queue[0].top < tabBot {
		t.Fatalf("group's queue at %d, over its tab ending at %d", s.queue[0].top, tabBot)
	}
	head, row := queueHeights(layout.Context{Metric: unit.Metric{PxPerDp: 1}})
	b := s.queue[0]
	w := 288 - 1 - 2*int(listPad)
	y := float32(56 + b.top + head + row/2)
	start := f32.Pt(float32(int(listPad)+w-12-20-6-10), y) // right of the row: start, then remove
	for range 2 {
		r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: start})
		frame()
	}
	r.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: start},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: start})
	if got := frame(); !reflect.DeepEqual(got, []Event{DropTask{ID: "t1", Start: true}}) {
		t.Fatalf("start click: %#v", got)
	}
}
