//go:build !android

package mobile

import (
	"image"
	"slices"
	"testing"
	"time"

	"gioui.org/app"
	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/phone"
	"github.com/quanticstudios/pitwall/internal/remote"
)

func names(items []triageItem) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Tab.Tab)
	}
	return out
}

// TestTriageOrder: needs you first, oldest first, then working, then
// done, across machines; a card deferred with later sinks below the other
// needs-you cards until the agent asks something new; a terminal is no agent.
func TestTriageOrder(t *testing.T) {
	now := time.Date(2026, 11, 27, 9, 41, 0, 0, time.UTC)
	ms, tabs := demoFleet(now)
	sh := tabs["d"][0]
	sh.Tab, sh.Pane, sh.Agent, sh.State = "shell", "p-shell", "terminal", ""
	tabs["d"] = append(tabs["d"], sh)
	tabsOf := func(id string) []remote.Tab { return tabs[id] }
	want := []string{"arke", "since-you-left", "billing", "sinter", "question-answer", "phone-polish"}
	if got := names(triage(ms, tabsOf, nil)); !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	later := map[string]int64{phone.NS("b", "p-arke"): tabs["b"][0].At}
	want = []string{"since-you-left", "billing", "arke", "sinter", "question-answer", "phone-polish"}
	if got := names(triage(ms, tabsOf, later)); !slices.Equal(got, want) {
		t.Fatalf("with arke later, order = %v, want %v", got, want)
	}
	tabs["b"][0].At++ // a new question
	if got := triage(ms, tabsOf, later); got[0].Tab.Tab != "arke" || got[0].Later {
		t.Fatalf("a new question kept the card later: %v", names(got))
	}
}

// TestSwipeVerdict: a swipe acts past 30% of the card's width either way.
func TestSwipeVerdict(t *testing.T) {
	for _, c := range []struct {
		dx    float32
		width int
		want  int
	}{
		{0, 300, 0}, {89, 300, 0}, {90, 300, 1}, {250, 300, 1},
		{-89, 300, 0}, {-90, 300, -1}, {40, 0, 0},
	} {
		if got := swipeVerdict(c.dx, c.width); got != c.want {
			t.Errorf("swipeVerdict(%v, %d) = %d, want %d", c.dx, c.width, got, c.want)
		}
	}
}

// feedApp is demoFleetApp showing the feed, with a frame function at
// 360x760 px, 1 px per dp.
func feedApp(t *testing.T) (*App, *input.Router, func()) {
	now := time.Date(2026, 11, 27, 9, 41, 0, 0, time.UTC)
	a := demoFleetApp(Theme(), now)
	a.w, a.ui = new(app.Window), make(chan func(), 16)
	a.list.feed = true
	for _, l := range a.links {
		l.conn = new(phone.Conn) // never started: Watch only records the pane
	}
	r := new(input.Router)
	var ops op.Ops
	return a, r, func() {
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Source: r.Source(), Now: now,
			Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(360, 760))}
		a.layoutFrame(gtx)
		r.Frame(&ops)
	}
}

// drag moves a finger from at by dx, in steps, and lifts it.
func drag(r *input.Router, frame func(), at f32.Point, dx float32) {
	r.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Touch, PointerID: 1, Position: at})
	frame()
	for i := 1; i <= 4; i++ {
		r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Touch, PointerID: 1, Position: at.Add(f32.Pt(dx*float32(i)/4, 0))})
		frame()
	}
	r.Queue(pointer.Event{Kind: pointer.Release, Source: pointer.Touch, PointerID: 1, Position: at.Add(f32.Pt(dx, 0))})
	frame()
}

// TestTriageSwipeLater: a swipe left on the first card defers it and
// opens nothing; a short one does nothing; a swipe right on a question
// opens its chat.
func TestTriageSwipeLater(t *testing.T) {
	a, r, frame := feedApp(t)
	frame()
	frame()
	// The first card sits under the header and the views row, about 200
	// px down at 1 px per dp zoomed.
	at := f32.Pt(250, 240)
	drag(r, frame, at, -60)
	if len(a.triage.later) != 0 || a.chat.pane != "" {
		t.Fatalf("a short swipe acted: later %v, chat %q", a.triage.later, a.chat.pane)
	}
	drag(r, frame, at, -200)
	if _, ok := a.triage.later[phone.NS("b", "p-arke")]; !ok || a.chat.pane != "" {
		t.Fatalf("a swipe left did not defer arke: later %v, chat %q", a.triage.later, a.chat.pane)
	}
	a.triage.later = nil
	frame()
	drag(r, frame, f32.Pt(100, 240), 200)
	if a.chat.pane != "p-arke" {
		t.Fatalf("a swipe right on a question opened %q, want p-arke", a.chat.pane)
	}
}

// TestTriageAllowTapStays: a tap on a card's Allow answers it and does
// not also open its chat, though the card's own tap area holds the button.
func TestTriageAllowTapStays(t *testing.T) {
	a, r, frame := feedApp(t)
	frame()
	frame()
	c := a.triage.card(phone.NS("d", "p-billing"))
	var allowAt f32.Point
	for y := float32(200); y < 760 && allowAt == (f32.Point{}); y += 4 {
		// Find the button by pressing down the right half until Allow reads pressed.
		r.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Touch, PointerID: 1, Position: f32.Pt(270, y)})
		frame()
		if c.allow.Pressed() {
			allowAt = f32.Pt(270, y)
		}
		r.Queue(pointer.Event{Kind: pointer.Cancel, Source: pointer.Touch, PointerID: 1})
		frame()
	}
	if allowAt == (f32.Point{}) {
		t.Fatal("found no Allow button on billing's card")
	}
	r.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Touch, PointerID: 1, Position: allowAt},
		pointer.Event{Kind: pointer.Release, Source: pointer.Touch, PointerID: 1, Position: allowAt})
	frame()
	if a.chat.pane != "" {
		t.Fatalf("a tap on Allow opened chat %q", a.chat.pane)
	}
	if c.sentAt == 0 {
		t.Fatal("a tap on Allow sent no answer")
	}
}

// TestTriageInbox: the daemon's inbox puts a finished tab with failed CI
// in needs you, and its card says so.
func TestTriageInbox(t *testing.T) {
	tab := remote.Tab{Item: remote.Item{Pane: "p1", Tab: "ci", Agent: "claude", Label: "Done"}, Exited: true,
		Inbox: []model.InboxItem{{Kind: model.InboxCI, Text: "CI failed on #12"}}}
	if g := groupOf(tab); g != needsYou {
		t.Fatalf("group = %d, want needs you", g)
	}
	if l := cardLine(tab); l != "CI failed on #12" {
		t.Fatalf("line = %q", l)
	}
	tab.Inbox = nil
	if g := groupOf(tab); g != done {
		t.Fatalf("without inbox, group = %d, want done", g)
	}
}
