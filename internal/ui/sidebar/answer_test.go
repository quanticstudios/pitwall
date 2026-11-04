package sidebar

import (
	"slices"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/pointer"

	"github.com/quanticstudios/pitwall/internal/model"
)

func TestFitPills(t *testing.T) {
	const gap, least, more = 4, 48, 30
	for _, tc := range []struct {
		name          string
		natural       []int
		w             int
		shown, widest int
	}{
		{"all fit", []int{60, 60}, 170, 2, 60},
		{"widest gives way first", []int{50, 200}, 170, 2, 116},
		{"both cut evenly", []int{200, 200}, 170, 2, 83},
		{"short pills stay whole", []int{40, 40, 40}, 128, 3, 40},
		{"too many leave for +N", []int{60, 60, 60, 60, 60, 60}, 170, 2, 60},
		{"no room for any", []int{200}, 20, 0, 0},
	} {
		shown, widest := fitPills(tc.natural, tc.w, gap, least, more)
		if shown != tc.shown || widest != tc.widest {
			t.Errorf("%s: got %d at %d, want %d at %d", tc.name, shown, widest, tc.shown, tc.widest)
		}
	}
}

// findOn moves the pointer over the sidebar until over reports it is on the
// button, and returns where.
func (h *replyHarness) findOn(over func() bool) f32.Point {
	h.t.Helper()
	for y := float32(56); y < 160; y += 2 {
		for x := float32(20); x < 280; x += 4 {
			h.r.Queue(pointer.Event{Kind: pointer.Move, Position: f32.Pt(x, y), Source: pointer.Mouse})
			h.frame()
			if over() {
				return f32.Pt(x, y)
			}
		}
	}
	h.t.Fatal("no such button on the row")
	return f32.Point{}
}

func (h *replyHarness) clickAt(p f32.Point) []Event {
	h.r.Queue(pointer.Event{Kind: pointer.Press, Position: p, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary})
	h.r.Queue(pointer.Event{Kind: pointer.Release, Position: p, Source: pointer.Mouse})
	return h.frame()
}

// A click belongs to the question it was pressed on: when the question or the
// button's label changes before the release, nothing is answered.
func TestClickKeepsItsQuestion(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(a *model.Activity)
		want   bool
	}{
		{"unchanged", func(*model.Activity) {}, true},
		{"new question", func(a *model.Activity) { a.UpdatedAt, a.Detail = time.Unix(8, 0), "Delete production?" }, false},
		{"new labels", func(a *model.Activity) { a.Buttons = []string{"No", "Yes"} }, false},
	} {
		h := newReplyHarness(t)
		a := &h.st.Activities[1]
		a.UpdatedAt, a.Detail, a.Buttons = time.Unix(7, 0), "Delete staging?", []string{"Yes", "No"}
		h.frame()
		r := h.s.row("w")
		p := h.findOn(func() bool { return r.choices.btn[0].Hovered() })
		h.r.Queue(pointer.Event{Kind: pointer.Press, Position: p, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary})
		h.frame()
		tc.change(a)
		h.frame()
		h.r.Queue(pointer.Event{Kind: pointer.Release, Position: p, Source: pointer.Mouse})
		pressed := slices.ContainsFunc(h.frame(), func(e Event) bool { _, ok := e.(Press); return ok })
		if pressed != tc.want {
			t.Errorf("%s: pressed %v, want %v", tc.name, pressed, tc.want)
		}
	}
}

// A question's option presses its label for the prompt the row shows, a
// narrow row's hidden ones open the tab, and on a menu without buttons Reply
// opens the tab instead of a sheet whose Enter would pick an option.
func TestRowChoices(t *testing.T) {
	h := newReplyHarness(t)
	at := time.Unix(7, 0)
	a := &h.st.Activities[1]
	a.UpdatedAt = at
	a.Buttons, a.Menu = []string{"Redis", "SQLite"}, true
	h.frame()
	r := h.s.row("w")
	p := h.findOn(func() bool { return r.choices.btn[1].Hovered() })
	want := Event(Press{PaneID: "p", At: at.UnixNano(), Label: "SQLite"})
	if evs := h.clickAt(p); !slices.Contains(evs, want) {
		t.Fatalf("SQLite sent %v", evs)
	}

	a.Buttons = []string{"Frankfurt", "Amsterdam", "Helsinki", "Warsaw", "Dublin", "Madrid"}
	h.frame()
	p = h.findOn(func() bool { return r.choices.more.Hovered() })
	if evs := h.clickAt(p); !slices.ContainsFunc(evs, func(e Event) bool { _, ok := e.(SelectWorkspace); return ok }) {
		t.Fatalf("+N sent %v", evs)
	}

	a.Buttons = nil
	h.frame()
	p = h.findOn(func() bool { return r.reply.Hovered() })
	evs := h.clickAt(p)
	if h.s.reply.ws != "" || !slices.ContainsFunc(evs, func(e Event) bool { _, ok := e.(SelectWorkspace); return ok }) {
		t.Fatalf("Reply on a menu: sheet on %q, sent %v", h.s.reply.ws, evs)
	}

	a.Menu = false
	h.frame()
	h.clickAt(h.findOn(func() bool { return r.reply.Hovered() }))
	if h.s.reply.ws != "w" {
		t.Fatal("Reply on a plain question opened no sheet")
	}
}
