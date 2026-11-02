package app

import (
	"image"
	"testing"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

const askForm = `<form title="Release">
  <input id="tag" label="Tag" value="v1."/>
  <choice id="env"><option>staging</option><option selected>prod</option></choice>
  <button id="ship">Ship</button>
  <button cancel>Not now</button>
</form>`

// formWindow is a window on the fake backend with a frame and a key
// helper, its first tab shown.
func formWindow(t *testing.T) (*FakeBackend, *ui, func(), func(...event.Event)) {
	b := NewFakeBackend()
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: conventional}}
	var r input.Router
	var ops op.Ops
	frame := func() {
		ops.Reset()
		gtx := gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: gl.Exact(image.Pt(1280, 800)), Now: time.Now()}
		u.layout(gtx)
		r.Frame(&ops)
	}
	queue := func(evs ...event.Event) {
		for _, e := range evs {
			r.Queue(e)
		}
		frame()
		frame()
	}
	frame()
	frame()
	return b, u, frame, queue
}

// sentOf is the messages of type T b was sent.
func sentOf[T any](b *FakeBackend) []T {
	var out []T
	for _, m := range b.Sent() {
		if v, ok := m.(T); ok {
			out = append(out, v)
		}
	}
	return out
}

// A form over the shown tab takes the keys from its panes: typing goes
// into its first field and Enter answers with that text, the choice's
// pick and the first button; the pane gets none of it.
func TestFormTakesKeysAndAnswers(t *testing.T) {
	b, u, frame, queue := formWindow(t)
	pane := u.nav.focused()
	b.AddForm(model.Form{ID: "f1", WorkspaceID: u.nav.workspace, PaneID: pane, Spec: askForm, Title: "Release", Waits: true})
	frame()
	frame()
	if !u.formHoldsKeys(new(b.State())) {
		t.Fatal("a form that asks should hold the keys")
	}
	queue(key.EditEvent{Range: key.Range{Start: 3, End: 3}, Text: "4"})
	if u.forms["f1"].edit["tag"].Text() != "v1.4" {
		t.Fatalf("typed into the field: %q", u.forms["f1"].edit["tag"].Text())
	}
	queue(key.Event{Name: key.NameReturn, State: key.Press})
	got := sentOf[proto.FormAnswer](b)
	if len(got) != 1 || got[0].ID != "f1" || got[0].Cancel || got[0].Answers != `{"button":"ship","env":"prod","tag":"v1.4"}` {
		t.Fatalf("answers %+v", got)
	}
	if in := sentOf[proto.Input](b); len(in) != 0 {
		t.Fatalf("the pane got keys meant for the form: %+v", in)
	}
	if len(b.State().Forms) != 0 || u.formHoldsKeys(new(b.State())) {
		t.Fatal("an answered form should be gone")
	}
}

// Escape cancels a form; a dashboard leaves the keys to the pane.
func TestFormEscapeAndDashboard(t *testing.T) {
	b, u, frame, queue := formWindow(t)
	pane := u.nav.focused()
	b.AddForm(model.Form{ID: "f2", WorkspaceID: u.nav.workspace, PaneID: pane, Spec: askForm, Waits: true})
	frame()
	queue(key.Event{Name: key.NameEscape, State: key.Press})
	if got := sentOf[proto.FormAnswer](b); len(got) != 1 || !got[0].Cancel || got[0].Answers != "" {
		t.Fatalf("Escape sent %+v", got)
	}

	b.AddForm(model.Form{ID: "d", WorkspaceID: u.nav.workspace, PaneID: pane, Spec: `<form title="Build"><progress label="Tests" value="3" max="4"/></form>`})
	frame()
	if u.formHoldsKeys(new(b.State())) {
		t.Fatal("a dashboard should leave the keys to the panes")
	}
	queue(key.Event{Name: "Q", State: key.Press}, key.EditEvent{Text: "q"})
	if in := sentOf[proto.Input](b); len(in) != 1 || in[0].Pane != pane || string(in[0].Data) != "q" {
		t.Fatalf("with a dashboard up the pane got %+v", in)
	}
	if u.forms["d"] == nil || u.forms["d"].f == nil {
		t.Fatal("the dashboard was not drawn")
	}
}

// Tab and Shift+Tab step through the fields in the form's order.
func TestFormTab(t *testing.T) {
	b, u, frame, queue := formWindow(t)
	b.AddForm(model.Form{ID: "t", WorkspaceID: u.nav.workspace, PaneID: u.nav.focused(), Waits: true,
		Spec: `<form><input id="a"/><input id="b" lines="3"/><input id="c"/></form>`})
	frame()
	frame()
	fu := u.forms["t"]
	for _, c := range []struct {
		mods key.Modifiers
		want string
	}{{0, "b"}, {0, "c"}, {0, "a"}, {key.ModShift, "c"}} {
		queue(key.Event{Name: key.NameTab, Modifiers: c.mods, State: key.Press})
		queue(key.EditEvent{Text: "x"})
		if got := fu.edit[c.want].Text(); got == "" {
			t.Fatalf("after Tab (%v) field %s is empty: a %q b %q c %q", c.mods, c.want, fu.edit["a"].Text(), fu.edit["b"].Text(), fu.edit["c"].Text())
		}
		fu.edit[c.want].SetText("")
	}
}

// An update keeps what the user typed and picked in fields whose ids
// stay, and takes the new spec's other items.
func TestFormSyncKeepsInput(t *testing.T) {
	var fu formUI
	fu.sync(askForm)
	fu.edit["tag"].SetText("v2")
	fu.picked["env"] = map[string]bool{"staging": true}
	fu.sync(`<form><input id="tag"/><input id="note" value="hi"/><choice id="env"><option>staging</option><option selected>prod</option></choice></form>`)
	if fu.edit["tag"].Text() != "v2" || fu.edit["note"].Text() != "hi" || !fu.picked["env"]["staging"] || fu.picked["env"]["prod"] {
		t.Fatalf("after an update: tag %q note %q env %v", fu.edit["tag"].Text(), fu.edit["note"].Text(), fu.picked["env"])
	}
	fu.sync("<form><frob/></form>")
	if fu.f != nil || fu.bad == "" || len(fu.buttons()) != 1 || !fu.buttons()[0].Cancel {
		t.Fatalf("a spec that does not parse should offer Cancel alone: %+v", fu.buttons())
	}
}
