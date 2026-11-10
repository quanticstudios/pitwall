package app

import (
	"image"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"github.com/quanticstudios/sector/view"

	sectortheme "github.com/quanticstudios/sector/theme"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Monitorer is optionally implemented by a Backend: the latest
// MonitorSample from the daemon that owns Monitor tab ws, and when it came
// by this machine's clock; false before the first.
type Monitorer interface {
	Monitor(ws string) (s proto.MonitorSample, at time.Time, ok bool)
}

// monitors are the window's Monitor tabs and the monitor shown in place
// of the panes. Their views are made on first draw; each keeps its own
// history, so two machines never mix. A Monitor tab's view is keyed by its
// tab id and dropped with its tab. The monitor in place of the panes keeps
// one view per machine, keyed by machineKey, for the window's life, so a
// peek from any tab of that machine shows the history it has.
type monitors struct {
	tabs  map[string]*monitorTab
	th    sectortheme.Theme // pitwall's theme as sector's; the views keep a pointer to it
	of    *theme.Theme      // the pitwall theme th was filled from
	shown string            // the Monitor tab drawn this frame, "" for none

	// on shows the monitor in place of the panes, opened over tab ws;
	// showing another tab closes it.
	on    bool
	ws    string
	tag   int  // its key focus, which Esc goes to
	focus bool // take the keyboard on the next frame
}

// monitorTab is one view and what the window told its daemon.
type monitorTab struct {
	v      *view.View
	ws     string            // the tab its watches name, which picks the daemon
	pushed time.Time         // when the last sample pushed came, zero before one
	tabs   map[string]string // that sample's Tabs
	err    string            // that sample's Err
	sent   time.Duration     // the Interval of the last MonitorWatch, 0 for none
	epoch  int               // the Link.Epoch it was sent on
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

// machineKey keys the view of the monitor in place of the panes for
// host's machine, apart from any Monitor tab's id.
func machineKey(host string) string { return "machine:" + host }

// layoutMonitor draws view key of tab ws's machine: its latest sample,
// pushed to the view once, with agents named by the tabs they run in.
// key is ws's id for a Monitor tab.
func (u *ui) layoutMonitor(gtx gl.Context, st *model.State, key string, ws *model.Workspace) {
	m := &u.mon
	m.shown = key
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
	t := m.tabs[key]
	if t == nil {
		t = &monitorTab{v: view.NewWith(&m.th, view.Options{Embedded: true}), ws: ws.ID}
		m.tabs[key] = t
	}
	if t.ws != ws.ID { // the machine's view, shown from another of its tabs
		u.watch(t, 0, t.epoch)
		t.ws = ws.ID
	}
	if s, at, ok := mb.Monitor(ws.ID); ok && at.After(t.pushed) {
		t.pushed, t.tabs, t.err = at, s.Tabs, s.Err
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
	t.v.SetLabels(monitorLabels(st, t.tabs))
	if stale(t, u.monitorHostUp(ws.ID), gtx.Now) {
		t.v.SetStale(t.pushed)
	} else {
		t.v.SetStale(time.Time{})
	}
	t.v.Layout(gtx)
	if key, ok := t.v.Clicked(); ok {
		if fs, ok := monitorFocus(st, t.tabs, key); ok {
			u.queueFocus(fs)
		}
	}
}

// monitorFocus is where a click on group key's row goes: the tab and pane
// its agent runs in, false for a group no pane of st runs.
func monitorFocus(st *model.State, tabs map[string]string, key string) (proto.FocusSession, bool) {
	p := findPane(st, tabs[key])
	if p == nil {
		return proto.FocusSession{}, false
	}
	w := findWorkspace(st, p.WorkspaceID)
	if w == nil {
		return proto.FocusSession{}, false
	}
	return proto.FocusSession{WorkspaceID: w.ID, SessionID: w.SessionID, Pane: p.ID}, true
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

// monitorLabels names each agent group of tabs, by group key, after the
// tab its pane is in now, so a renamed tab shows its new name at once.
func monitorLabels(st *model.State, tabs map[string]string) map[string]string {
	out := map[string]string{}
	for key, pane := range tabs {
		if p := findPane(st, pane); p != nil {
			if w := findWorkspace(st, p.WorkspaceID); w != nil && w.Label != "" {
				out[key] = w.Label
			}
		}
	}
	return out
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
	for key, t := range m.tabs {
		gone := findWorkspace(st, t.ws) == nil
		want := time.Duration(0)
		if key == m.shown && !gone && !t.v.Paused() {
			want = t.v.Interval()
		}
		u.watch(t, want, epoch)
		if gone && key == t.ws { // a machine's view keeps its history
			delete(m.tabs, key)
		}
	}
	m.shown = ""
}

// watch asks t's daemon for samples every want, 0 for none, unless it was
// asked so already on this connection.
func (u *ui) watch(t *monitorTab, want time.Duration, epoch int) {
	if want != t.sent || want != 0 && epoch != t.epoch {
		u.send(proto.MonitorWatch{WorkspaceID: t.ws, Interval: want})
		t.sent, t.epoch = want, epoch
	}
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

// toggleMonitor shows the monitor of the shown tab's machine in place of
// the panes, as the board shows, or hides it. The panes come back as they
// were: they are not drawn meanwhile, and nothing of theirs changes.
func (u *ui) toggleMonitor() {
	if u.mon.on {
		u.mon.on = false
		return
	}
	if u.link().Level < proto.MonitorLevel {
		u.sendFailed(cmpHost(u.hostOf(u.nav.workspace)) + " runs an older pitwall; update it there to monitor it")
		return
	}
	u.settings.Hide()
	u.review.hide()
	u.boardOn = false
	u.mon.on, u.mon.ws, u.mon.focus = true, u.nav.workspace, true
}

// layoutMonitorView draws the monitor in place of the panes under a header
// like the board's, or the panes again once another tab is shown.
func (u *ui) layoutMonitorView(gtx gl.Context, st *model.State) {
	ws := findWorkspace(st, u.mon.ws)
	if ws == nil || u.nav.workspace != u.mon.ws {
		u.mon.on = false
		u.layoutPanes(gtx, st)
		return
	}
	// why: Gio gives key focus only to a tag with a FocusFilter.
	for {
		ev, ok := gtx.Event(key.FocusFilter{Target: &u.mon.tag}, key.Filter{Name: key.NameEscape})
		if !ok {
			break
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			u.mon.on = false
		}
	}
	if !u.mon.on {
		u.layoutPanes(gtx, st) // Esc: the panes on this frame
		return
	}
	size := gtx.Constraints.Max
	paint.FillShape(gtx.Ops, u.th.Bg, clip.Rect{Max: size}.Op())
	// why: the focus tag's area goes under the view, or it would take its clicks.
	event.Op(gtx.Ops, &u.mon.tag)
	if u.mon.focus {
		u.mon.focus = false
		gtx.Execute(key.FocusCmd{Tag: &u.mon.tag})
	}
	top, headH := gtx.Dp(12), gtx.Dp(28)
	sidebar.PageHeader(gtx, u.th, image.Rect(gtx.Dp(16), top, size.X-gtx.Dp(14), top+headH), "Monitor", "Esc", "back to panes")
	y := top + headH + gtx.Dp(theme.SpaceS)
	defer clip.Rect{Min: image.Pt(0, y), Max: size}.Push(gtx.Ops).Pop()
	vgtx := gtx
	vgtx.Constraints = gl.Exact(image.Pt(size.X, max(0, size.Y-y)))
	off := op.Offset(image.Pt(0, y)).Push(gtx.Ops)
	u.layoutMonitor(vgtx, st, machineKey(u.hostOf(ws.ID)), ws)
	off.Pop()
}
