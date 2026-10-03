package daemon

import (
	"context"
	"slices"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// Sessions are made with a generated or chosen unique name, renamed, keep
// their own tabs, groups and order, end with their last tab, and are killed
// with all their processes.
func TestSessionLifecycle(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	ctx := context.Background()
	dir := t.TempDir()

	must(t, d.handle(ctx, proto.SessionNew{Cwd: dir}))
	must(t, d.handle(ctx, proto.SessionNew{Name: "work", Cwd: dir}))
	st := d.state()
	if len(st.Sessions) != 2 || !model.IsSessionName(st.Sessions[0].Name) || st.Sessions[1].Name != "work" {
		t.Fatalf("sessions %+v", st.Sessions)
	}
	gen, work := st.Sessions[0].ID, st.Sessions[1].ID
	if d.handle(ctx, proto.SessionNew{Name: "work", Cwd: dir}) == nil || d.handle(ctx, proto.SessionRename{SessionID: gen, Name: "work"}) == nil {
		t.Fatal("two sessions named work")
	}
	if d.handle(ctx, proto.SessionRename{SessionID: gen, Name: "  "}) == nil {
		t.Fatal("renamed to nothing")
	}
	must(t, d.handle(ctx, proto.SessionRename{SessionID: gen, Name: "play"}))

	// Tabs go to the session asked for; each session has its own order and
	// its own tab names.
	must(t, d.handle(ctx, proto.NewSession{Cwd: dir, SessionID: work, Name: "api"}))
	must(t, d.handle(ctx, proto.NewSession{Cwd: dir, SessionID: gen, Name: "api"}))
	st = d.state()
	tabs := func(s model.State, session string) []string {
		var out []string
		for _, w := range s.Ordered(session) {
			out = append(out, w.ID)
		}
		return out
	}
	if len(tabs(st, work)) != 2 || len(tabs(st, gen)) != 2 {
		t.Fatalf("tabs: work %v, play %v", tabs(st, work), tabs(st, gen))
	}
	if d.handle(ctx, proto.NewSession{Cwd: dir, SessionID: work, Name: "api"}) == nil {
		t.Fatal("two tabs named api in one session")
	}
	w1, w2 := tabs(st, work)[0], tabs(st, work)[1]
	must(t, d.handle(ctx, proto.MoveSession{WorkspaceID: w2, Before: w1}))
	st = d.state()
	if got := st.Session(work).Order; !slices.Equal(got, []string{w2, w1}) || !slices.Equal(st.Session(gen).Order, tabs(st, gen)) {
		t.Fatalf("orders: work %v, play %v", got, st.Session(gen).Order)
	}
	if d.handle(ctx, proto.MoveSession{WorkspaceID: w1, Before: tabs(st, gen)[0]}) == nil {
		t.Fatal("placed before another session's tab")
	}
	must(t, d.handle(ctx, proto.NewGroup{Name: "g", WorkspaceIDs: []string{w1}}))
	g := d.state().Projects[0]
	if g.SessionID != work {
		t.Fatalf("group in %s, want work", g.SessionID)
	}
	if d.handle(ctx, proto.SetSessionGroup{WorkspaceID: tabs(st, gen)[0], GroupID: g.ID}) == nil ||
		d.handle(ctx, proto.NewGroup{WorkspaceIDs: []string{w2, tabs(st, gen)[0]}}) == nil {
		t.Fatal("grouped tabs of two sessions")
	}

	// Closing the last tab ends a session, with its groups.
	for _, id := range tabs(d.state(), work) {
		must(t, d.handle(ctx, proto.CloseTab{WorkspaceID: id}))
	}
	if st := d.state(); st.Session(work) != nil || len(st.Projects) != 0 || len(st.Sessions) != 1 {
		t.Fatalf("work outlived its tabs: %+v %+v", st.Sessions, st.Projects)
	}

	// Kill closes every pane of the session.
	n := len(f.panes)
	must(t, d.handle(ctx, proto.SessionKill{SessionID: gen}))
	if st := d.state(); len(st.Sessions) != 0 || len(st.Workspaces) != 0 || len(st.Panes) != 0 {
		t.Fatalf("kill left %+v", st)
	}
	for i := range n {
		waitUntil(t, "pane closed", func() bool { return f.closed(i) })
	}

	// A tab with no session to go to makes one.
	must(t, d.handle(ctx, proto.NewSession{Cwd: dir}))
	if st := d.state(); len(st.Sessions) != 1 || st.Workspaces[0].SessionID != st.Sessions[0].ID {
		t.Fatalf("lone tab: %+v", st)
	}
}

// Each GUI window counts on the session it shows, attach requests reach only
// the windows showing the tab's session, and a window that goes stops
// counting.
func TestSessionWindows(t *testing.T) {
	sock, stop := run(t, &fakes{statsCalls: map[string]int{}})
	defer stop()
	a := dial(t, sock, "gui")
	st := a.waitState("first session", func(s model.State) bool { return len(s.Sessions) == 1 })
	first := st.Sessions[0]
	a.send(proto.SessionShow{SessionID: first.ID})
	a.waitState("a counted", func(s model.State) bool { return s.Session(first.ID).Windows == 1 })

	// A second window asks for a session by name, which is made for it.
	b := dialHello(t, sock, proto.Hello{Version: proto.Version, Kind: "gui", Cwd: t.TempDir(), Session: "docs"})
	st = b.waitState("docs made", func(s model.State) bool { return s.SessionNamed("docs") != nil })
	docs := st.SessionNamed("docs")
	if len(st.Ordered(docs.ID)) != 1 {
		t.Fatalf("docs has no shell: %+v", st)
	}
	b.send(proto.SessionShow{SessionID: docs.ID})
	b.waitState("b counted", func(s model.State) bool {
		return s.Session(docs.ID).Windows == 1 && s.Session(first.ID).Windows == 1 && s.Recent().ID == docs.ID
	})

	tab := st.Ordered(docs.ID)[0]
	dial(t, sock, "cli").send(proto.FocusSession{WorkspaceID: tab.ID})
	b.waitFor("focus in b", func(m any) bool {
		f, ok := m.(proto.FocusSession)
		return ok && f.WorkspaceID == tab.ID && f.SessionID == docs.ID
	})
	dial(t, sock, "cli").send(proto.FocusSession{SessionID: first.ID})
	a.waitFor("raise in a", func(m any) bool { f, ok := m.(proto.FocusSession); return ok && f.SessionID == first.ID })
	for len(b.in) > 0 {
		if f, ok := (<-b.in).(proto.FocusSession); ok && f.SessionID == first.ID {
			t.Fatal("b got a's raise")
		}
	}

	// Both windows on one session; then one closes.
	b.send(proto.SessionShow{SessionID: first.ID})
	a.waitState("two on first", func(s model.State) bool { return s.Session(first.ID).Windows == 2 && s.Session(docs.ID).Windows == 0 })
	b.conn.Close()
	a.waitState("b gone", func(s model.State) bool { return s.Session(first.ID).Windows == 1 })

	// A window on a session whose tabs are all detached gets a shell.
	a.send(proto.DetachSession{WorkspaceID: st.Ordered(first.ID)[0].ID, Detached: true})
	a.waitState("detached", func(s model.State) bool {
		return len(slices.DeleteFunc(s.Ordered(first.ID), func(w model.Workspace) bool { return w.Detached })) == 0
	})
	c := dialHello(t, sock, proto.Hello{Version: proto.Version, Kind: "gui", Cwd: t.TempDir(), Session: first.Name})
	c.waitState("shell for the window", func(s model.State) bool {
		return len(slices.DeleteFunc(s.Ordered(first.ID), func(w model.Workspace) bool { return w.Detached })) == 1
	})
}
