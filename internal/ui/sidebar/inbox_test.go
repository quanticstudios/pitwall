package sidebar

import (
	"slices"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// A row shows approval over input over working over a failure over
// ready, whatever order its agents' activities come in.
func TestStatusOf(t *testing.T) {
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	act := func(s model.AgentState) model.Activity { return model.Activity{State: s, UpdatedAt: now} }
	limited := model.Activity{State: model.StateError, Resumes: now.Add(time.Hour)}
	for _, tc := range []struct {
		acts []model.Activity
		want status
	}{
		{nil, statusReady},
		{[]model.Activity{act(model.StateCompleted)}, statusReady},
		{[]model.Activity{act(model.StateError), act(model.StateCompleted)}, statusFailed},
		{[]model.Activity{act(model.StateError), limited}, statusLimited},
		{[]model.Activity{act(model.StateError), act(model.StateWorking)}, statusWorking},
		{[]model.Activity{act(model.StateWorking), act(model.StatePlanReady)}, statusInput},
		{[]model.Activity{act(model.StateAwaitingInput), act(model.StatePendingApproval), act(model.StateError)}, statusApproval},
		{[]model.Activity{{State: model.StateError, Detail: "You've hit your session limit · resets 3pm (UTC)"}}, statusLimited},
	} {
		got, a := statusOf(tc.acts, now)
		if got != tc.want || len(tc.acts) > 0 && (a == nil || statusOne(*a, now) != got && got != statusReady) {
			t.Errorf("%v: %v (%+v), want %v", tc.acts, got, a, tc.want)
		}
	}
}

// Snoozed beats settled, settled beats pinned, pinned beats working, and
// a tab without an agent is a shell whatever its flags.
func TestSectionOf(t *testing.T) {
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	later, earlier := now.Add(time.Hour), now.Add(-time.Hour)
	for _, tc := range []struct {
		name  string
		w     model.Workspace
		agent bool
		st    status
		shelf bool
		want  section
	}{
		{"plain", model.Workspace{}, true, statusReady, true, secActive},
		{"pinned", model.Workspace{Pinned: true}, true, statusReady, true, secPinned},
		{"settled and pinned", model.Workspace{Pinned: true, Settled: earlier}, true, statusReady, true, secSettled},
		{"working and settled", model.Workspace{Settled: earlier}, true, statusWorking, true, secSettled},
		{"working and pinned", model.Workspace{Pinned: true}, true, statusWorking, true, secPinned},
		{"working", model.Workspace{}, true, statusWorking, true, secWorking},
		{"working without the shelf", model.Workspace{}, true, statusWorking, false, secActive},
		{"snoozed and working", model.Workspace{SnoozedUntil: later}, true, statusWorking, true, secSnoozed},
		{"snooze over", model.Workspace{SnoozedUntil: earlier}, true, statusReady, true, secActive},
		{"a shell", model.Workspace{Pinned: true, SnoozedUntil: later}, false, statusReady, true, secShells},
	} {
		if got := sectionOf(tc.w, tc.agent, tc.st, now, tc.shelf); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, sectionTitle[got], sectionTitle[tc.want])
		}
	}
}

func TestDropVerb(t *testing.T) {
	for _, tc := range []struct {
		from, to section
		want     verb
	}{
		{secActive, secPinned, verbPin},
		{secSettled, secPinned, verbPin},
		{secPinned, secActive, verbUnpin},
		{secActive, secSettled, verbSettle},
		{secWorking, secSettled, verbSettle},
		{secSettled, secActive, verbUnsettle},
		{secSnoozed, secActive, verbWake},
		{secActive, secActive, verbNone},
		{secWorking, secActive, verbNone},
		{secActive, secWorking, verbNone},
		{secActive, secSnoozed, verbNone},
		{secShells, secPinned, verbNone},
		{secActive, secShells, verbNone},
	} {
		if got := dropVerb(tc.from, tc.to); got != tc.want {
			t.Errorf("%s to %s: %q, want %q", sectionTitle[tc.from], sectionTitle[tc.to], verbText[got], verbText[tc.want])
		}
	}
}

func TestRecedes(t *testing.T) {
	for _, tc := range []struct {
		st                       status
		unseen, active, selected bool
		want                     bool
	}{
		{statusWorking, false, false, false, true},
		{statusReady, false, false, false, true},
		{statusReady, true, false, false, false},
		{statusApproval, false, false, false, false},
		{statusInput, false, false, false, false},
		{statusFailed, false, false, false, false},
		{statusLimited, false, false, false, false},
		{statusWorking, false, true, false, false},
		{statusReady, false, false, true, false},
	} {
		if got := recedes(tc.st, tc.unseen, tc.active, tc.selected); got != tc.want {
			t.Errorf("%+v: %v", tc, got)
		}
	}
}

// Snooze offers in an hour, this evening while it is ahead, tomorrow
// morning and Monday, without Monday on a Sunday, soonest first.
func TestSnoozePresets(t *testing.T) {
	loc := time.FixedZone("x", 3600)
	labels := func(now time.Time) []string {
		var out []string
		for _, p := range snoozePresets(now) {
			out = append(out, p.label+" "+p.when)
		}
		return out
	}
	for _, tc := range []struct {
		now  time.Time
		want []string
	}{
		{time.Date(2026, 11, 4, 10, 30, 0, 0, loc), // a Wednesday morning
			[]string{"In an hour 11:30", "This evening 18:00", "Tomorrow morning 09:00", "Monday 09:00"}},
		{time.Date(2026, 11, 4, 19, 0, 0, 0, loc), // past the evening
			[]string{"In an hour 20:00", "Tomorrow morning 09:00", "Monday 09:00"}},
		{time.Date(2026, 11, 8, 17, 15, 0, 0, loc), // a Sunday: Monday is tomorrow
			[]string{"This evening 18:00", "In an hour 18:15", "Tomorrow morning 09:00"}},
		{time.Date(2026, 11, 9, 8, 0, 0, 0, loc), // a Monday: next week's
			[]string{"In an hour 09:00", "This evening 18:00", "Tomorrow morning 09:00", "Monday 09:00"}},
	} {
		if got := labels(tc.now); !slices.Equal(got, tc.want) {
			t.Errorf("%v: %q, want %q", tc.now, got, tc.want)
		}
	}
	if p := snoozePresets(time.Date(2026, 11, 9, 8, 0, 0, 0, loc)); !p[3].at.Equal(time.Date(2026, 11, 16, 9, 0, 0, 0, loc)) {
		t.Errorf("Monday from a Monday: %v", p[3].at)
	}
	if p := snoozePresets(time.Date(2026, 11, 4, 23, 30, 0, 0, loc)); p[0].when != "Thu 00:30" {
		t.Errorf("in an hour past midnight: %q", p[0].when)
	}
}

// inboxState is a session of agent tabs: g1 holds a, b and the worker k
// of b; u is ungrouped; s is a shell.
func inboxState(now time.Time) model.State {
	act := func(ws string, s model.AgentState, at time.Time) model.Activity {
		return model.Activity{PaneID: "p" + ws, WorkspaceID: ws, Provider: model.ProviderClaude, State: s, UpdatedAt: at}
	}
	return model.State{
		Projects: []model.Project{{ID: "g1", Name: "one"}},
		Workspaces: []model.Workspace{
			{ID: "u", Name: "u"},
			{ID: "a", Name: "a", ProjectID: "g1"},
			{ID: "b", Name: "b", ProjectID: "g1"},
			{ID: "k", Name: "k", ProjectID: "g1", Parent: "b"},
			{ID: "s", Name: "s"},
			{ID: "d", Name: "d", Detached: true},
		},
		Activities: []model.Activity{
			act("u", model.StateCompleted, now),
			act("a", model.StateWorking, now),
			act("b", model.StateCompleted, now),
			act("k", model.StatePendingApproval, now),
			act("d", model.StateCompleted, now),
		},
	}
}

// The inbox lists every live tab once in sections, a worker under its
// parent's row rather than as one of its own; Active puts what needs the
// user first, the parent of an asking worker among it; the shell sits
// last on its own.
func TestInboxOrder(t *testing.T) {
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	st := inboxState(now)
	st.Workspace("u").Settled = now
	s := Sidebar{Inbox: true, WorkingShelf: true}
	v := s.viewOf(layout.Context{Now: now}, theme.Dark(), &st, "", "")
	if got, want := s.order(v), []string{"b", "a", "u", "s"}; !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	for id, want := range map[string]section{"b": secActive, "a": secWorking, "u": secSettled, "s": secShells} {
		if v.secOf[id] != want {
			t.Errorf("%s in %s, want %s", id, sectionTitle[v.secOf[id]], sectionTitle[want])
		}
	}
	if _, ok := v.secOf["k"]; ok {
		t.Error("the worker k has a row of its own")
	}
	if v.need["b"] != statusApproval || v.activity["b"].WorkspaceID != "k" {
		t.Errorf("b, done over its asking worker, needs %d with %+v", v.need["b"], v.activity["b"])
	}
	if subs := s.shownSubs(v, "b"); len(subs) != 1 || subs[0].id() != "k" {
		t.Errorf("b's sub-rows %v, want its worker k", subs)
	}
	st.Activities[3].State = model.StateWorking // k works, b is done
	v = s.viewOf(layout.Context{Now: now}, theme.Dark(), &st, "", "")
	if v.secOf["b"] != secWorking {
		t.Errorf("b, done over its working worker, in %s, want Working", sectionTitle[v.secOf["b"]])
	}
	st.Activities[3].State = model.StatePendingApproval
	s.WorkingShelf = false
	v = s.viewOf(layout.Context{Now: now}, theme.Dark(), &st, "", "")
	if got, want := s.order(v), []string{"b", "a", "u", "s"}; !slices.Equal(got, want) {
		t.Fatalf("without the shelf: %v, want %v", got, want)
	}
	if rows := s.Rows(&st, "", ""); !slices.Equal(rows, s.order(v)) {
		t.Errorf("Rows %v follows the tree", rows)
	}
}

// Moving tabs sends the events that land them, and its undo sends the
// ones that put them back.
func TestReshelveUndo(t *testing.T) {
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	st := inboxState(now)
	st.Workspace("a").Pinned, st.Workspace("a").Color = true, "teal"
	s := Sidebar{Inbox: true, Shelves: true, now: now}
	v := s.viewOf(layout.Context{Now: now}, theme.Dark(), &st, "", "")
	s.reshelve(v, []string{"a"}, "Settled", func(_ string, sh shelf) shelf { return sh.apply(verbSettle) })
	if want := []Event{SetInbox{WorkspaceID: "a", Settled: true}}; !slices.Equal(s.events, want) {
		t.Fatalf("settle sent %v", s.events)
	}
	if s.undo == nil || s.undo.text != "Settled a" || !slices.Equal(s.undo.events, []Event{SetInbox{WorkspaceID: "a"}}) {
		t.Fatalf("undo %+v", s.undo)
	}
	s.events, s.undo = nil, nil
	wake := now.Add(time.Hour)
	st.Workspace("b").SnoozedUntil = wake
	s.reshelve(v, []string{"a", "b"}, "Moved", func(_ string, sh shelf) shelf { return sh.apply(verbWake) })
	want := []Event{SetTabOptions{WorkspaceID: "a", Color: "teal"}, SetInbox{WorkspaceID: "b"}}
	if !slices.Equal(s.events, want) {
		t.Fatalf("to Active sent %v, want %v", s.events, want)
	}
	back := []Event{SetTabOptions{WorkspaceID: "a", Pinned: true, Color: "teal"}, SetInbox{WorkspaceID: "b", SnoozedUntil: wake},
		MoveSession{WorkspaceID: "a", GroupID: "g1", Before: "b"}}
	if s.undo.text != "Moved 2 tabs" || !slices.Equal(s.undo.events, back) {
		t.Fatalf("undo %+v, want %v", s.undo, back)
	}
}

// Dragging a row into Settled sends SetInbox; into Working it does
// nothing; the inbox's header offers the toggle back to the tree.
func TestInboxDrag(t *testing.T) {
	h := &dragHarness{t: t, now: time.Unix(1e9, 0)}
	h.st = inboxState(h.now)
	h.st.Workspace("u").Settled = h.now
	h.s.Inbox, h.s.WorkingShelf, h.s.Shelves = true, true, true
	h.frame()
	h.frame()
	var heads []string
	for _, e := range h.s.elems {
		if e.kind == 'h' {
			heads = append(heads, sectionTitle[headerSection(e)])
		}
	}
	if want := []string{"Active", "Working", "Settled", "Shells"}; !slices.Equal(heads, want) {
		t.Fatalf("sections %v, want %v", heads, want)
	}
	p := h.at("b", 20)
	h.pointer(pointer.Press, p)
	h.pointer(pointer.Drag, p.Add(f32.Pt(0, 10)))
	h.frame()
	heads = heads[:0]
	for _, e := range h.s.elems {
		if e.kind == 'h' {
			heads = append(heads, sectionTitle[headerSection(e)])
		}
	}
	if want := []string{"Pinned", "Active", "Working", "Settled", "Shells"}; !slices.Equal(heads, want) {
		t.Fatalf("while dragging %v, want the empty Pinned too", heads)
	}
	// The rows below b close up under the pointer as it lifts.
	row := f32.Pt(0, float32(rowHeight(layout.Context{Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}, theme.Dark())+2))
	h.pointer(pointer.Drag, h.at("a", 10).Sub(row))
	if d := h.s.drag.target; d.sec != secWorking {
		t.Fatalf("over a: %+v", d)
	}
	h.pointer(pointer.Drag, h.at("u", 10).Sub(row))
	if d := h.s.drag.target; d.sec != secSettled {
		t.Fatalf("over u: %+v", d)
	}
	evs := h.pointer(pointer.Release, h.at("u", 10).Sub(row))
	if !slices.Equal(evs, []Event{SetInbox{WorkspaceID: "b", Settled: true}}) {
		t.Fatalf("drop sent %v", evs)
	}
	if h.s.undo == nil {
		t.Fatal("no undo notice")
	}
	h.s.undoBtn.Click()
	if evs := h.frame(); !slices.Equal(evs, []Event{SetInbox{WorkspaceID: "b"}}) {
		t.Fatalf("undo sent %v", evs)
	}
	h.now = h.now.Add(time.Second)
	h.frame()

	p = h.at("u", 20) // below a, which stays put
	h.pointer(pointer.Press, p)
	h.pointer(pointer.Drag, p.Add(f32.Pt(0, -10)))
	h.pointer(pointer.Drag, h.at("a", 10))
	if evs := h.pointer(pointer.Release, h.at("a", 10)); len(evs) != 0 {
		t.Fatalf("a drop into Working sent %v", evs)
	}
}

// Undoing a pin or an unpin puts the tab back where it was, in its group
// or at the top level, as the daemon moved it to the pinned ones.
func TestUndoKeepsOrder(t *testing.T) {
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	st := inboxState(now)
	s := Sidebar{Inbox: true, Shelves: true, now: now}
	v := s.viewOf(layout.Context{Now: now}, theme.Dark(), &st, "", "")
	for _, tc := range []struct {
		id   string
		want MoveSession
	}{
		{"b", MoveSession{WorkspaceID: "b", GroupID: "g1", Before: "k"}},
		{"s", MoveSession{WorkspaceID: "s", Before: "d"}}, // the detached tab keeps its place
		{"u", MoveSession{WorkspaceID: "u", Before: "s"}},
	} {
		s.events, s.undo = nil, nil
		s.reshelve(v, []string{tc.id}, "Pinned", func(_ string, sh shelf) shelf { return sh.apply(verbPin) })
		if got := s.undo.events; len(got) != 2 || got[1] != Event(tc.want) {
			t.Errorf("pin %s: undo %v, want it to end with %v", tc.id, got, tc.want)
		}
	}
	s.events, s.undo = nil, nil
	s.reshelve(v, []string{"k"}, "Pinned", func(_ string, sh shelf) shelf { return sh.apply(verbPin) })
	if len(s.undo.events) != 1 {
		t.Errorf("a worker, which pinning does not move: undo %v", s.undo.events)
	}
}

// Dropping a selection of a pinned and a settled tab into Active unpins
// one and unsettles the other, and one undo puts both back.
func TestDropMixedSelection(t *testing.T) {
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	st := inboxState(now)
	st.Workspace("u").Pinned = true
	st.Workspace("b").Settled = now
	s := Sidebar{Inbox: true, Shelves: true, now: now}
	v := s.viewOf(layout.Context{Now: now}, theme.Dark(), &st, "", "")
	s.drag.ids = []string{"u", "b"}
	if !s.dropInbox(v, drop{ok: true, into: true, sec: secActive}) {
		t.Fatal("nothing moved")
	}
	want := []Event{SetTabOptions{WorkspaceID: "u"}, SetInbox{WorkspaceID: "b"}}
	if !slices.Equal(s.events, want) {
		t.Fatalf("sent %v, want %v", s.events, want)
	}
	back := []Event{SetTabOptions{WorkspaceID: "u", Pinned: true}, SetInbox{WorkspaceID: "b", Settled: true}, MoveSession{WorkspaceID: "u", Before: "s"}}
	if s.undo.text != "Moved 2 tabs" || !slices.Equal(s.undo.events, back) {
		t.Fatalf("undo %+v, want %v", s.undo, back)
	}
}

// Switching to the inbox with queued tasks in two groups lays the queue
// out again for the inbox's rows instead of reusing the tree's indexes.
func TestInboxQueue(t *testing.T) {
	h := &dragHarness{t: t, now: time.Unix(1e9, 0)}
	h.st = inboxState(h.now)
	h.st.Projects = append(h.st.Projects, model.Project{ID: "g2", Name: "two"})
	h.st.Workspaces = append(h.st.Workspaces, model.Workspace{ID: "x1", ProjectID: "g2"}, model.Workspace{ID: "x2", ProjectID: "g2"})
	h.st.Tasks = []model.Task{{ID: "t1", GroupID: "g1", Cmd: []string{"codex", "one"}}, {ID: "t2", GroupID: "g2", Cmd: []string{"codex", "two"}}}
	h.frame()
	h.s.expanded["g1"], h.s.expanded["g2"] = true, true
	h.frame()
	h.s.Inbox = true
	h.frame()
	if len(h.s.queue) != 2 {
		t.Fatalf("%d queue blocks, want 2", len(h.s.queue))
	}
	for _, b := range h.s.queue {
		if b.after >= len(h.s.elems) || b.x != 0 {
			t.Errorf("block %+v with %d elems", b, len(h.s.elems))
		}
	}
}
