package sidebar

import (
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/pointer"
	"gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// machineHarness is a shared session: local tabs u1 and u2, box tabs b1
// and b2 listed between them, and gpu (down) with g1 first of all. b2's
// finished turn is unseen. Hosts list box before gpu.
func machineHarness(t *testing.T, sections bool) *dragHarness {
	h := &dragHarness{t: t, now: time.Unix(1e9, 0)}
	h.st = model.State{
		Workspaces: []model.Workspace{
			{ID: "g1", Name: "eval sweep"}, {ID: "b1", Name: "hermes"}, {ID: "u1", Name: "sidebar"},
			{ID: "b2", Name: "arke"}, {ID: "u2", Name: "palettes"},
		},
		Panes:      []model.Pane{{ID: "p2", WorkspaceID: "b2", Provider: model.ProviderClaude}},
		Activities: []model.Activity{{PaneID: "p2", WorkspaceID: "b2", Provider: model.ProviderClaude, State: model.StateCompleted, Unseen: true, UpdatedAt: h.now}},
	}
	h.s.HostSections, h.s.HideSessions = sections, true
	h.s.Hosts = []HostInfo{
		{Up: true},
		{Name: "box", SSH: "me@box", Up: true, Latency: 38 * time.Millisecond},
		{Name: "gpu", SSH: "gpu", Note: "ssh: connect to host gpu: Connection refused", Retry: h.now.Add(7 * time.Second)},
	}
	h.s.HostOf = func(id string) string {
		switch id[0] {
		case 'b':
			return "box"
		case 'g':
			return "gpu"
		}
		return ""
	}
	h.frame()
	return h
}

// line is a point on machine host's line, x from the sidebar's left.
func (h *dragHarness) line(host string, x int) f32.Point {
	for _, e := range h.s.elems {
		if e.kind == 'm' && e.id == host {
			return f32.Pt(float32(x), float32(e.head+5+56))
		}
	}
	h.t.Fatalf("no line for %q", host)
	return f32.Point{}
}

// A shared session lists this machine's tabs first, then each host's in
// Hosts order, whatever the session's own order; Alt navigation and the
// tab digits count them that way.
func TestMachineOrder(t *testing.T) {
	h := machineHarness(t, false)
	if got, want := h.s.Rows(&h.st, "", "u1"), []string{"u1", "u2", "b1", "b2", "g1"}; !slices.Equal(got, want) {
		t.Errorf("rows %v, want %v", got, want)
	}
	var drawn []string
	for _, e := range h.s.elems {
		drawn = append(drawn, string(e.kind)+e.id)
	}
	if want := []string{"m", "su1", "su2", "mbox", "sb1", "sb2", "mgpu", "sg1"}; !slices.Equal(drawn, want) {
		t.Errorf("drawn %v, want %v", drawn, want)
	}
	// One machine: the session looks as it did.
	h.s.HostOf = func(string) string { return "" }
	h.frame()
	if slices.ContainsFunc(h.s.elems, func(e elem) bool { return e.kind == 'm' }) {
		t.Error("a one-machine session shows machine lines")
	}
}

// Both styles name each machine once, on its line, with its health; no
// row wears a host chip, and the row of machines under the header is gone.
func TestMachineLines(t *testing.T) {
	for _, sections := range []bool{false, true} {
		h := machineHarness(t, sections)
		got := labels(func(gtx layout.Context) { h.s.Layout(gtx, theme.Dark(), &h.st, "", "u1") }, 288, h.now)
		for _, want := range []string{"This machine", "box", "38 ms", "gpu", "offline, retry in 7s"} {
			if !slices.Contains(got, want) {
				t.Errorf("sections=%v: no %q in %q", sections, want, got)
			}
		}
		// The semantics repeat each label per enclosing node; a host's
		// name shows only where its line's health does.
		count := func(s string) int { return strings.Count("\n"+strings.Join(got, "\n")+"\n", "\n"+s+"\n") }
		for host, health := range map[string]string{"box": "38 ms", "gpu": "offline, retry in 7s"} {
			if count(host) != count(health) {
				t.Errorf("sections=%v: %q shows off its line: %q", sections, host, got)
			}
		}
		if slices.Contains(got, "this machine") {
			t.Errorf("sections=%v: the row of machines still shows: %q", sections, got)
		}
		if tabs := slices.Contains(got, "2 tabs"); tabs != sections {
			t.Errorf("sections=%v: tab count shown %v: %q", sections, tabs, got)
		}
	}
}

// With sections a click on a header folds its tabs away, out of the
// digits' count too, and the header then counts what needs you; another
// session keeps its own sections open.
func TestMachineFold(t *testing.T) {
	h := machineHarness(t, true)
	h.click(h.line("box", 60))
	if got, want := h.s.Rows(&h.st, "", "u1"), []string{"u1", "u2", "g1"}; !slices.Equal(got, want) {
		t.Fatalf("folded rows %v, want %v", got, want)
	}
	got := labels(func(gtx layout.Context) { h.s.Layout(gtx, theme.Dark(), &h.st, "", "u1") }, 288, h.now)
	if !slices.Contains(got, "1 needs you") || slices.Contains(got, "arke") {
		t.Errorf("folded box: %q", got)
	}
	if got := h.s.Rows(&h.st, "other", "u1"); len(got) != 0 && !slices.Contains(got, "b1") {
		t.Errorf("another session took the fold: %v", got)
	}
	// The active tab moving into a folded section opens it.
	if got := h.s.Rows(&h.st, "", "b2"); !slices.Contains(got, "b1") {
		t.Errorf("the active tab's section stays folded: %v", got)
	}
}

// The + on a machine's line opens a tab on that machine, not the picker;
// a down host's does nothing.
func TestMachinePlus(t *testing.T) {
	h := machineHarness(t, false)
	plus := 8 + (288 - 1 - 16) - 20 - 2 + 10
	for _, tc := range []struct {
		host string
		want []Event
	}{
		{"box", []Event{NewTabOnHost{Host: "box"}}},
		{"", []Event{NewTabOnHost{Host: ""}}},
		{"gpu", nil},
	} {
		var got []Event
		for _, ev := range h.click(h.line(tc.host, plus)) {
			if _, ok := ev.(NewTabOnHost); ok {
				got = append(got, ev)
			}
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("+ on %q: %v, want %v", tc.host, got, tc.want)
		}
	}
}

// A drag never leaves its machine: a local tab dropped on a box row lands
// last among this machine's tabs.
func TestMachineDrag(t *testing.T) {
	h := machineHarness(t, false)
	h.pointer(pointer.Press, h.at("u1", 10))
	h.pointer(pointer.Drag, h.at("u1", 30))
	h.now = h.now.Add(time.Second)
	h.pointer(pointer.Drag, h.at("b1", 6))
	h.now = h.now.Add(time.Second)
	evs := h.pointer(pointer.Release, h.at("b1", 6))
	want := MoveSession{WorkspaceID: "u1"}
	if !slices.Contains(evs, Event(want)) {
		t.Errorf("drop on box's row: %v, want %v", evs, want)
	}
}
