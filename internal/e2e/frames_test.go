package e2e_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// window is a GUI of this proto.Level showing session: it keeps each
// pane's frame as cmd/pitwall's backend does, applying FrameRows.
type window struct {
	*client
	frames map[string]proto.Frame
}

func openWindow(t *testing.T, session string) *window {
	t.Helper()
	c := connectHello(t, proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "gui", Cwd: t.TempDir(), Session: session})
	return &window{client: c, frames: map[string]proto.Frame{}}
}

// next waits for the next Frame or FrameRows of pane, failing on one of a
// pane in not, applies it and returns it.
func (w *window) next(t *testing.T, pane string, not ...string) any {
	t.Helper()
	return w.waitFor(t, timeout, func(msg any) bool { return w.apply(t, msg, pane, not) })
}

func (w *window) apply(t *testing.T, msg any, pane string, not []string) bool {
	t.Helper()
	id := ""
	switch m := msg.(type) {
	case proto.Frame:
		id = m.Pane
		w.frames[id] = m
	case proto.FrameRows:
		id = m.Pane
		f, ok := m.Apply(w.frames[id])
		if !ok {
			t.Fatalf("rows %v of %s do not fit its frame", m.Rows, id)
		}
		w.frames[id] = f
	}
	if slices.Contains(not, id) {
		t.Fatalf("got a frame of %s, which the window does not show", id)
	}
	return id != "" && id == pane
}

// until applies frames of pane until its screen contains text.
func (w *window) until(t *testing.T, pane, text string, not ...string) {
	t.Helper()
	w.waitFor(t, timeout, func(msg any) bool {
		return w.apply(t, msg, pane, not) && strings.Contains(gridText(w.frames[pane].Grid), text)
	})
}

// sessionPane is the first pane of the first tab of the session named name.
func sessionPane(s model.State, name string) string {
	ss := s.SessionNamed(name)
	if ss == nil {
		return ""
	}
	for _, p := range s.Panes {
		if i := slices.IndexFunc(s.Workspaces, func(w model.Workspace) bool { return w.ID == p.WorkspaceID }); i >= 0 && s.Workspaces[i].SessionID == ss.ID {
			return p.ID
		}
	}
	return ""
}

// echoer prints "<name> ready", then "<name> got <line>" for each line it
// reads, and is idle in between: a pane changes only when the test types.
func echoer(name string) []string {
	return []string{"sh", "-c", fmt.Sprintf(`echo %s ready; while read l; do echo %s got $l; done`, name, name)}
}

// TestTabSwitch shows one tab, then switches to another whose program
// printed while hidden and is idle now: the window gets no frames of the
// tab it does not show, the first one of the tab it switches to is whole
// and current with no output to follow it, and the rows after keep it equal
// to a whole frame. A window of Level 13 watches every pane, to know when
// the hidden one's output has landed.
func TestTabSwitch(t *testing.T) {
	isolate(t)
	startDaemon(t)
	w := openWindow(t, "e2e")
	s := waitState(t, w.client, func(s model.State) bool { return sessionPane(s, "e2e") != "" })
	sid := s.SessionNamed("e2e").ID
	a, b := echoer("tab-a"), echoer("tab-b")
	w.send(t, proto.NewSession{SessionID: sid, Cmd: a})
	w.send(t, proto.NewSession{SessionID: sid, Cmd: b})
	s = waitState(t, w.client, func(s model.State) bool { return len(s.Panes) == 3 })
	pane := func(cmd []string) string {
		return s.Panes[slices.IndexFunc(s.Panes, func(p model.Pane) bool { return slices.Equal(p.Cmd, cmd) })].ID
	}
	pa, pb := pane(a), pane(b)
	all := connectHello(t, proto.Hello{Version: proto.Version, Level: proto.Since(proto.View{}) - 1, Kind: "gui", Cwd: t.TempDir(), Session: "e2e"})

	w.send(t, proto.View{Panes: []string{pa}})
	w.until(t, pa, "tab-a ready", pb)
	w.send(t, proto.Input{Pane: pb, Data: []byte("hidden\r")})
	all.waitFor(t, timeout, frameContains(pb, "tab-b got hidden"))
	w.send(t, proto.View{Panes: []string{pb}})
	if f, ok := w.next(t, pb).(proto.Frame); !ok || !strings.Contains(gridText(f.Grid), "tab-b got hidden") {
		t.Fatalf("first frame of the tab switched to: %#v", f)
	}
	w.send(t, proto.Input{Pane: pb, Data: []byte("shown\r")})
	w.until(t, pb, "tab-b got shown", pa)

	// Shown again, the pane comes whole: it equals what rows built.
	built := gridText(w.frames[pb].Grid)
	w.send(t, proto.View{})
	w.send(t, proto.View{Panes: []string{pb}})
	if f, ok := w.next(t, pb).(proto.Frame); !ok || gridText(f.Grid) != built {
		t.Fatalf("whole frame:\n%s\nrows built:\n%s", gridText(f.Grid), built)
	}
}

// TestTwoWindows has two windows on two sessions: each gets its own pane's
// frames alone, and still sees the other session change, as the session
// switcher's live preview needs.
func TestTwoWindows(t *testing.T) {
	isolate(t)
	startDaemon(t)
	one, two := openWindow(t, "one"), openWindow(t, "two")
	s := waitState(t, two.client, func(s model.State) bool { return sessionPane(s, "one") != "" && sessionPane(s, "two") != "" })
	p1, p2 := sessionPane(s, "one"), sessionPane(s, "two")
	one.send(t, proto.View{Panes: []string{p1}})
	two.send(t, proto.View{Panes: []string{p2}})
	one.send(t, proto.Input{Pane: p1, Data: []byte("echo mark-one\r")})
	two.send(t, proto.Input{Pane: p2, Data: []byte("printf '\\033]0;title-two\\007'; echo mark-two\r")})
	two.until(t, p2, "\nmark-two\n", p1)
	marked, titled := false, false
	one.waitFor(t, timeout, func(msg any) bool {
		if s, ok := msg.(proto.StateMsg); ok {
			i := slices.IndexFunc(s.State.Panes, func(p model.Pane) bool { return p.ID == p2 })
			titled = titled || i >= 0 && s.State.Panes[i].Title == "title-two"
		}
		if one.apply(t, msg, p1, []string{p2}) {
			marked = marked || strings.Contains(gridText(one.frames[p1].Grid), "\nmark-one\n")
		}
		return marked && titled
	})
}

// TestSearchAndCopyPastScreen searches a shown pane's history, scrolls to
// its top, which comes as a whole frame, and copies lines that run past
// the screen.
func TestSearchAndCopyPastScreen(t *testing.T) {
	isolate(t)
	startDaemon(t)
	w := openWindow(t, "e2e")
	s := waitState(t, w.client, func(s model.State) bool { return sessionPane(s, "e2e") != "" })
	sid := s.SessionNamed("e2e").ID
	cmd := []string{"sh", "-c", "i=0; while [ $i -lt 100 ]; do echo row$i; i=$((i+1)); done; echo done; exec sleep 30"}
	w.send(t, proto.NewSession{SessionID: sid, Cmd: cmd})
	s = waitState(t, w.client, func(s model.State) bool { return len(s.Panes) == 2 })
	p := s.Panes[slices.IndexFunc(s.Panes, func(p model.Pane) bool { return slices.Equal(p.Cmd, cmd) })].ID
	w.send(t, proto.View{Panes: []string{p}})
	w.until(t, p, "done")

	w.send(t, proto.Search{Pane: p, Query: "row7"})
	r := w.waitFor(t, timeout, func(msg any) bool { _, ok := msg.(proto.SearchResult); return ok }).(proto.SearchResult)
	if len(r.Matches) != 11 { // row7 and row70-row79
		t.Fatalf("row7: %d matches: %+v", len(r.Matches), r.Matches)
	}
	w.send(t, proto.Scroll{Pane: p, Lines: 1000})
	f, ok := w.next(t, p).(proto.Frame)
	if !ok || f.ScrollOffset == 0 || f.ScrollOffset != f.ScrollMax {
		t.Fatalf("scrolled to the top: %#v", f)
	}
	sel := vt.Selection{A: vt.Pos{Line: r.Matches[0].Line}, B: vt.Pos{Line: r.Matches[len(r.Matches)-1].Line, Col: 4}}
	w.send(t, proto.Text{Pane: p, Sel: sel})
	got := w.waitFor(t, timeout, func(msg any) bool { _, ok := msg.(proto.TextResult); return ok }).(proto.TextResult)
	if !strings.HasPrefix(got.Text, "row7\nrow8\n") || !strings.HasSuffix(got.Text, "row78\nrow79") {
		t.Fatalf("copied %q", got.Text)
	}
}

// TestResizeWholeFrame: a resize brings a shown pane a whole frame at the
// new size, and the rows after it fit that size (apply checks).
func TestResizeWholeFrame(t *testing.T) {
	isolate(t)
	startDaemon(t)
	w := openWindow(t, "e2e")
	s := waitState(t, w.client, func(s model.State) bool { return sessionPane(s, "e2e") != "" })
	p := sessionPane(s, "e2e")
	w.send(t, proto.View{Panes: []string{p}})
	w.send(t, proto.Input{Pane: p, Data: []byte("echo before\r")})
	w.until(t, p, "\nbefore\n")
	w.send(t, proto.Resize{Pane: p, Cols: 100, Rows: 30})
	w.waitFor(t, timeout, func(msg any) bool {
		f, ok := msg.(proto.Frame)
		return w.apply(t, msg, p, nil) && ok && f.Grid.Cols == 100 && f.Grid.Rows == 30
	})
	w.send(t, proto.Input{Pane: p, Data: []byte("echo after\r")})
	w.until(t, p, "\nafter\n")
}
