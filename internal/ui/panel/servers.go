package panel

import (
	"cmp"
	"fmt"
	"image"
	"slices"
	"strconv"
	"strings"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Lucide's globe and square, for a server's row and its Stop.
const (
	IconGlobe = "M12 2a10 10 0 1 0 0 20a10 10 0 1 0 0-20zM12 2a14.5 14.5 0 0 0 0 20a14.5 14.5 0 0 0 0-20M2 12h20"
	icSquare  = "M5 3h14a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2z"
)

// ServerList is what the chat's tab runs that listens on a port, for the
// side panel's Servers view (T3 Code's thread Servers panel).
type ServerList struct {
	// List is State.Servers of the chat's tab.
	List []model.Server
	// Note says why the view cannot list, as for an older daemon or a
	// system pitwall cannot read processes on; "" when it can.
	Note string
	// Remote: the servers run on another machine, whose localhost a
	// browser here does not reach, so they have no Open.
	Remote bool
}

// ServerAction is a click in the Servers view: Open a URL in the default
// browser, or stop PID listening on Port (SIGTERM, or SIGKILL with Force).
type ServerAction struct {
	Open      string
	PID, Port int
	Force     bool
}

// srvRow is where a server's Stop is: sent and waiting, stopping still
// after SIGTERM's grace, asking before SIGKILL, or failed with err.
type srvRow struct {
	phase int
	err   string
}

const (
	srvIdle     = iota
	srvStopping // ServerStop sent
	srvStubborn // it outlived SIGTERM: Force stop shows
	srvConfirm  // Force stop clicked: Kill asks
	srvKilling  // ServerStop Force sent
)

// srvState is the Servers view's: each row's Stop, and the clicks not yet
// taken.
type srvState struct {
	rows map[string]*srvRow
	out  []ServerAction
	list layout.List
}

// serverPort is one row of the view: a server on one of its ports.
type serverPort struct {
	model.Server
	port int
}

func (p serverPort) key() string { return strconv.Itoa(p.PID) + ":" + strconv.Itoa(p.port) }

// serverRows is one row per port, by port.
func serverRows(ss []model.Server) []serverPort {
	var out []serverPort
	for _, s := range ss {
		for _, p := range s.Ports {
			out = append(out, serverPort{s, p})
		}
	}
	slices.SortStableFunc(out, func(a, b serverPort) int { return a.port - b.port })
	return out
}

// ServersOpen reports whether the side panel shows the Servers view.
func (t *Transcript) ServersOpen() bool { return t.side.on && t.side.servers }

// ShowServers opens the side panel at the Servers view, or closes it when
// it shows that view.
func (t *Transcript) ShowServers() {
	if t.ServersOpen() {
		t.CloseFiles()
		return
	}
	t.side.on, t.side.servers = true, true
}

// ServerActions takes the Servers view's clicks since the last call.
func (t *Transcript) ServerActions() []ServerAction {
	out := t.side.srv.out
	t.side.srv.out = nil
	return out
}

// ServerStopped takes the daemon's answer to a stop: running when the
// server outlived the signal, err when it was not sent.
func (t *Transcript) ServerStopped(pid, port int, force, running bool, err string) {
	r := t.side.srv.row(serverPort{model.Server{PID: pid}, port}.key())
	switch {
	case err != "":
		r.phase, r.err = srvIdle, err
	case running && !force:
		r.phase, r.err = srvStubborn, ""
	case running:
		r.phase, r.err = srvIdle, "It is still running after SIGKILL"
	default:
		r.phase, r.err = srvIdle, ""
	}
}

func (s *srvState) row(k string) *srvRow {
	if s.rows == nil {
		s.rows = map[string]*srvRow{}
	}
	r := s.rows[k]
	if r == nil {
		r = &srvRow{}
		s.rows[k] = r
	}
	return r
}

// serversView lists the tab's servers: localhost:port, who started it and
// its process, Open and Stop.
func (t *Transcript) serversView(gtx layout.Context, d *cdraw, size image.Point) {
	s := &t.side.srv
	rows := serverRows(t.Servers.List)
	for k := range s.rows { // a server that went takes its Stop's state along
		if !slices.ContainsFunc(rows, func(p serverPort) bool { return p.key() == k }) {
			delete(s.rows, k)
		}
	}
	for _, p := range rows {
		t.serverClicks(gtx, p)
	}
	switch {
	case t.Servers.Note != "":
		d.sideNote(gtx, "server", t.Servers.Note)
		return
	case len(rows) == 0:
		d.sideNote(gtx, "server", "No servers running. A dev server the agent or a terminal in this tab starts shows here with its port.")
		return
	}
	s.list.Axis = layout.Vertical
	w := size.X - 2*gtx.Dp(sidePad)
	s.list.Layout(gtx, len(rows)+1, func(gtx layout.Context, i int) layout.Dimensions {
		gtx.Constraints = layout.Constraints{Min: image.Pt(size.X, 0), Max: image.Pt(size.X, 1<<20)}
		if i == len(rows) {
			return layout.Dimensions{Size: image.Pt(size.X, gtx.Dp(sidePad))}
		}
		top := theme.SpaceS
		if i == 0 {
			top = sidePad
		}
		return layout.Inset{Top: top, Left: sidePad, Right: sidePad}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints = layout.Constraints{Min: image.Pt(w, 0), Max: image.Pt(w, 1<<20)}
			return t.serverRow(gtx, d, rows[i])
		})
	})
}

// serverClicks turns row p's buttons into actions and phases.
func (t *Transcript) serverClicks(gtx layout.Context, p serverPort) {
	s := &t.side.srv
	k := p.key()
	r := s.row(k)
	for t.click("srv:open:" + k).Clicked(gtx) {
		if !t.Servers.Remote {
			s.out = append(s.out, ServerAction{Open: fmt.Sprintf("http://localhost:%d", p.port)})
		}
	}
	for t.click("srv:stop:" + k).Clicked(gtx) {
		if r.phase == srvIdle {
			r.phase, r.err = srvStopping, ""
			s.out = append(s.out, ServerAction{PID: p.PID, Port: p.port})
		}
	}
	for t.click("srv:force:" + k).Clicked(gtx) {
		if r.phase == srvStubborn {
			r.phase = srvConfirm
		}
	}
	for t.click("srv:cancel:" + k).Clicked(gtx) {
		if r.phase == srvConfirm {
			r.phase = srvStubborn
		}
	}
	for t.click("srv:kill:" + k).Clicked(gtx) {
		if r.phase == srvConfirm {
			r.phase = srvKilling
			s.out = append(s.out, ServerAction{PID: p.PID, Port: p.port, Force: true})
		}
	}
}

// serverWho is the row's second line: who started it, its process name
// and pid.
func serverWho(p serverPort) string {
	who := "Terminal"
	if p.Agent {
		who = "Agent"
	}
	parts := []string{who}
	if p.Name != "" {
		parts = append(parts, p.Name)
	}
	return strings.Join(append(parts, "pid "+strconv.Itoa(p.PID)), " · ")
}

// serverRow is one server: the globe, localhost:port over who started it,
// and on the right Open and Stop, or where its stop is.
func (t *Transcript) serverRow(gtx layout.Context, d *cdraw, p serverPort) layout.Dimensions {
	th := d.th
	k := p.key()
	r := t.side.srv.row(k)
	gap := gtx.Dp(theme.SpaceS)
	var btns []layout.Widget
	switch r.phase {
	case srvIdle:
		if !t.Servers.Remote {
			btns = append(btns, func(gtx layout.Context) layout.Dimensions {
				return d.iconButton(gtx, t.click("srv:open:"+k), icExternal, "Open in browser")
			})
		}
		btns = append(btns, func(gtx layout.Context) layout.Dimensions {
			return d.iconButton(gtx, t.click("srv:stop:"+k), icSquare, "Stop server")
		})
	case srvStopping, srvKilling:
		btns = append(btns, func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, "Stopping…")
		})
	case srvStubborn:
		btns = append(btns, func(gtx layout.Context) layout.Dimensions {
			return kit.Button(gtx, th, t.click("srv:force:"+k), kit.Secondary, kit.Small, "Force stop")
		})
	}
	bc, bd := measure(gtx, func(gtx layout.Context) layout.Dimensions { return hrow(gtx, gtx.Dp(theme.Space2XS), btns...) })
	is := gtx.Dp(16)
	textW := max(0, gtx.Constraints.Max.X-is-gap-bd.Size.X-gap)
	tc, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints = layout.Constraints{Max: image.Pt(textW, 1<<20)}
		return vstack(gtx, gtx.Dp(theme.Space2XS),
			func(gtx layout.Context) layout.Dimensions {
				return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Body), th.Fg, "localhost:"+strconv.Itoa(p.port))
			},
			func(gtx layout.Context) layout.Dimensions {
				return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, serverWho(p))
			})
	})
	h := max(td.Size.Y, bd.Size.Y)
	at(gtx, image.Pt(0, (h-is)/2), func(gtx layout.Context) layout.Dimensions {
		return sidebar.IconPath(gtx, IconGlobe, is, th.Muted)
	})
	at(gtx, image.Pt(is+gap, (h-td.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { tc.Add(gtx.Ops); return td })
	at(gtx, image.Pt(gtx.Constraints.Max.X-bd.Size.X, (h-bd.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { bc.Add(gtx.Ops); return bd })
	y := h
	// Under the row: why it failed, it outlived SIGTERM, or Kill's question.
	var note layout.Widget
	switch {
	case r.err != "":
		note = func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, th.UIFont, th.Sp(theme.Small), th.Readable(th.Red, th.Sidebar), r.err, 3)
		}
	case r.phase == srvStubborn:
		note = func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, "Still running: it ignored the stop signal.", 2)
		}
	case r.phase == srvConfirm:
		note = func(gtx layout.Context) layout.Dimensions {
			return t.serverConfirm(gtx, d, p)
		}
	}
	if note != nil {
		y += gtx.Dp(theme.SpaceXS)
		g := gtx
		g.Constraints = layout.Constraints{Max: image.Pt(max(0, gtx.Constraints.Max.X-is-gap), 1<<20)}
		nd := at(g, image.Pt(is+gap, y), note)
		y += nd.Size.Y
	}
	y += gtx.Dp(theme.SpaceS)
	paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Min: image.Pt(is+gap, y-gtx.Dp(1)), Max: image.Pt(gtx.Constraints.Max.X, y)}.Op())
	return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, y)}
}

// serverConfirm asks before SIGKILL: what it kills, Cancel and Kill.
func (t *Transcript) serverConfirm(gtx layout.Context, d *cdraw, p serverPort) layout.Dimensions {
	th := d.th
	k := p.key()
	name := cmp.Or(p.Name, "the process")
	return vstack(gtx, gtx.Dp(theme.SpaceS),
		func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, th.UIFont, th.Sp(theme.Small), th.Fg, fmt.Sprintf("Kill %s (pid %d) now? It gets no chance to clean up, so unsaved work in it is lost.", name, p.PID), 3)
		},
		func(gtx layout.Context) layout.Dimensions {
			return hrow(gtx, gtx.Dp(theme.SpaceS),
				func(gtx layout.Context) layout.Dimensions {
					return kit.Button(gtx, th, t.click("srv:cancel:"+k), kit.Secondary, kit.Small, "Cancel")
				},
				func(gtx layout.Context) layout.Dimensions {
					return kit.Button(gtx, th, t.click("srv:kill:"+k), kit.Danger, kit.Small, "Kill")
				})
		})
}
