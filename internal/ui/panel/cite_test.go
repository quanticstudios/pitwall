package panel

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gioui.org/gpu/headless"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/chatin"
)

// click is a primary press and release at p.
func (g *selRig) click(p image.Point) {
	g.now = g.now.Add(time.Second)
	g.press(p, 0)
	g.release(p)
	g.frame()
}

// A selection within one reply gets the toolbar under its end; Cite in
// composer quotes the text with where it came from and drops the
// selection; Copy copies it and keeps it. A selection that leaves the
// reply, or lies in a prompt, gets none.
func TestCiteToolbar(t *testing.T) {
	g := newSelRig(t, chatFixture())
	g.tr.CopyOnSelect = false
	first, last := g.box("a1/0"), g.box("a1/2")
	g.drag(left(first).Add(image.Pt(4, 0)), right(last))
	r := g.tr.cite.r
	if r.Empty() {
		t.Fatal("no toolbar over a selection within a reply")
	}
	if r.Min.Y < last.Min.Y {
		t.Fatalf("the toolbar %v is above the selection's end %v", r, last)
	}
	// Copy, on the toolbar's right half, keeps the selection.
	g.click(image.Pt(r.Max.X-10, (r.Min.Y+r.Max.Y)/2))
	if len(g.copies) != 1 || !strings.Contains(g.copies[0], "Then it wraps") || !g.tr.span.has() {
		t.Fatalf("Copy: copies %q, selection %v", g.copies, g.tr.span.has())
	}
	g.click(image.Pt(r.Min.X+20, (r.Min.Y+r.Max.Y)/2))
	cites := g.tr.Cites()
	if len(cites) != 1 {
		t.Fatalf("cites %+v", cites)
	}
	c := cites[0]
	// Its blocks a blank line apart, the code's lines as they are.
	if !strings.HasSuffix(c.Text, "cursor moves past the edge.\n\nx := 1\ny := 2\n\nThen it wraps.") || strings.HasPrefix(c.Text, "The") {
		t.Fatalf("cite text %q", c.Text)
	}
	if c.Src.Item != "a1" || c.Src.FromKey != "a1/0" || c.Src.ToKey != "a1/2" || c.Src.From == 0 {
		t.Fatalf("cite source %+v", c.Src)
	}
	if g.tr.span.has() || !g.tr.cite.r.Empty() || len(g.tr.Cites()) != 0 {
		t.Fatal("the selection or the toolbar stayed after Cite")
	}
	// Across a reply and the next prompt: no toolbar.
	g.drag(left(g.box("a1/2")), right(g.box("p2")))
	if !g.tr.span.has() || !g.tr.cite.r.Empty() {
		t.Fatalf("toolbar over a selection that leaves the reply: has %v r %v", g.tr.span.has(), g.tr.cite.r)
	}
	g.drag(left(g.box("p2")), right(g.box("p2")))
	if !g.tr.cite.r.Empty() {
		t.Fatal("toolbar over a prompt")
	}
	// While the button is held, none.
	g.tr.ClearSelection()
	g.now = g.now.Add(time.Second)
	a := g.box("a2/0")
	g.press(left(a), 0)
	g.move(right(a))
	if !g.tr.cite.r.Empty() {
		t.Fatal("toolbar during the drag")
	}
	g.release(right(a))
	g.frame()
	if g.tr.cite.r.Empty() {
		t.Fatal("no toolbar after the release")
	}
}

// A selection longer than a quote takes shows the toolbar's Cite dim and
// inert.
func TestCiteTooLong(t *testing.T) {
	s := chatFixture()
	s.Content[4].Text = strings.Repeat("word ", chatin.MaxQuote/4)
	g := &selRig{t: t, s: s, size: image.Pt(1000, 4000)}
	g.start()
	g.tr.CopyOnSelect = false
	b := g.box("a2/0")
	g.drag(image.Pt(b.Min.X+1, b.Min.Y+2), image.Pt(b.Max.X-1, b.Max.Y-2))
	r := g.tr.cite.r
	if r.Empty() {
		t.Fatal("no toolbar")
	}
	g.click(image.Pt(r.Min.X+20, (r.Min.Y+r.Max.Y)/2))
	if len(g.tr.Cites()) != 0 {
		t.Fatal("a too long selection was cited")
	}
}

// Reveal takes a reply's id only, scrolls its source into view from far
// above and flashes it, fading out.
func TestReveal(t *testing.T) {
	s := chatFixture()
	for i := range 40 {
		id := string(rune('A'+i%26)) + strings.Repeat("x", i/26)
		s.Content = append(s.Content, session.Item{ID: "p" + id, Kind: session.KindUserPrompt, Text: "more " + id, TurnID: tid("t" + id)},
			session.Item{ID: "r" + id, Kind: session.KindAssistantText, Text: "reply " + id, TurnID: tid("t" + id)})
	}
	g := newSelRig(t, s)
	for range 3 {
		g.frame()
	}
	if g.tr.Reveal(chatin.Source{Item: "p1"}) || g.tr.Reveal(chatin.Source{Item: "gone"}) {
		t.Fatal("Reveal took a prompt or a missing item")
	}
	src := chatin.Source{Item: "a1", FromKey: "a1/2", From: 0, ToKey: "a1/2", To: 4}
	if !g.tr.Reveal(src) {
		t.Fatal("Reveal refused a reply")
	}
	for range 6 {
		g.frame()
	}
	b := g.box("a1/2")
	if b.Min.Y < 0 || b.Max.Y > g.size.Y {
		t.Fatalf("the source %v is not in the view %v", b, g.size)
	}
	if g.tr.cite.flash.Item != "a1" || g.tr.cite.goTo != nil {
		t.Fatalf("flash %+v, goTo %v", g.tr.cite.flash, g.tr.cite.goTo)
	}
	g.now = g.now.Add(flashFor)
	g.frame()
	if g.tr.cite.flash.Item != "" {
		t.Fatal("the flash never ended")
	}
}

// TestCiteShots draws the toolbar over a selection within a reply, and a
// revealed source mid-flash, to $PITWALL_SELECT_SHOTS. It skips without
// it.
func TestCiteShots(t *testing.T) {
	dir := os.Getenv("PITWALL_SELECT_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_SELECT_SHOTS is not set")
	}
	shot := func(name string, act func(g *selRig)) {
		g := &selRig{t: t, s: richChat(), size: image.Pt(1350, 1300), scale: 1.5}
		g.tr.init()
		g.start()
		g.tr.CopyOnSelect = false
		act(g)
		win, err := headless.NewWindow(g.size.X, g.size.Y)
		if err != nil {
			t.Skip("no headless GPU:", err)
		}
		defer win.Release()
		if err := win.Frame(&g.ops); err != nil {
			t.Fatal(err)
		}
		img := image.NewRGBA(image.Rectangle{Max: g.size})
		if err := win.Screenshot(img); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
	}
	shot("cite-toolbar.png", func(g *selRig) {
		r := g.box("a1/1")
		g.drag(image.Pt(r.Min.X+r.Dx()/5, r.Min.Y+4), image.Pt(r.Min.X+r.Dx()*3/4, r.Max.Y-4))
	})
	shot("cite-flash.png", func(g *selRig) {
		g.tr.Reveal(chatin.Source{Item: "a1", FromKey: "a1/1", From: 4, ToKey: "a1/1", To: 60})
		for range 4 {
			g.frame()
		}
		g.now = g.now.Add(flashFor / 4)
		g.frame()
	})
}
