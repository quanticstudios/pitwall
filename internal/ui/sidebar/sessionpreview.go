package sidebar

import (
	"image"
	"slices"
	"strconv"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

const sessionPreviewDelay = 400 * time.Millisecond

type sessionPreview struct {
	hover  hoverState
	anchor image.Rectangle
	inside bool
	tag    int
	rows   map[string]*widget.Clickable
}

type sessionTab struct {
	ws            model.Workspace
	dot, activity *model.Activity
}

// sessionPreviewTabs orders live tabs by the host list's dot rule.
func sessionPreviewTabs(st *model.State, session string) []sessionTab {
	acts := map[string][]model.Activity{}
	for _, a := range st.Activities {
		acts[a.WorkspaceID] = append(acts[a.WorkspaceID], a)
	}
	var tabs []sessionTab
	for _, ws := range st.Workspaces {
		if ws.SessionID != session || ws.Detached {
			continue
		}
		dot, _, _ := sessionActivity(acts[ws.ID])
		a := dot
		if a == nil {
			a = model.Aggregate(acts[ws.ID])
		}
		tabs = append(tabs, sessionTab{ws: ws, dot: dot, activity: a})
	}
	slices.SortStableFunc(tabs, func(a, b sessionTab) int {
		switch {
		case a.dot == nil && b.dot == nil:
			return 0
		case a.dot == nil:
			return 1
		case b.dot == nil:
			return -1
		case model.NeedsYou(a.dot.State) && !model.NeedsYou(b.dot.State):
			return -1
		case !model.NeedsYou(a.dot.State) && model.NeedsYou(b.dot.State):
			return 1
		}
		return model.StateRank(b.dot.State) - model.StateRank(a.dot.State)
	})
	return tabs
}

func (s *Sidebar) sessionPreviewFrame(gtx layout.Context, over string) {
	p := &s.preview
	if !s.SessionPreview || s.TreeOnly || s.drag.kind != 0 || s.menuOpen() || s.Editing() {
		*p = sessionPreview{}
		return
	}
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &p.tag, Kinds: pointer.Enter | pointer.Leave | pointer.Cancel})
		if !ok {
			break
		}
		if e, ok := ev.(pointer.Event); ok {
			p.inside = e.Kind == pointer.Enter
		}
	}
	if over == "" && p.inside {
		over = p.hover.shown
	}
	p.hover.delay = sessionPreviewDelay
	if wake := p.hover.step(gtx.Now, over); !wake.IsZero() {
		gtx.Execute(op.InvalidateCmd{At: wake})
	}
	if p.hover.shown == "" {
		p.inside = false
	}
}

func (s *Sidebar) drawSessionPreview(gtx layout.Context, th *theme.Theme, st *model.State) {
	p := &s.preview
	if !s.SessionPreview || s.TreeOnly || len(s.Hosts) < 2 || p.hover.shown == "" {
		return
	}
	ss := st.Session(p.hover.shown)
	if ss == nil {
		return
	}
	tabs := sessionPreviewTabs(st, ss.ID)
	pad, header := gtx.Dp(6), gtx.Dp(28)
	rh := max(gtx.Dp(32), gtx.Sp(th.Sp(theme.Body)*1.5)+gtx.Dp(8))
	n := min(12, len(tabs), max(0, (s.height-gtx.Dp(16)-2*pad-2*header)/rh))
	size := image.Pt(min(gtx.Dp(340), s.bounds().Dx()-gtx.Dp(16)), 2*pad+header+n*rh)
	if len(tabs) > n || len(tabs) == 0 {
		size.Y += header
	}
	if p.rows == nil {
		p.rows = map[string]*widget.Clickable{}
	}
	for _, tab := range tabs[:n] {
		c := p.rows[tab.ws.ID]
		if c == nil {
			c = new(widget.Clickable)
			p.rows[tab.ws.ID] = c
		}
		for c.Clicked(gtx) {
			s.events = append(s.events, ShowSession{SessionID: ss.ID}, SelectWorkspace{WorkspaceID: tab.ws.ID})
			s.HideHover()
			gtx.Execute(op.InvalidateCmd{})
			return
		}
	}
	m := op.Record(gtx.Ops)
	bg := th.SurfaceElevated
	kit.Surface(gtx, image.Rectangle{Max: size}, gtx.Dp(theme.RadiusPopover), kit.Raised, th.Border, bg)
	y := pad
	line := func(h int, draw layout.Widget) {
		off := op.Offset(image.Pt(pad, y)).Push(gtx.Ops)
		g := gtx
		g.Constraints = layout.Exact(image.Pt(size.X-2*pad, h))
		draw(g)
		off.Pop()
		y += h
	}
	line(header, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Left: 8, Right: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.W.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return label(gtx, th, semibold(th.UIFont), th.Sp(theme.Small), th.Muted, ss.Name)
			})
		})
	})
	v := &view{th: th, st: st, now: gtx.Now}
	for _, tab := range tabs[:n] {
		line(rh, func(gtx layout.Context) layout.Dimensions {
			c := p.rows[tab.ws.ID]
			return clickable(gtx, c, func(gtx layout.Context) layout.Dimensions {
				base := bg
				if c.Hovered() {
					base = th.SurfaceSecondary
				}
				paint.FillShape(gtx.Ops, base, clip.UniformRRect(image.Rectangle{Max: gtx.Constraints.Max}, gtx.Dp(6)).Op(gtx.Ops))
				return layout.Inset{Left: 8, Right: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					items := []item{
						{w: func(gtx layout.Context) layout.Dimensions {
							if ag := AgentOf(st, tab.ws); ag != "" {
								return AgentMark(gtx, ag, gtx.Dp(14), th.Fg)
							}
							return drawIcon(gtx, icTerminal, gtx.Dp(14), th.Muted, 0)
						}},
						{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
							return label(gtx, th, semibold(th.UIFont), th.Sp(theme.Body), th.Fg, Title(tab.ws))
						}},
					}
					if tab.activity != nil {
						items = append(items, item{right: true, w: func(gtx layout.Context) layout.Dimensions {
							return pill(gtx, v, s, *tab.activity, base)
						}})
					}
					return hrow(gtx, rh, gtx.Dp(8), items...)
				})
			})
		})
		if model.Pulses(tab.activity) {
			gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(time.Second / 30)})
		}
	}
	if len(tabs) > n || len(tabs) == 0 {
		text := "No tabs"
		if len(tabs) > n {
			text = "+" + strconv.Itoa(len(tabs)-n) + " more"
		}
		line(header, func(gtx layout.Context) layout.Dimensions {
			return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return label(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, text)
			})
		})
	}
	pass := pointer.PassOp{}.Push(gtx.Ops)
	area := clip.Rect{Max: size}.Push(gtx.Ops)
	event.Op(gtx.Ops, &p.tag)
	area.Pop()
	pass.Pop()
	call := m.Stop()
	defer op.Offset(kit.Place(p.anchor, size, s.bounds(), kit.Beside, gtx.Dp(6), gtx.Dp(8))).Push(gtx.Ops).Pop()
	op.Defer(gtx.Ops, call)
}
