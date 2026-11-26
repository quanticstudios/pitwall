package mobile

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/session"
)

var phoneCommands = []session.Command{
	{Name: "compact", Description: "Free up context by summarizing the conversation so far", Hint: "[instructions]"},
	{Name: "config", Description: "Open settings", Terminal: true},
	{Name: "review", Description: "Review a pull request"},
	{Name: "code-review", Description: "Review the diff for correctness bugs", Source: "user"},
	{Name: "release", Description: "Cut a release and tag it", Source: "project"},
	{Name: "pdf", Description: "Read and fill PDF forms", Source: "plugin"},
}

func slashNames(cs []session.Command) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

// The phone's / menu: a /word at the start lists every command, one in
// the middle only skills, Codex none there; a tap on a row puts /name in
// place of the word; Back shuts it until the draft changes; a chat from
// an older daemon, which sends no commands, gets none. With
// PITWALL_SLASH_SHOTS set it writes the menus as PNGs there.
func TestPhoneSlashMenu(t *testing.T) {
	th := Theme()
	c := newChat("p1")
	env := chatEnv{Online: true, Open: true, Tab: remote.Tab{Item: remote.Item{Pane: "p1", Agent: "claude", State: "completed", Tab: "skill menu"}, Send: true, Picker: true}}
	const w, h, scale = 400, 760, 2
	win, err := headless.NewWindow(w*scale, h*scale)
	if err != nil {
		t.Skip(err)
	}
	defer win.Release()
	var r input.Router
	var ops op.Ops
	frame := func() {
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: scale, PxPerSp: scale},
			Constraints: layout.Exact(image.Pt(w*scale, h*scale)), Now: time.Unix(1e9, 0)}
		c.layout(gtx, th, env)
		r.Frame(&ops)
	}
	draft := func(s string, caret int) []string {
		t.Helper()
		setDraft(&c.cmp.ed, s)
		c.cmp.ed.SetCaret(caret, caret)
		frame()
		frame()
		return slashNames(c.slashes.Rows())
	}
	shot := func(name string) {
		dir := os.Getenv("PITWALL_SLASH_SHOTS")
		if dir == "" {
			return
		}
		if err := win.Frame(&ops); err != nil {
			t.Fatal(err)
		}
		img := image.NewRGBA(image.Rect(0, 0, w*scale, h*scale))
		if err := win.Screenshot(img); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		png.Encode(f, img)
	}
	tap := func(label string) {
		t.Helper()
		for _, n := range r.AppendSemantics(nil) {
			if n.Desc.Label == label {
				p := f32.Pt(float32(n.Desc.Bounds.Min.X+n.Desc.Bounds.Max.X)/2, float32(n.Desc.Bounds.Min.Y+n.Desc.Bounds.Max.Y)/2)
				r.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Touch, PointerID: 1, Position: p},
					pointer.Event{Kind: pointer.Release, Source: pointer.Touch, PointerID: 1, Position: p})
				frame()
				frame()
				return
			}
		}
		t.Fatalf("no row %q", label)
	}

	frame()
	r.Source().Execute(key.FocusCmd{Tag: &c.cmp.ed}) // the field has the keyboard, as after a tap on it
	if got := draft("/re", 3); got != nil {
		t.Fatalf("an older daemon's chat listed %q", got)
	}
	c.slashes.cmds = phoneCommands
	if got := draft("/", 1); !slices.Equal(got, slashNames(phoneCommands)) {
		t.Fatalf("/ lists %q", got)
	}
	if got := draft("/re", 3); !slices.Equal(got, []string{"review", "release", "code-review"}) {
		t.Fatalf("/re lists %q", got)
	}
	shot("phone-slash-start.png")
	tap("/release")
	if got, caret := c.cmp.ed.Text(), c.cmp.ed.Len(); got != "/release " || caret != len("/release ") {
		t.Fatalf("pick: %q", got)
	}
	if len(c.slashes.Rows()) != 0 {
		t.Fatalf("the menu stayed open after a pick: %q", slashNames(c.slashes.Rows()))
	}

	if got := draft("please run /re and then push", 14); !slices.Equal(got, []string{"release", "code-review"}) {
		t.Fatalf("mid-draft /re lists %q", got)
	}
	shot("phone-slash-mid.png")
	tap("/code-review")
	if got, _ := c.cmp.ed.Selection(); c.cmp.ed.Text() != "please run /code-review and then push" || got != 24 {
		t.Fatalf("mid-draft pick: %q, caret %d", c.cmp.ed.Text(), got)
	}

	for _, s := range []string{"use /com", "see /tmp/x", "and/or", "see https://x.io/a"} {
		if got := draft(s, len(s)); got != nil {
			t.Errorf("%q lists %q", s, got)
		}
	}

	if draft("/re", 3); !c.handleBack(C{}) || c.slashes.Rows() != nil {
		t.Fatal("Back left the menu up")
	}
	if got := draft("/re", 3); got != nil {
		t.Fatalf("the menu came back on the same draft: %q", got)
	}
	if got := draft("/rel", 4); !slices.Equal(got, []string{"release"}) {
		t.Fatalf("after Back, typing on lists %q", got)
	}

	env.Tab.Agent = "codex"
	for _, s := range []string{"run /re", "run $re", "$re"} {
		if got := draft(s, len(s)); got != nil {
			t.Fatalf("codex %q lists %q", s, got)
		}
	}
	if got := draft("/rel", 4); !slices.Equal(got, []string{"release"}) {
		t.Fatalf("codex at the start lists %q", got)
	}
	tap("$release")
	if got := c.cmp.ed.Text(); got != "$release " { // Codex reads a skill as $name
		t.Fatalf("codex pick: %q", got)
	}
}
