package daemon

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

const askSpec = `<form title="Deploy?"><choice id="env"><option>prod</option></choice></form>`

// formOf is form id in s, or nil.
func formOf(s model.State, id string) *model.Form {
	if i := slices.IndexFunc(s.Forms, func(f model.Form) bool { return f.ID == id }); i >= 0 {
		return &s.Forms[i]
	}
	return nil
}

// activityOf is pane's activity in s, or nil.
func activityOf(s model.State, pane string) *model.Activity {
	if i := slices.IndexFunc(s.Activities, func(a model.Activity) bool { return a.PaneID == pane }); i >= 0 {
		return &s.Activities[i]
	}
	return nil
}

// A form shows in every window over its pane's tab, makes the pane's
// activity a question, wakes a snoozed tab, and its first answer goes to
// the connection that opened it; then the question is gone.
func TestFormAnswer(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	sock, stop := run(t, f)
	defer stop()
	gui := dial(t, sock, "gui")
	st := gui.waitState("first session", func(s model.State) bool { return len(s.Panes) == 1 })
	pane, ws := st.Panes[0].ID, st.Panes[0].WorkspaceID
	gui.send(proto.SetTabInbox{WorkspaceID: ws, SnoozedUntil: time.Now().Add(time.Hour)})
	gui.waitState("snoozed", func(s model.State) bool { return !s.Workspaces[0].SnoozedUntil.IsZero() })

	cli := dial(t, sock, "cli")
	cli.send(proto.OpenForm{ID: "q", Pane: pane, Spec: askSpec})
	st = gui.waitState("form", func(s model.State) bool { return formOf(s, "q") != nil })
	fm := formOf(st, "q")
	if fm.WorkspaceID != ws || fm.Title != "Deploy?" || !fm.Waits || fm.Spec != askSpec {
		t.Fatalf("form %+v", fm)
	}
	a := activityOf(st, pane)
	if a == nil || a.State != model.StateAwaitingInput || a.Detail != "Deploy?" || !a.Unseen {
		t.Fatalf("a waiting form should be the pane's question: %+v", a)
	}
	if !st.Workspaces[0].SnoozedUntil.IsZero() {
		t.Fatal("a form that asks should wake its snoozed tab")
	}

	other := dial(t, sock, "gui")
	other.waitState("second window sees it", func(s model.State) bool { return formOf(s, "q") != nil })
	gui.send(proto.FormAnswer{ID: "q", Answers: `{"button":"submit","env":"prod"}`})
	got := cli.waitFor("answer", func(m any) bool { _, ok := m.(proto.FormAnswer); return ok }).(proto.FormAnswer)
	if got.Answers != `{"button":"submit","env":"prod"}` || got.Cancel {
		t.Fatalf("answer %+v", got)
	}
	st = gui.waitState("form gone", func(s model.State) bool { return formOf(s, "q") == nil })
	if a := activityOf(st, pane); a != nil && a.State == model.StateAwaitingInput {
		t.Fatalf("the question outlived the form: %+v", a)
	}
	other.send(proto.FormAnswer{ID: "q", Cancel: true})
	waitErr(t, other, "no form q is open")
}

// A cancel reaches the owner as Cancel, and an owner that disconnects
// takes its forms with it.
func TestFormCancelAndDrop(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	sock, stop := run(t, f)
	defer stop()
	gui := dial(t, sock, "gui")
	first := gui.waitState("first session", func(s model.State) bool { return len(s.Panes) == 1 }).Panes[0]
	pane := first.ID

	cli := dial(t, sock, "cli")
	cli.send(proto.OpenForm{ID: "c", Pane: pane, Spec: askSpec})
	gui.waitState("form", func(s model.State) bool { return formOf(s, "c") != nil })
	gui.send(proto.FormAnswer{ID: "c", Cancel: true})
	if got := cli.waitFor("cancel", func(m any) bool { _, ok := m.(proto.FormAnswer); return ok }).(proto.FormAnswer); !got.Cancel {
		t.Fatalf("answer %+v", got)
	}

	gone := dial(t, sock, "cli")
	gone.send(proto.OpenForm{ID: "d", Pane: pane, Spec: askSpec})
	gui.waitState("second form", func(s model.State) bool { return formOf(s, "d") != nil })
	gone.conn.Close()
	gui.waitState("owner gone, form gone", func(s model.State) bool { return formOf(s, "d") == nil })

	// A form whose pane closes ends as Closed: nobody can answer it.
	gui.send(proto.OpenPane{WorkspaceID: first.WorkspaceID})
	other := gui.waitState("second pane", func(s model.State) bool { return len(s.Panes) == 2 }).Panes[1].ID
	orphan := dial(t, sock, "cli")
	orphan.send(proto.OpenForm{ID: "e", Pane: other, Spec: askSpec})
	gui.waitState("form on the second pane", func(s model.State) bool { return formOf(s, "e") != nil })
	gui.send(proto.ClosePane{Pane: other})
	if got := orphan.waitFor("closed with its pane", func(m any) bool { _, ok := m.(proto.FormAnswer); return ok }).(proto.FormAnswer); !got.Closed {
		t.Fatalf("answer %+v", got)
	}
	gui.waitState("orphan gone", func(s model.State) bool { return formOf(s, "e") == nil })
}

// A dashboard waits on nobody; a script updates it by id from another
// connection, and closes it, which tells its owner Closed.
func TestFormDashboardUpdate(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	sock, stop := run(t, f)
	defer stop()
	gui := dial(t, sock, "gui")
	pane := gui.waitState("first session", func(s model.State) bool { return len(s.Panes) == 1 }).Panes[0].ID

	owner := dial(t, sock, "cli")
	owner.send(proto.OpenForm{ID: "build", Pane: pane, Spec: `<form title="Build"><progress label="Tests" value="10"/></form>`})
	st := gui.waitState("dashboard", func(s model.State) bool { return formOf(s, "build") != nil })
	if fm := formOf(st, "build"); fm.Waits {
		t.Fatalf("a dashboard should not wait: %+v", fm)
	}
	if a := activityOf(st, pane); a != nil && a.State == model.StateAwaitingInput {
		t.Fatalf("a dashboard should not ask: %+v", a)
	}

	script := dial(t, sock, "cli")
	next := `<form title="Build"><progress label="Tests" value="90"/></form>`
	script.send(proto.OpenForm{ID: "build", Spec: next, Update: true})
	gui.waitState("updated", func(s model.State) bool { fm := formOf(s, "build"); return fm != nil && fm.Spec == next })

	for _, c := range []struct {
		m    proto.OpenForm
		want string
	}{
		{proto.OpenForm{ID: "build", Pane: pane, Spec: next}, "open already"},
		{proto.OpenForm{ID: "nope", Spec: next, Update: true}, "no form nope is open"},
		{proto.OpenForm{ID: "bad", Pane: pane, Spec: "<form><frob/></form>"}, "form bad: line 1: unknown element <frob>"},
		{proto.OpenForm{ID: "lost", Pane: "no-such-pane", Spec: next}, "no pane no-such-pane"},
	} {
		script.send(c.m)
		waitErr(t, script, c.want)
	}

	script.send(proto.FormAnswer{ID: "build", Closed: true})
	if got := owner.waitFor("closed", func(m any) bool { _, ok := m.(proto.FormAnswer); return ok }).(proto.FormAnswer); !got.Closed {
		t.Fatalf("answer %+v", got)
	}
	gui.waitState("closed", func(s model.State) bool { return formOf(s, "build") == nil })
}

// waitErr reads c's messages until a daemon error that contains want.
func waitErr(t *testing.T, c *testClient, want string) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case m, open := <-c.in:
			if !open {
				t.Fatalf("connection closed waiting for error %q", want)
			}
			if e, ok := m.(proto.Error); ok {
				if !strings.Contains(e.Message, want) {
					t.Fatalf("error %q, want %q", e.Message, want)
				}
				return
			}
		case <-timeout:
			t.Fatalf("timed out waiting for error %q", want)
		}
	}
}

func TestFormWakesWorkerParent(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d, err := NewWith(f.options())
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	d.st.Workspaces = []model.Workspace{
		{ID: "parent", Settled: old, SnoozedAt: old, SnoozedUntil: time.Now().Add(time.Hour)},
		{ID: "worker", Parent: "parent"},
	}
	d.st.Panes = []model.Pane{{ID: "p", WorkspaceID: "worker"}}
	if err := d.openForm(&client{}, proto.OpenForm{ID: "q", Pane: "p", Spec: askSpec}); err != nil {
		t.Fatal(err)
	}
	if w := d.st.Workspace("parent"); !w.Settled.IsZero() || !w.SnoozedUntil.IsZero() {
		t.Fatalf("worker form left parent settled or snoozed: %+v", w)
	}
}
