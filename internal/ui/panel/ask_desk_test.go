package panel

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/chatin"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// desk lays out a chat as the window does: the transcript over the
// composer, the composer answering through the transcript. With
// PITWALL_RENDER_DIR set it also draws offscreen and saves shots there.
type desk struct {
	t   *testing.T
	tr  Transcript
	c   chatin.Composer
	s   *session.Snapshot
	th  *theme.Theme
	ops op.Ops
	r   input.Router
	win *headless.Window
	now time.Time
}

func newDesk(t *testing.T, s *session.Snapshot) *desk {
	c, _ := config.Builtin("aide-dark")
	th, err := theme.New(c, config.Font{UISize: 13})
	if err != nil {
		t.Fatal(err)
	}
	d := &desk{t: t, s: s, th: th, now: time.Date(2026, 11, 21, 18, 0, 0, 0, time.UTC)}
	d.tr.Width = 760
	if os.Getenv("PITWALL_RENDER_DIR") != "" {
		if d.win, err = headless.NewWindow(1000, 640); err != nil {
			t.Skip(err)
		}
		t.Cleanup(d.win.Release)
	}
	d.frame()
	d.frame()
	return d
}

func (d *desk) frame() {
	d.ops.Reset()
	d.now = d.now.Add(16 * time.Millisecond)
	gtx := layout.Context{Ops: &d.ops, Source: d.r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(1000, 640)), Now: d.now}
	env := chatin.Env{Chat: "c", Snap: d.s, Width: 760, Focus: !d.tr.Typing(), Key: d.tr.Key, Answer: d.tr.Answer}
	layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return d.tr.Layout(gtx, d.th, d.s, false) }),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return d.c.Layout(gtx, d.th, env) }),
	)
	d.r.Frame(&d.ops)
	if d.win != nil {
		if err := d.win.Frame(&d.ops); err != nil {
			d.t.Fatal(err)
		}
	}
}

// shot saves the last frame as name.png when PITWALL_RENDER_DIR is set.
func (d *desk) shot(name string) {
	if d.win == nil {
		return
	}
	img := image.NewRGBA(image.Rect(0, 0, 1000, 640))
	if err := d.win.Screenshot(img); err != nil {
		d.t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(os.Getenv("PITWALL_RENDER_DIR"), name+".png"))
	if err != nil {
		d.t.Fatal(err)
	}
	defer f.Close()
	png.Encode(f, img)
}

func (d *desk) typ(s string) {
	d.r.Queue(key.EditEvent{Range: key.Range{Start: -1, End: -1}, Text: s})
	d.frame()
}

func askSnap(qs ...session.Question) *session.Snapshot {
	at := session.Stamp(time.Date(2026, 11, 21, 17, 59, 0, 0, time.UTC))
	return &session.Snapshot{SessionID: "c", Provider: "claude", SessionState: session.StateWaiting,
		Content:           []session.Item{{ID: "p1", Kind: session.KindUserPrompt, CreatedAt: at, UpdatedAt: at, Text: "Add a settings page"}},
		PendingUserInputs: []session.InputRequest{{ID: "i1", Questions: qs}}}
}

// TestAskDesktop: in the window's layout a click on an option sends a
// one-question card, and the next frame draws its answered line, not the
// card; on a two-question card with a recommended option the keys stay in
// the composer after a click, and text typed there answers the question
// the user has not confirmed, or the one Other was picked on.
func TestAskDesktop(t *testing.T) {
	d := newDesk(t, askSnap(session.Question{ID: "where", Header: "Layout", Prompt: "Where should the settings live?",
		Options: []session.PromptOption{{Label: "Sidebar", Description: "A panel beside the chat"}, {Label: "Top bar", Description: "A menu in the header"}}}))
	d.shot("ask-one")
	d.tr.asks["i1"].opts[0][1].Click()
	d.frame()
	if got := d.tr.Inputs(); len(got) != 1 || got[0].Answers["where"][0] != "Top bar" {
		t.Fatalf("one question: %+v", got)
	}
	d.s.PendingUserInputs = nil // as the daemon resolves it
	d.frame()
	d.shot("ask-one-answered")

	d = newDesk(t, askSnap(
		session.Question{ID: "where", Header: "Layout", Prompt: "Where should the settings live?",
			Options: []session.PromptOption{{Label: "Sidebar", Description: "A panel beside the chat"}, {Label: "Top bar", Description: "A menu in the header"}}},
		session.Question{ID: "size", Header: "Size", Prompt: "How wide is the panel?",
			Options: []session.PromptOption{{Label: "Narrow (Recommended)", Description: "320 px"}, {Label: "Wide", Description: "480 px"}}}))
	d.shot("ask-two-recommended")
	d.tr.asks["i1"].opts[0][0].Click()
	d.frame()
	d.frame()
	if len(d.tr.Inputs()) != 0 {
		t.Fatal("a pick sent the unconfirmed recommendation")
	}
	d.shot("ask-two-midway")
	d.typ("Medium, 400 px")
	if d.c.Text() != "Medium, 400 px" {
		t.Fatalf("the composer lost the keys after the click: %q", d.c.Text())
	}
	d.r.Queue(key.Event{Name: key.NameReturn, State: key.Press})
	d.frame()
	got := d.tr.Inputs()
	if len(got) != 1 || got[0].Answers["where"][0] != "Sidebar" || got[0].Answers["size"][0] != "Medium, 400 px" || d.c.Text() != "" || len(d.c.Messages()) != 0 {
		t.Fatalf("typed answer: %+v, box %q", got, d.c.Text())
	}
	d.frame() // the window's invalidate after the composer answered
	if c, l := drawnCards(&d.tr); c != 0 || l != 1 || d.tr.done[0].what != "Sidebar, Medium, 400 px" {
		t.Fatalf("after the typed answer: %d cards, %d lines", c, l)
	}
	d.shot("ask-two-answered")

	// Other's row keeps the keys in the composer, and Enter there answers
	// the question Other was picked on.
	d = newDesk(t, askSnap(session.Question{ID: "where", Prompt: "Where?", Options: []session.PromptOption{{Label: "Sidebar"}, {Label: "Top bar"}}}))
	d.tr.asks["i1"].opts[0][2].Click()
	d.frame()
	d.typ("Mauve")
	d.r.Queue(key.Event{Name: key.NameReturn, State: key.Press})
	d.frame()
	if got := d.tr.Inputs(); len(got) != 1 || got[0].Answers["where"][0] != "Mauve" || d.c.Text() != "" {
		t.Fatalf("Other: %+v, box %q", got, d.c.Text())
	}
}
