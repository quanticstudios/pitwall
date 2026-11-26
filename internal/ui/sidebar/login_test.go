package sidebar

import (
	"image"
	"reflect"
	"slices"
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

// A host that waits on a person says what for under its heading, even
// with sessions hidden or none known; its link and Retry now work.
func TestHostListLogin(t *testing.T) {
	const url = "https://login.tailscale.com/a/1f2e3d4c5b6a7"
	login := &Login{Reason: "Tailscale SSH wants an approval", Command: "pitwall host login -- 'box'", URL: url}
	for _, hidden := range []bool{false, true} {
		st := model.State{Sessions: []model.Session{{ID: "ls", Name: "main"}}}
		s := Sidebar{HideSessions: hidden, session: "ls", Hosts: []HostInfo{{Up: true, Sessions: []string{"ls"}}, {Name: "box", Note: "Reconnecting…", Login: login}}}
		var r input.Router
		var ops op.Ops
		frame := func() {
			ops.Reset()
			gtx := layout.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
				Constraints: layout.Exact(image.Pt(288, 800)), Now: time.Now()}
			s.hostList(gtx, theme.Dark(), &st)
			r.Frame(&ops)
		}
		frame()
		var labels []string
		var events []Event
		var collect func([]input.SemanticNode)
		collect = func(nodes []input.SemanticNode) {
			for _, n := range nodes {
				labels = append(labels, n.Desc.Label)
				collect(n.Children)
			}
		}
		collect(r.AppendSemantics(nil))
		for _, want := range []string{"box", "needs login", login.Reason + ". Run in a shell:", login.Command, url, "Retry now"} {
			if !slices.Contains(labels, want) {
				t.Fatalf("hidden %v: %q lacks %q", hidden, labels, want)
			}
		}
		if slices.Contains(labels, "main") == hidden {
			t.Fatalf("hidden %v: sessions in %q", hidden, labels)
		}
		// Press every row; the block's two controls answer.
		for y := float32(0); y < 300; y += 6 {
			at := f32.Pt(30, y)
			s.events = nil
			r.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: at},
				pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: at})
			frame()
			for _, e := range s.events {
				if _, ok := e.(ShowSession); !ok && !slices.Contains(events, e) {
					events = append(events, e)
				}
			}
		}
		if !reflect.DeepEqual(events, []Event{OpenLogin{URL: url}, RetryHosts{}}) {
			t.Fatalf("hidden %v: %#v", hidden, events)
		}
	}
	if got := healthNote(HostInfo{Name: "box", Login: login, Retry: time.Now()}, time.Now()); got != "needs login" {
		t.Fatalf("machine line %q", got)
	}
}
