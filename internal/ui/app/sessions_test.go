package app

import (
	"image"
	"slices"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// The switcher's keys: j/k and arrows move, digits jump, typing filters
// (then every letter is the filter's), Esc clears the filter before it
// closes, n and r type a name inline with the suggestion selected, x asks
// before it kills.
func TestSessionSwitcherKeys(t *testing.T) {
	b := NewFakeBackend()
	st := b.State()
	now := time.Now()
	var s sessionSwitcher
	k := func(n key.Name, m key.Modifiers) switcherResult { return s.key(&st, "a", press(n, m), now) }
	s.openAt(&st, "s1", "pick", now)
	if !s.open || s.sel != "s1" {
		t.Fatalf("open on %q", s.sel)
	}
	k("J", 0)
	k(key.NameDownArrow, 0)
	if s.sel != "s3" {
		t.Fatalf("two down: %s", s.sel)
	}
	k(key.NameDownArrow, 0) // the last row stays
	k("K", 0)
	if s.sel != "s2" {
		t.Fatalf("up: %s", s.sel)
	}
	if r := k(key.NameReturn, 0); r.show != "s2" || s.open {
		t.Fatalf("Enter: %+v open %v", r, s.open)
	}
	s.openAt(&st, "s1", "pick", now)
	if r := k("3", 0); r.show != "s3" || s.open {
		t.Fatalf("3: %+v", r)
	}

	// Filtering: "he" keeps calm-heron; j and k now type.
	s.openAt(&st, "s1", "pick", now)
	k("H", 0)
	k("E", 0)
	if rows := s.rows(&st); len(rows) != 1 || rows[0].Name != "calm-heron" || s.sel != "s2" {
		t.Fatalf("filter %q: %v sel %s", s.filter, rows, s.sel)
	}
	k("J", 0)
	if s.filter != "hej" || len(s.rows(&st)) != 0 {
		t.Fatalf("j in a filter: %q", s.filter)
	}
	k(key.NameDeleteBackward, 0)
	k(key.NameEscape, 0)
	if !s.open || s.filter != "" || s.filtering {
		t.Fatalf("Esc clears the filter first: open %v %q", s.open, s.filter)
	}
	k("/", 0)
	k("N", 0) // types, not new
	if s.mode != modePick || s.filter != "n" {
		t.Fatalf("/ then n: mode %v %q", s.mode, s.filter)
	}
	k(key.NameEscape, 0)
	k(key.NameEscape, 0)
	if s.open {
		t.Fatal("second Esc did not close")
	}

	// New: the suggestion is replaced by typing; a taken name is refused.
	s.openAt(&st, "s1", "pick", now)
	k("N", 0)
	if s.mode != modeNew || !s.fresh || !model.IsSessionName(s.field) || st.SessionNamed(s.field) != nil {
		t.Fatalf("new: mode %v %q", s.mode, s.field)
	}
	for _, c := range "calm" {
		k(key.Name(string(c-'a'+'A')), 0)
	}
	k(key.NameSpace, 0)
	for _, c := range "heron" {
		k(key.Name(string(c-'a'+'A')), 0)
	}
	if r := k(key.NameReturn, 0); r.send != nil || s.err == "" || s.mode != modeNew {
		t.Fatalf("taken name: %+v err %q", r, s.err)
	}
	k(key.NameDeleteBackward, 0)
	if r := k(key.NameReturn, 0); r.send != (proto.SessionNew{Name: "calm-hero", FromPane: "a"}) || r.newSession != "calm-hero" || s.open {
		t.Fatalf("new: %+v open %v", r, s.open)
	}

	// Rename the highlighted session; Esc leaves the name alone.
	s.openAt(&st, "s1", "pick", now)
	k("J", 0)
	k("R", 0)
	if s.mode != modeRename || s.field != "calm-heron" {
		t.Fatalf("rename: %v %q", s.mode, s.field)
	}
	k("D", 0)
	k("B", 0)
	if r := k(key.NameReturn, 0); r.send != (proto.SessionRename{SessionID: "s2", Name: "db"}) || s.mode != modePick || !s.open {
		t.Fatalf("rename: %+v", r)
	}
	k("R", 0)
	k(key.NameEscape, 0)
	if s.mode != modePick || !s.open {
		t.Fatal("Esc in rename closed the switcher")
	}

	// Kill asks first; n keeps the session, y kills it.
	k("X", 0)
	if r := k("N", 0); r.send != nil || s.mode != modePick {
		t.Fatalf("n killed: %+v", r)
	}
	k("X", 0)
	if r := k("Y", 0); r.send != (proto.SessionKill{SessionID: "s2"}) || s.mode != modePick {
		t.Fatalf("y: %+v", r)
	}

	// A session that goes away moves the highlight to one that is there.
	b.Send(proto.SessionKill{SessionID: "s2"})
	st = b.State()
	s.fix(&st, now)
	if s.sel != "s1" {
		t.Fatalf("highlight on a killed session: %s", s.sel)
	}
}

// jump_attention crosses sessions: the newest unseen pane lives in
// calm-heron, so the window switches there.
func TestJumpAttentionAcrossSessions(t *testing.T) {
	b := NewFakeBackend()
	b.mu.Lock()
	for i := range b.st.Activities {
		a := &b.st.Activities[i]
		a.UpdatedAt = time.Now().Add(-time.Hour)
		if a.PaneID == "n" {
			a.UpdatedAt = time.Now()
		}
	}
	b.mu.Unlock()
	st := b.State()
	n := nav{keys: aide}
	n.sync(&st)
	n.key(&st, press("U", key.ModAlt))
	if n.session != "s2" || n.workspace != "w10" || n.focused() != "n" {
		t.Fatalf("at %s %s/%s", n.session, n.workspace, n.focused())
	}
	// Going back to the first session lands on the tab it showed.
	n.switchSession(&st, "s1")
	if n.workspace != "w1" {
		t.Fatalf("back in s1 at %s", n.workspace)
	}
	n.cycleSession(&st, -1)
	if n.session != "s3" {
		t.Fatalf("prev from s1 wraps to s3: %s", n.session)
	}
}

// The real window: the conventional key opens the switcher over the panes,
// typed keys reach it and not the pane, Enter switches the window and the
// daemon hears which session it shows. The sidebar header's name follows.
func TestSessionSwitcherWindow(t *testing.T) {
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
	frame()
	frame()
	if !slices.Contains(b.Sent(), any(proto.SessionShow{SessionID: "s1"})) {
		t.Fatalf("no SessionShow for the first session: %v", b.Sent())
	}
	for _, e := range []key.Event{press("S", key.ModCtrl|key.ModShift), press("J", 0), press(key.NameReturn, 0)} {
		r.Queue(e)
		frame()
	}
	frame()
	if u.nav.session != "s2" || u.sw.open {
		t.Fatalf("at %s, switcher open %v", u.nav.session, u.sw.open)
	}
	for _, m := range b.Sent() {
		if in, ok := m.(proto.Input); ok {
			t.Fatalf("the pane got %q", in.Data)
		}
	}
	if !slices.Contains(b.Sent(), any(proto.SessionShow{SessionID: "s2"})) || u.windowTitle() != "calm-heron · pitwall" {
		t.Fatalf("title %q, sent %v", u.windowTitle(), b.Sent())
	}
	// Ctrl+Shift+] arrives named by the symbol it types.
	r.Queue(press("}", key.ModCtrl|key.ModShift))
	frame()
	if u.nav.session != "s3" {
		t.Fatalf("Ctrl+Shift+] went to %s", u.nav.session)
	}
}
