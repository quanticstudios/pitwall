package sidebar

import (
	"cmp"
	"image"
	"image/color"
	"slices"
	"strconv"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// HostInfo is one machine of a window whose sessions are on several, for
// the host list above the tabs.
type HostInfo struct {
	Name     string   // "" for this machine
	Up       bool     // connected now
	Note     string   // while it is not up: why, or that it is connecting
	Sessions []string // its session ids, in the switcher's order
}

// ShowSession is a click on a session in the host list.
type ShowSession struct{ SessionID string }

// Blocked is the activities in st that wait on someone, on any host and in
// any session, most urgent first: approvals, questions, plans and errors.
// Detached tabs are left out, as the sidebar leaves them out.
func Blocked(st *model.State) []model.Activity {
	live := map[string]bool{}
	for _, w := range st.Workspaces {
		live[w.ID] = !w.Detached
	}
	var out []model.Activity
	for _, a := range st.Activities {
		switch a.State {
		case model.StatePendingApproval, model.StateAwaitingInput, model.StatePlanReady, model.StateError:
			if live[a.WorkspaceID] {
				out = append(out, a)
			}
		}
	}
	model.SortActivities(out)
	return out
}

// hostName is how the list names a host.
func hostName(h string) string {
	if h == "" {
		return "local"
	}
	return h
}

// sessionActivity keeps waiting agents and unseen finished turns above work.
func sessionActivity(acts []model.Activity) *model.Activity {
	var wait, finished, work *model.Activity
	for i := range acts {
		a := &acts[i]
		switch a.State {
		case model.StateError, model.StatePendingApproval, model.StateAwaitingInput, model.StatePlanReady:
			wait = urgent(wait, a)
		case model.StateCompleted, model.StatePROpen:
			if a.Unseen {
				finished = urgent(finished, a)
			}
		default:
			if model.Pulses(a) {
				work = urgent(work, a)
			}
		}
	}
	return cmp.Or(wait, finished, work)
}

// sessionState applies the tree's Watching state and excludes detached tabs.
func sessionState(st *model.State) model.State {
	out := *st
	out.Workspaces = slices.DeleteFunc(slices.Clone(st.Workspaces), func(w model.Workspace) bool { return w.Detached })
	out.Activities = slices.Clone(st.Activities)
	watching := st.Watching()
	for i := range out.Activities {
		a := &out.Activities[i]
		if watching[a.WorkspaceID] && a.State == model.StateCompleted {
			a.State, a.Unseen = model.StateWatching, false
		}
	}
	return out
}

func sessionDotColor(th *theme.Theme, state model.AgentState) color.NRGBA {
	if state == model.StatePROpen {
		state = model.StateCompleted
	}
	return StateColor(th, state)
}

// hostList draws, when the window shows more than one machine, each
// machine's heading and sessions, then the agents that wait on someone
// anywhere, or in the inbox anywhere but the current session. st is every
// session's state.
func (s *Sidebar) hostList(gtx layout.Context, th *theme.Theme, st *model.State) layout.Dimensions {
	if len(s.Hosts) < 2 {
		s.preview = sessionPreview{}
		return layout.Dimensions{}
	}
	live := sessionState(st)
	st = &live
	if s.hostBtn == nil {
		s.hostBtn = map[string]*widget.Clickable{}
	}
	btn := func(key string) *widget.Clickable {
		c := s.hostBtn[key]
		if c == nil {
			c = new(widget.Clickable)
			s.hostBtn[key] = c
		}
		return c
	}
	acts := map[string][]model.Activity{} // by session
	tabs := map[string]int{}
	sessionOf := map[string]string{}
	for _, w := range st.Workspaces {
		if !w.Detached {
			sessionOf[w.ID] = w.SessionID
			tabs[w.SessionID]++
		}
	}
	for _, a := range st.Activities {
		if ss := sessionOf[a.WorkspaceID]; ss != "" {
			acts[ss] = append(acts[ss], a)
		}
	}
	hostOf, down := map[string]string{}, map[string]bool{} // by session
	for _, h := range s.Hosts {
		for _, id := range h.Sessions {
			hostOf[id], down[id] = h.Name, !h.Up
		}
	}
	blocked, head := s.waiting(st, sessionOf)
	if s.HideSessions && len(blocked) == 0 {
		s.preview = sessionPreview{}
		return layout.Dimensions{}
	}
	w := gtx.Constraints.Max.X
	y := 0
	place := func(h int, f func(gtx layout.Context)) {
		o := op.Offset(image.Pt(0, y)).Push(gtx.Ops)
		g := gtx
		g.Constraints = layout.Exact(image.Pt(w, h))
		f(g)
		o.Pop()
		y += h
	}
	heading := func(name, note string, c color.NRGBA) {
		place(gtx.Dp(26), func(gtx layout.Context) {
			pad := gtx.Dp(18)
			gtx.Constraints = layout.Exact(image.Pt(w-2*pad, gtx.Constraints.Max.Y))
			defer op.Offset(image.Pt(pad, 0)).Push(gtx.Ops).Pop()
			hrow(gtx, gtx.Constraints.Max.Y, gtx.Dp(8),
				item{w: func(gtx layout.Context) layout.Dimensions {
					return label(gtx, th, semibold(th.UIFont), th.Sp(theme.Small), th.Muted, name)
				}},
				item{shrink: true, right: true, w: func(gtx layout.Context) layout.Dimensions {
					return label(gtx, th, th.UIFont, th.Sp(theme.Small), c, note)
				}})
		})
	}
	row := func(c *widget.Clickable, current, dim bool, dot *model.Activity, name, right, count string) {
		place(gtx.Dp(28), func(gtx layout.Context) {
			pad := gtx.Dp(listPad)
			size := image.Pt(w-2*pad, gtx.Constraints.Max.Y)
			defer op.Offset(image.Pt(pad, 0)).Push(gtx.Ops).Pop()
			gtx.Constraints = layout.Exact(size)
			clickable(gtx, c, func(gtx layout.Context) layout.Dimensions {
				rr := clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(6))
				bg := th.Sidebar
				switch {
				case current:
					bg = theme.Mix(th.Sidebar, th.Primary, 0.14)
				case c.Hovered():
					bg = th.SurfaceSecondary
				}
				paint.FillShape(gtx.Ops, bg, rr.Op(gtx.Ops))
				fg := th.Fg
				if dim {
					fg = th.Muted
				}
				in := gtx.Dp(10)
				g := gtx
				g.Constraints = layout.Exact(image.Pt(size.X-2*in, size.Y))
				if count != "" {
					// The extra count must not shorten the session name.
					m := op.Record(gtx.Ops)
					n := label(g, th, th.UIFont, th.Sp(theme.Body), fg, name)
					r := label(g, th, th.UIFont, th.Sp(theme.Small), th.Muted, right+" · "+count)
					m.Stop()
					if n.Size.X+r.Size.X+3*gtx.Dp(8) <= g.Constraints.Max.X {
						right += " · " + count
					}
				}
				o := op.Offset(image.Pt(in, 0)).Push(gtx.Ops)
				hrow(g, size.Y, gtx.Dp(8),
					item{w: func(gtx layout.Context) layout.Dimensions {
						d := gtx.Dp(8)
						if dot != nil {
							col := sessionDotColor(th, dot.State)
							if model.Pulses(dot) {
								col = theme.Mix(bg, col, pulse(gtx.Now.Sub(s.epoch).Seconds()))
								gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(time.Second / 30)})
							}
							paint.FillShape(gtx.Ops, col, clip.Ellipse{Max: image.Pt(d, d)}.Op(gtx.Ops))
						}
						return layout.Dimensions{Size: image.Pt(d, d)}
					}},
					item{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
						return label(gtx, th, th.UIFont, th.Sp(theme.Body), fg, name)
					}},
					item{right: true, w: func(gtx layout.Context) layout.Dimensions {
						return label(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, right)
					}})
				o.Pop()
				return layout.Dimensions{Size: size}
			})
		})
	}
	y += gtx.Dp(6)
	over := ""
	if !s.HideSessions {
		for _, h := range s.Hosts {
			note, c := "", th.Muted
			if !h.Up {
				note, c = firstLine(h.Note), th.Red
				if note == "" || strings.HasSuffix(note, "ing…") {
					note, c = cmp.Or(note, "Disconnected"), th.Muted
				}
			}
			heading(hostName(h.Name), note, c)
			for _, id := range h.Sessions {
				ss := st.Session(id)
				if ss == nil {
					continue
				}
				for btn("s:" + id).Clicked(gtx) {
					s.HideHover()
					s.events = append(s.events, ShowSession{SessionID: id})
				}
				if btn("s:" + id).Hovered() {
					over = id
					s.preview.rowAnchor = image.Rect(0, gtx.Dp(56)+y, w, gtx.Dp(56)+y+gtx.Dp(28))
				}
				dot := sessionActivity(acts[id])
				sum := st.Summary(id)
				count := ""
				if sum.NeedsYou > 0 {
					count = strconv.Itoa(sum.NeedsYou) + " " + plural(sum.NeedsYou, "needs you", "need you")
				}
				row(btn("s:"+id), id == s.session, !h.Up, dot, ss.Name, strconv.Itoa(tabs[id])+" "+plural(tabs[id], "tab", "tabs"), count)
			}
		}
	}
	if len(blocked) > 0 {
		heading(head, "", th.Muted)
		for i := range blocked {
			a := blocked[i]
			ws := findWS(st, a.WorkspaceID)
			if ws == nil {
				continue
			}
			for btn("a:" + a.PaneID).Clicked(gtx) {
				s.events = append(s.events, SelectWorkspace{WorkspaceID: a.WorkspaceID, PaneID: a.PaneID})
			}
			row(btn("a:"+a.PaneID), false, down[ws.SessionID], &a, Title(*ws), hostName(hostOf[ws.SessionID])+" · "+model.PillLabel(a), "")
		}
	}
	y += gtx.Dp(6)
	paint.FillShape(gtx.Ops, th.Border, clip.Rect{Min: image.Pt(0, y-1), Max: image.Pt(w, y)}.Op())
	s.sessionPreviewFrame(gtx, over)
	return layout.Dimensions{Size: image.Pt(w, y)}
}

// waiting is the host list's agents that wait on someone, and their
// heading. The inbox already shows the current session's, so there the
// list keeps only those of other sessions and hosts. sessionOf maps a tab
// to its session.
func (s *Sidebar) waiting(st *model.State, sessionOf map[string]string) ([]model.Activity, string) {
	blocked := Blocked(st)
	if !s.Inbox {
		return blocked, "Waiting on you"
	}
	return slices.DeleteFunc(blocked, func(a model.Activity) bool { return sessionOf[a.WorkspaceID] == s.session }), "Waiting elsewhere"
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return s
}

func findWS(st *model.State, id string) *model.Workspace {
	if i := slices.IndexFunc(st.Workspaces, func(w model.Workspace) bool { return w.ID == id }); i >= 0 {
		return &st.Workspaces[i]
	}
	return nil
}
