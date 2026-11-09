package app

import (
	"image"
	"slices"
	"strings"
	"testing"
	"time"

	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"github.com/quanticstudios/sector/view"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// linkedFake is the fake behind a connection whose Epoch and Level a test
// sets.
type linkedFake struct {
	*FakeBackend
	epoch, level int
}

func (l *linkedFake) Link() Link { return Link{Epoch: l.epoch, Level: l.level} }
func (l *linkedFake) Reconnect() {}
func (l *linkedFake) Restart()   {}

// watches is every MonitorWatch b was sent, oldest first.
func watches(b *FakeBackend) []proto.MonitorWatch {
	var out []proto.MonitorWatch
	for _, m := range b.Sent() {
		if w, ok := m.(proto.MonitorWatch); ok {
			out = append(out, w)
		}
	}
	return out
}

// A Monitor tab asks its daemon for samples while it is on screen, again on
// a new connection, and stops when it leaves the screen or closes. Its
// agents carry their tabs' names.
func TestMonitorTab(t *testing.T) {
	b := &linkedFake{FakeBackend: NewFakeBackend(), epoch: 1, level: proto.Level}
	b.Send(proto.NewSession{SessionID: "s1", Monitor: true})
	st := b.State()
	mon := st.Workspaces[len(st.Workspaces)-1]
	if !mon.Monitor {
		t.Fatalf("the fake opened %+v", mon)
	}
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: aide}}
	var ops op.Ops
	now := time.Now()
	frame := func() {
		ops.Reset()
		now = now.Add(time.Second)
		u.layout(gl.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(image.Pt(1600, 800)), Now: now})
	}
	frame()
	u.nav.workspace = mon.ID
	frame()
	if u.nav.workspace != mon.ID || u.mon.tabs[mon.ID] == nil {
		t.Fatalf("on %q with views %v; want the Monitor tab drawn", u.nav.workspace, u.mon.tabs)
	}
	want := []proto.MonitorWatch{{WorkspaceID: mon.ID, Interval: 2 * time.Second}}
	if got := watches(b.FakeBackend); !slices.Equal(got, want) {
		t.Fatalf("watches %v, want %v", got, want)
	}
	frame()
	b.epoch = 2 // a new connection has no watchers
	frame()
	want = append(want, want[0])
	if got := watches(b.FakeBackend); !slices.Equal(got, want) {
		t.Fatalf("after a reconnect: watches %v, want %v", got, want)
	}
	if l := monitorLabels(&st, u.mon.tabs[mon.ID].tabs); l["claude 101"] != "Add rate limiting to the public API" || len(l) != 1 {
		t.Fatalf("labels %v; want claude 101 named after its tab", l)
	}
	if fs, ok := monitorFocus(&st, u.mon.tabs[mon.ID].tabs, "claude 101"); !ok || fs != (proto.FocusSession{WorkspaceID: "w1b", SessionID: "s1", Pane: "i"}) {
		t.Fatalf("a click on claude 101 focuses %+v, %v; want its tab", fs, ok)
	}
	if _, ok := monitorFocus(&st, u.mon.tabs[mon.ID].tabs, "codex 202"); ok {
		t.Fatal("a click on an agent outside pitwall focuses a tab")
	}

	u.nav.workspace = "w1"
	frame()
	want = append(want, proto.MonitorWatch{WorkspaceID: mon.ID})
	if got := watches(b.FakeBackend); !slices.Equal(got, want) {
		t.Fatalf("after leaving it: watches %v, want %v", got, want)
	}

	u.nav.workspace = mon.ID
	frame()
	b.mu.Lock()
	b.st.Workspaces = slices.DeleteFunc(b.st.Workspaces, func(w model.Workspace) bool { return w.ID == mon.ID })
	b.mu.Unlock()
	frame()
	if got := watches(b.FakeBackend); got[len(got)-1].Interval != 0 || u.mon.tabs[mon.ID] != nil {
		t.Fatalf("after it closed: watches %v, views %v; want it stopped and dropped", got, u.mon.tabs)
	}
}

// A view goes stale when its daemon is down, or when no sample came for
// three intervals.
func TestMonitorStale(t *testing.T) {
	now := time.Now()
	tab := &monitorTab{v: view.New(nil), pushed: now.Add(-time.Second)}
	if stale(tab, true, now) {
		t.Error("stale a second after a sample")
	}
	if !stale(tab, false, now) {
		t.Error("not stale with its daemon down")
	}
	tab.pushed = now.Add(-staleAfter*tab.v.Interval() - time.Second)
	if !stale(tab, true, now) {
		t.Error("not stale after three intervals without a sample")
	}
}

// A daemon below MonitorTabLevel is not asked for a Monitor tab; the
// window says why.
func TestMonitorOlderDaemon(t *testing.T) {
	b := &linkedFake{FakeBackend: NewFakeBackend(), epoch: 1, level: proto.MonitorTabLevel - 1}
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: aide, session: "s1"}}
	u.openMonitor()
	if slices.ContainsFunc(b.Sent(), func(m any) bool { _, ok := m.(proto.NewSession); return ok }) {
		t.Fatal("asked an older daemon for a Monitor tab")
	}
	if !strings.Contains(u.sends.err, "older pitwall") {
		t.Fatalf("notice %q", u.sends.err)
	}
	b.level = proto.Level
	u.openMonitor()
	if !slices.ContainsFunc(b.Sent(), func(m any) bool { n, ok := m.(proto.NewSession); return ok && n.Monitor && n.SessionID == "s1" }) {
		t.Fatalf("sent %v; want NewSession{Monitor} for s1", b.Sent())
	}
}
