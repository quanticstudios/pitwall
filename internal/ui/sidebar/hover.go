package sidebar

import (
	"fmt"
	"image"
	"image/color"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

const (
	hoverDelay = 500 * time.Millisecond // resting on a row before its card opens
	hoverGrace = 150 * time.Millisecond // off every row before an open card closes
	hoverFade  = 100 * time.Millisecond
)

// groupIndent moves a grouped tab's row right of its group header, about
// one icon wide, so the tree reads at a glance.
const groupIndent unit.Dp = 12

// hoverState decides which tab's card shows from the row under the pointer
// and the clock. A card opens after hoverDelay on one row; while one is
// open, another row takes it over at once; off the rows it closes after
// hoverGrace. A dismissed card stays closed until the pointer is on another
// row.
type hoverState struct {
	over    string    // the row under the pointer, "" for none
	since   time.Time // when over last changed
	blocked string    // the row whose card was dismissed
	shown   string    // the row whose card is open
	shownAt time.Time // when it opened, for the fade
}

// step moves the state to now with the pointer over row over, and returns
// when it next needs a frame (zero for no need).
func (h *hoverState) step(now time.Time, over string) time.Time {
	if over != h.over {
		h.over, h.since = over, now
		if over != h.blocked {
			h.blocked = ""
		}
	}
	switch {
	case over == "":
		if h.shown == "" {
			return time.Time{}
		}
		if at := h.since.Add(hoverGrace); now.Before(at) {
			return at
		}
		h.shown = ""
	case over == h.blocked:
		h.shown = ""
	case h.shown != "":
		h.shown = over
	default:
		if at := h.since.Add(hoverDelay); now.Before(at) {
			return at
		}
		h.shown, h.shownAt = over, now
	}
	return time.Time{}
}

// dismiss closes the card and keeps it closed while the pointer stays on
// the same row.
func (h *hoverState) dismiss() {
	h.shown, h.blocked = "", h.over
}

// HideHover closes a tab's hover card, for a key press or the window
// losing focus.
func (s *Sidebar) HideHover() { s.hover.dismiss() }

// hoverFrame feeds the row under the pointer to the hover state. A drag,
// an open menu or a rename hides the card; the session switcher's preview
// never shows one.
func (s *Sidebar) hoverFrame(gtx layout.Context) {
	if s.ExpandAll {
		return
	}
	if s.drag.kind != 0 || s.menuOpen() || s.Editing() {
		s.hover.dismiss()
	}
	over := ""
	for _, e := range s.elems {
		if r := s.rows[e.id]; e.kind == 's' && r != nil && r.hovered() {
			over = e.id
		}
	}
	if wake := s.hover.step(gtx.Now, over); !wake.IsZero() {
		gtx.Execute(op.InvalidateCmd{At: wake})
	}
}

// card is what a tab's hover card shows. Lines lists which lines, in
// order: 't' title, 'f' folder, 'b' branch, 'o' the worktree's ports, 's'
// agent state, 'd' its detail, 'u' token use, 'j' the decision model's
// advice, 'p' pane count.
type card struct {
	title, when  string
	agent        model.Provider // its mark, or a terminal glyph for ""
	group        string         // the group's name
	groupColor   string         // its aide color id
	path, branch string
	ports        string // the worktree's port block, "3010-3019"
	add, del     int
	state        model.AgentState
	detail       string
	usage        string // UsageText of the tab's agents
	decision     string
	panes        int
}

func (c card) lines() string {
	out := "t"
	if c.group != "" || c.path != "" {
		out += "f"
	}
	if c.branch != "" {
		out += "b"
	}
	if c.ports != "" {
		out += "o"
	}
	if c.state != "" {
		out += "s"
		if c.detail != "" {
			out += "d"
		}
	}
	if c.usage != "" {
		out += "u"
	}
	if c.decision != "" {
		out += "j"
	}
	if c.panes > 1 {
		out += "p"
	}
	return out
}

// cardFor collects tab ws's card from the frame's view.
func cardFor(v *view, ws model.Workspace) card {
	c := card{title: Title(ws), when: relTime(v.now, ws.UpdatedAt), agent: v.agent[ws.ID],
		path: model.ShortPath(v.st.LivePath(ws)), branch: ws.Branch, ports: ws.Ports.String()}
	if g := v.groupOf(ws.ID); g != "" {
		for _, p := range v.st.Projects {
			if p.ID == g {
				c.group, c.groupColor = p.Name, p.Color
			}
		}
	}
	if st, ok := v.st.Stats[ws.ID]; ok && c.branch != "" {
		c.add, c.del = st.Additions, st.Deletions
	}
	if a := v.activity[ws.ID]; a != nil {
		c.state, c.detail = a.State, clampDetail(a.Detail)
		c.decision = AdviceText(*a, v.st.Decide.Provider)
	}
	for _, p := range v.st.Panes {
		if p.WorkspaceID == ws.ID {
			c.panes++
		}
	}
	return c
}

// detailRunes caps the detail text before layout; the card shows at most
// three lines of it anyway.
const detailRunes = 400

// clampDetail is an agent's question or detail on one line: whitespace
// runs, newlines included, become one space, and long text is cut with an
// ellipsis. The card wraps it to at most three lines.
func clampDetail(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > detailRunes {
		s = string(r[:detailRunes-1]) + "…"
	}
	return s
}

// stateText is the card's sentence for an agent state.
func stateText(s model.AgentState) string {
	switch s {
	case model.StateAwaitingInput:
		return "Waiting for input"
	case model.StatePendingApproval:
		return "Asking for approval"
	case model.StateWorking:
		return "Working"
	case model.StateConnecting:
		return "Connecting"
	case model.StatePlanReady:
		return "Plan ready"
	case model.StateCompleted:
		return "Finished"
	case model.StateError:
		return "Error"
	case model.StateTerminalRunning:
		return "Running"
	}
	return string(s)
}

// cardWidth is the hover card's width.
const cardWidth unit.Dp = 300

// drawCard draws c with its top left at the origin, fading in by alpha,
// and returns its size.
func drawCard(gtx layout.Context, th *theme.Theme, c card, alpha float32) image.Point {
	w := gtx.Dp(cardWidth)
	padX, padY, gap := gtx.Dp(10), gtx.Dp(8), gtx.Dp(6)
	icon, iconGap := gtx.Dp(14), gtx.Dp(8)
	textX := padX + icon + iconGap
	textW := w - textX - padX
	bg := th.SurfaceElevated
	muted := theme.Mix(bg, th.Muted, 0.9)

	// Lay the lines out first to learn the height, then paint the surface
	// under them.
	m := op.Record(gtx.Ops)
	y := padY
	line := func(ic layout.Widget, body func(gtx layout.Context) layout.Dimensions) {
		g := gtx
		g.Constraints = layout.Constraints{Max: image.Pt(textW, 1<<16)}
		o := op.Offset(image.Pt(textX, y)).Push(gtx.Ops)
		d := body(g)
		o.Pop()
		lh := max(d.Size.Y, icon)
		if ic != nil {
			// The icon centers on the first line of text.
			first := min(lh, gtx.Sp(13*1.5))
			o := op.Offset(image.Pt(padX, y+(first-icon)/2)).Push(gtx.Ops)
			ig := gtx
			ig.Constraints = layout.Exact(image.Pt(icon, icon))
			ic(ig)
			o.Pop()
		}
		y += lh + gap
	}
	iconOf := func(d string, col color.NRGBA) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions { return drawIcon(gtx, d, icon, col, 0) }
	}
	for _, k := range c.lines() {
		switch k {
		case 't':
			mark := func(gtx layout.Context) layout.Dimensions {
				if c.agent != "" {
					return AgentMark(gtx, c.agent, icon, th.Fg)
				}
				return drawIcon(gtx, icTerminal, icon, th.Muted, 0)
			}
			line(mark, func(gtx layout.Context) layout.Dimensions {
				tw := gtx.Constraints.Max.X
				if c.when != "" {
					tm := op.Record(gtx.Ops)
					d := label(gtx, th, th.UIFont, 11, muted, c.when)
					call := tm.Stop()
					o := op.Offset(image.Pt(tw-d.Size.X, (gtx.Sp(13*1.5)-d.Size.Y)/2)).Push(gtx.Ops)
					call.Add(gtx.Ops)
					o.Pop()
					tw -= d.Size.X + gtx.Dp(10)
				}
				gtx.Constraints.Max.X = tw
				return wrapLabel(gtx, th, semibold(th.UIFont), 13, th.Fg, c.title, 3)
			})
		case 'f':
			col := muted
			if c.group != "" {
				col = th.ProjectColor(c.groupColor)
			}
			line(iconOf(icFolder, col), func(gtx layout.Context) layout.Dimensions {
				// The group's name, then the path under it, broken anywhere
				// over two lines at most.
				var rows []layout.FlexChild
				if c.group != "" {
					rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return hrowFit(gtx, gtx.Sp(13*1.5), 0, item{w: func(gtx layout.Context) layout.Dimensions {
							return label(gtx, th, medium(th.UIFont), 12, th.Fg, c.group)
						}})
					}))
				}
				if c.path != "" {
					rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if c.group == "" {
							gtx.Constraints.Min.Y = gtx.Sp(13 * 1.5) // level with the icon
						}
						return layout.W.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							gtx.Constraints.Min = image.Point{}
							return widget.Label{MaxLines: 2, WrapPolicy: text.WrapGraphemes}.Layout(gtx, th.Shaper, th.MonoFont, 11, c.path, material(gtx, muted))
						})
					}))
				}
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
			})
		case 'b':
			line(iconOf(icGitBranch, th.Purple), func(gtx layout.Context) layout.Dimensions {
				items := []item{{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
					return label(gtx, th, th.MonoFont, 11, theme.Mix(bg, th.Fg, 0.85), c.branch)
				}}}
				if c.add > 0 || c.del > 0 {
					items = append(items, item{w: func(gtx layout.Context) layout.Dimensions {
						return label(gtx, th, semibold(th.UIFont), 11, th.Green, fmt.Sprintf("+%d", c.add))
					}}, item{w: func(gtx layout.Context) layout.Dimensions {
						return label(gtx, th, semibold(th.UIFont), 11, th.Red, fmt.Sprintf("-%d", c.del))
					}})
				}
				return hrowFit(gtx, gtx.Sp(13*1.5), gtx.Dp(6), items...)
			})
		case 'o':
			line(iconOf(icPlug, muted), func(gtx layout.Context) layout.Dimensions {
				return hrowFit(gtx, gtx.Sp(13*1.5), gtx.Dp(6), item{w: func(gtx layout.Context) layout.Dimensions {
					return label(gtx, th, th.UIFont, 12, muted, "Ports")
				}}, item{w: func(gtx layout.Context) layout.Dimensions {
					return label(gtx, th, th.MonoFont, 11, theme.Mix(bg, th.Fg, 0.85), c.ports)
				}})
			})
		case 's':
			col := StateColor(th, c.state)
			dot := func(gtx layout.Context) layout.Dimensions {
				d := gtx.Dp(6)
				o := op.Offset(image.Pt((icon-d)/2, (icon-d)/2)).Push(gtx.Ops)
				paint.FillShape(gtx.Ops, col, clip.Ellipse{Max: image.Pt(d, d)}.Op(gtx.Ops))
				o.Pop()
				return layout.Dimensions{Size: image.Pt(icon, icon)}
			}
			line(dot, func(gtx layout.Context) layout.Dimensions {
				return hrowFit(gtx, gtx.Sp(13*1.5), 0, item{w: func(gtx layout.Context) layout.Dimensions {
					return label(gtx, th, semibold(th.UIFont), 12, col, stateText(c.state))
				}})
			})
		case 'd':
			y -= gap / 2 // the detail belongs to the state above it
			line(nil, func(gtx layout.Context) layout.Dimensions {
				return detailLabel(gtx, th, theme.Mix(bg, th.Fg, 0.8), c.detail)
			})
		case 'u':
			line(iconOf(icGauge, muted), func(gtx layout.Context) layout.Dimensions {
				return hrowFit(gtx, gtx.Sp(13*1.5), 0, item{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
					return label(gtx, th, th.UIFont, 12, theme.Mix(bg, th.Fg, 0.8), c.usage)
				}})
			})
		case 'j':
			line(iconOf(icCircleCheck, muted), func(gtx layout.Context) layout.Dimensions {
				return wrapLabel(gtx, th, th.UIFont, 12, muted, c.decision, 2)
			})
		case 'p':
			line(iconOf(projectIcon("layers"), muted), func(gtx layout.Context) layout.Dimensions {
				return hrowFit(gtx, gtx.Sp(13*1.5), 0, item{w: func(gtx layout.Context) layout.Dimensions {
					return label(gtx, th, th.UIFont, 12, muted, fmt.Sprintf("%d panes", c.panes))
				}})
			})
		}
	}
	body := m.Stop()
	size := image.Pt(w, y-gap+padY)

	fade := paint.PushOpacity(gtx.Ops, alpha)
	rect := image.Rectangle{Max: size}
	r := gtx.Dp(8)
	shadow := rect.Add(image.Pt(0, gtx.Dp(3))).Inset(-gtx.Dp(2))
	paint.FillShape(gtx.Ops, color.NRGBA{A: 60}, clip.UniformRRect(shadow, r+gtx.Dp(2)).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, theme.Mix(th.Border, th.Fg, 0.08), clip.UniformRRect(rect.Inset(-1), r+1).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, bg, clip.UniformRRect(rect, r).Op(gtx.Ops))
	body.Add(gtx.Ops)
	fade.Pop()
	return size
}

// wrapLabel draws txt wrapped to at most lines lines, the last one cut with
// an ellipsis.
func wrapLabel(gtx layout.Context, th *theme.Theme, f font.Font, size unit.Sp, c color.NRGBA, txt string, lines int) layout.Dimensions {
	gtx.Constraints.Min = image.Point{}
	return widget.Label{MaxLines: lines, WrapPolicy: text.WrapHeuristically}.Layout(gtx, th.Shaper, f, size, txt, material(gtx, c))
}

// detailLabel is an agent's question or detail in the card: 12sp, at most
// three lines.
func detailLabel(gtx layout.Context, th *theme.Theme, c color.NRGBA, txt string) layout.Dimensions {
	return wrapLabel(gtx, th, th.UIFont, 12, c, txt, 3)
}

// drawHover draws the open card beside the sidebar of width w, its top
// level with the hovered row (cardY) and kept inside the height h.
func (s *Sidebar) drawHover(gtx layout.Context, v *view, w, h int) {
	id := s.hover.shown
	if id == "" || s.cardAt != id {
		return
	}
	var ws *model.Workspace
	for i := range v.st.Workspaces {
		if v.st.Workspaces[i].ID == id {
			ws = &v.st.Workspaces[i]
		}
	}
	if ws == nil {
		return
	}
	alpha := min(1, float32(gtx.Now.Sub(s.hover.shownAt))/float32(hoverFade))
	if alpha < 1 {
		gtx.Execute(op.InvalidateCmd{})
	}
	alpha = 1 - (1-alpha)*(1-alpha)
	// Record once to learn the height, then place it.
	m := op.Record(gtx.Ops)
	c := cardFor(v, *ws)
	if s.Usage != nil {
		if u := s.Usage(ws.ID); u != nil {
			c.usage = UsageText(*u, s.ShowCost)
		}
	}
	size := drawCard(gtx, v.th, c, alpha)
	call := m.Stop()
	margin := gtx.Dp(8)
	y := min(s.cardY, h-size.Y-margin)
	y = max(y, margin)
	defer op.Offset(image.Pt(w+gtx.Dp(6), y)).Push(gtx.Ops).Pop()
	op.Defer(gtx.Ops, call)
}
