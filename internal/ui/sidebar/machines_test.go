package sidebar

import (
	"image"
	"image/color"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/gpu/headless"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/unit"

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
	plus := 8 + (288 - 1 - 16) - 20 - 4 + 10
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

// With sections and every machine folded, the keyboard cursor sits on the
// headers: Right or Enter opens a section, Right then steps into it, Left
// goes back to its header and folds it, Delete on a header deletes nothing,
// and Escape gives the keyboard back.
func TestMachineKeys(t *testing.T) {
	h := machineHarness(t, true)
	h.s.hostFold = map[string]bool{foldKey("", ""): true, foldKey("", "box"): true, foldKey("", "gpu"): true}
	h.frame()
	h.s.Focus(&h.st, "", "u1")
	press := func(k key.Name) ([]Event, bool) {
		t.Helper()
		evs, back, used := h.s.Key(&h.st, "", "u1", key.Event{Name: k, State: key.Press})
		if !used {
			t.Fatalf("%s not used", k)
		}
		return evs, back
	}
	for i, step := range []struct {
		k    key.Name
		want string
	}{
		{"", "m:"},                    // on this machine's folded header
		{key.NameDownArrow, "m:box"},  // the next header: its tabs are folded away
		{key.NameRightArrow, "m:box"}, // opens box
		{key.NameRightArrow, "s:b1"},  // into box's first tab
		{key.NameLeftArrow, "m:box"},  // back to its header
		{key.NameLeftArrow, "m:box"},  // folds box
		{key.NameDownArrow, "m:gpu"},
		{key.NameReturn, "m:gpu"}, // opens gpu
		{key.NameDownArrow, "s:g1"},
	} {
		if step.k != "" {
			press(step.k)
		}
		if got := cursorAt(&h.s); got != step.want {
			t.Fatalf("step %d: %s put the cursor on %s, want %s", i, step.k, got, step.want)
		}
	}
	if h.s.sectionFolded(&view{session: ""}, "gpu") || !h.s.sectionFolded(&view{session: ""}, "box") {
		t.Fatalf("folds %v", h.s.hostFold)
	}
	press(key.NameUpArrow) // gpu's header
	if evs, _ := press(key.NameDeleteForward); len(evs) != 0 {
		t.Errorf("Delete on a header: %v", evs)
	}
	if _, back := press(key.NameEscape); !back {
		t.Error("Escape kept the keyboard")
	}
}

// Escape gives the keyboard back even when the sidebar has no rows.
func TestEscapeWithNoRows(t *testing.T) {
	var s Sidebar
	st := model.State{}
	s.Focus(&st, "", "")
	if _, back, used := s.Key(&st, "", "", key.Event{Name: key.NameEscape, State: key.Press}); !used || !back {
		t.Fatalf("Escape with no rows: used %v, back %v", used, back)
	}
}

// A hovered section header fills its whole width with rounded corners,
// and its + sits inside the fill, ending its glyph about 8dp from the
// edge as rows do.
func TestMachineHeaderEdge(t *testing.T) {
	win, err := headless.NewWindow(288, 800)
	if err != nil {
		t.Skip(err)
	}
	defer win.Release()
	h := machineHarness(t, true)
	var top, bot int
	for _, e := range h.s.elems {
		if e.kind == 'm' && e.id == "box" {
			top, bot = e.head+56, e.bot+56
		}
	}
	for range 2 { // hover shows after the second move
		h.r.Queue(pointer.Event{Kind: pointer.Move, Position: f32.Pt(100, float32(top+5)), Source: pointer.Mouse})
		h.frame()
	}
	if err := win.Frame(&h.ops); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 288, 800))
	if err := win.Screenshot(img); err != nil {
		t.Fatal(err)
	}
	th := theme.Dark()
	near := func(got color.RGBA, want color.NRGBA) bool {
		d := func(a, b uint8) int { return max(int(a), int(b)) - min(int(a), int(b)) }
		return d(got.R, want.R) < 4 && d(got.G, want.G) < 4 && d(got.B, want.B) < 4
	}
	right := 8 + 288 - 1 - 16 // the tree's right edge
	mid := (top + bot) / 2
	if c := img.RGBAAt(right-2, top+(bot-top)/4); !near(c, th.SurfaceSecondary) {
		t.Errorf("fill stops short of the right edge: %v, want %v", c, th.SurfaceSecondary)
	}
	if c := img.RGBAAt(right-1, top); !near(c, th.Sidebar) {
		t.Errorf("the fill's top right corner is square: %v, want %v", c, th.Sidebar)
	}
	// The glyph: some pixel in the + differs from the fill, none in the
	// last 7dp.
	glyph := false
	for x := right - 30; x < right; x++ {
		c := img.RGBAAt(x, mid)
		if !near(c, th.SurfaceSecondary) {
			if x >= right-7 {
				t.Errorf("the + reaches x %d, %ddp from the edge", x, right-x)
			}
			glyph = true
		}
	}
	if !glyph {
		t.Error("no + on the hovered header")
	}

	// Over the + its own square shows; the glyph has as much room on
	// each side of it as above and below.
	for range 2 {
		h.r.Queue(pointer.Event{Kind: pointer.Move, Position: f32.Pt(float32(right-14), float32(mid)), Source: pointer.Mouse})
		h.frame()
	}
	if err := win.Frame(&h.ops); err != nil {
		t.Fatal(err)
	}
	if err := win.Screenshot(img); err != nil {
		t.Fatal(err)
	}
	sq := theme.Mix(th.SurfaceSecondary, th.Fg, 0.08)
	var box, plus image.Rectangle
	grow := func(r *image.Rectangle, x, y int) {
		if r.Empty() {
			*r = image.Rect(x, y, x+1, y+1)
		}
		*r = r.Union(image.Rect(x, y, x+1, y+1))
	}
	for y := top; y < bot; y++ {
		for x := right - 30; x < right; x++ {
			if near(img.RGBAAt(x, y), sq) {
				grow(&box, x, y)
			}
		}
	}
	for y := box.Min.Y; y < box.Max.Y; y++ {
		for x := box.Min.X; x < box.Max.X; x++ {
			if img.RGBAAt(x, y).G > sq.G+40 {
				grow(&plus, x, y)
			}
		}
	}
	if box.Empty() || plus.Empty() {
		t.Fatalf("no square %v or + %v over the hovered +", box, plus)
	}
	if l, r, u, d := plus.Min.X-box.Min.X, box.Max.X-plus.Max.X, plus.Min.Y-box.Min.Y, box.Max.Y-plus.Max.Y; l != r || u != d {
		t.Errorf("+ %v in square %v: %d left, %d right, %d above, %d below", plus, box, l, r, u, d)
	}
}

// An icon button's square centers its glyph exactly at every scale: the
// glyph, placed on whole pixels as centered places it, has as much room
// on its left as on its right.
func TestIconBoxCenters(t *testing.T) {
	for _, scale := range []float32{1, 1.25, 1.5, 2, 3} {
		gtx := layout.Context{Metric: unit.Metric{PxPerDp: scale, PxPerSp: scale}}
		for _, b := range [][2]unit.Dp{{20, 13}, {24, 14}, {24, 16}, {28, 16}} {
			size, glyph := gtx.Dp(b[0]), gtx.Dp(b[1])
			box := iconBox(size, glyph)
			off := image.Pt(box, box).Sub(image.Pt(glyph, glyph)).Div(2).X // as centered offsets it
			if left, right := off, box-off-glyph; left != right || box < size-1 || box > size {
				t.Errorf("scale %v, %vdp button with a %vdp glyph: square %d, room %d left and %d right", scale, b[0], b[1], box, left, right)
			}
		}
	}
}
