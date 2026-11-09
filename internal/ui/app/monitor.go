package app

import (
	"time"

	gl "gioui.org/layout"
	"github.com/quanticstudios/sector/view"

	sectortheme "github.com/quanticstudios/sector/theme"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Monitorer is optionally implemented by a Backend: the latest
// MonitorSample from the daemon that owns Monitor tab ws, and when it came
// by this machine's clock; false before the first.
type Monitorer interface {
	Monitor(ws string) (s proto.MonitorSample, at time.Time, ok bool)
}

// monitors are the window's Monitor tabs. Their views are made on first
// draw and dropped with their tab; each keeps its own history, so two
// machines never mix.
type monitors struct {
	tabs  map[string]*monitorTab
	th    sectortheme.Theme // pitwall's theme as sector's; the views keep a pointer to it
	of    *theme.Theme      // the pitwall theme th was filled from
	shown string            // the Monitor tab drawn this frame, "" for none
}

// monitorTab is one Monitor tab's view and what the window told its daemon.
type monitorTab struct {
	v      *view.View
	pushed time.Time     // when the last sample pushed came, zero before one
	err    string        // that sample's Err
	sent   time.Duration // the Interval of the last MonitorWatch, 0 for none
	epoch  int           // the Link.Epoch it was sent on
}

// staleAfter is how many intervals without a sample make a view stale.
const staleAfter = 3

// sectorTheme fills t from pitwall's th, so the view draws in the user's
// colors and fonts.
func sectorTheme(t *sectortheme.Theme, th *theme.Theme) {
	*t = sectortheme.Theme{
		Shaper: th.Shaper, Font: th.UIFont, Mono: th.MonoFont,
		Bg: th.Bg, Surface: th.Surface, SurfaceSecondary: th.SurfaceSecondary,
		SurfaceElevated: th.SurfaceElevated, Border: th.Border, Fg: th.Fg,
		Muted: th.Muted, Primary: th.Primary, Red: th.Red, Yellow: th.Yellow,
		Green: th.Green, Blue: th.Blue, Purple: th.Purple,
		MemUsed: sectortheme.Hex("#a184f0"), MemCache: sectortheme.Hex("#4a3d78"),
	}
	t.BorderSubtle = sectortheme.Mix(t.Surface, t.Fg, 0.07)
	t.TextQuiet = sectortheme.Mix(t.Bg, t.Muted, 0.85)
}

// layoutMonitor draws Monitor tab ws: its machine's latest sample, pushed
// to the view once.
func (u *ui) layoutMonitor(gtx gl.Context, st *model.State, ws *model.Workspace) {
	m := &u.mon
	m.shown = ws.ID
	mb, ok := u.b.(Monitorer)
	if !ok {
		u.monitorLine(gtx, "Monitor needs a pitwall daemon")
		return
	}
	if m.of != u.th {
		sectorTheme(&m.th, u.th) // in place: the views keep their history
		m.of = u.th
	}
	if m.tabs == nil {
		m.tabs = map[string]*monitorTab{}
	}
	t := m.tabs[ws.ID]
	if t == nil {
		t = &monitorTab{v: view.NewWith(&m.th, view.Options{Embedded: true})}
		m.tabs[ws.ID] = t
	}
	if s, at, ok := mb.Monitor(ws.ID); ok && at.After(t.pushed) {
		t.pushed, t.err = at, s.Err
		if s.Err == "" {
			t.v.Push(s.Snapshot, s.Table)
		}
	}
	switch {
	case t.err != "":
		u.monitorLine(gtx, "Monitor: "+t.err)
		return
	case t.pushed.IsZero():
		u.monitorLine(gtx, "Reading "+cmpHost(u.hostOf(ws.ID))+"…")
		return
	}
	if stale(t, u.monitorHostUp(ws.ID), gtx.Now) {
		t.v.SetStale(t.pushed)
	} else {
		t.v.SetStale(time.Time{})
	}
	t.v.Layout(gtx)
}

// stale reports whether t's view shows old data at now: its host is down,
// or no sample came for staleAfter intervals while it is not paused.
func stale(t *monitorTab, up bool, now time.Time) bool {
	return !up || !t.v.Paused() && now.Sub(t.pushed) > staleAfter*t.v.Interval()
}

// cmpHost names a host for a line of text, "this machine" for "".
func cmpHost(host string) string {
	if host == "" {
		return "this machine"
	}
	return host
}

// monitorLine draws one centered line in place of the view.
func (u *ui) monitorLine(gtx gl.Context, s string) {
	gl.Center.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
		return para(gtx, u.th, u.th.UIFont, u.th.Sp(theme.Body), u.th.Muted, s)
	})
}

// monitorHostUp reports whether the daemon that owns tab id is connected.
func (u *ui) monitorHostUp(id string) bool {
	if h, ok := u.b.(Hoster); ok {
		name := h.HostOf(id)
		for _, hi := range u.hosts {
			if hi.Name == name {
				return hi.Up
			}
		}
		return false
	}
	return u.link().State == LinkUp
}

// syncMonitors tells each Monitor tab's daemon what the window wants: the
// view's interval while the tab is on screen and not paused, else nothing.
// A new connection starts with no watchers, so a watch goes again on each.
// It drops the views of tabs gone from st.
func (u *ui) syncMonitors(st *model.State) {
	m := &u.mon
	epoch := u.link().Epoch
	for id, t := range m.tabs {
		gone := findWorkspace(st, id) == nil
		want := time.Duration(0)
		if id == m.shown && !gone && !t.v.Paused() {
			want = t.v.Interval()
		}
		if want != t.sent || want != 0 && epoch != t.epoch {
			u.send(proto.MonitorWatch{WorkspaceID: id, Interval: want})
			t.sent, t.epoch = want, epoch
		}
		if gone {
			delete(m.tabs, id)
		}
	}
	m.shown = ""
}

// openMonitor opens, or shows, the Monitor tab of the shown tab's machine
// in the window's session.
func (u *ui) openMonitor() {
	host := u.hostOf(u.nav.workspace)
	if u.link().Level < proto.MonitorTabLevel {
		u.sendFailed(cmpHost(host) + " runs an older pitwall; update it there to monitor it")
		return
	}
	st := u.b.State()
	u.nav.expectSession(&st) // show it once it comes, as a new tab
	ns := proto.NewSession{SessionID: u.nav.session, Monitor: true}
	if _, ok := u.b.(Hoster); ok {
		u.send(NewTabOn{Host: host, Tab: ns})
		return
	}
	u.send(ns)
}
