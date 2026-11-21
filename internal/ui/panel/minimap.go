package panel

import (
	"encoding/json"
	"image"
	"image/color"
	"strings"

	gioevent "gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// The minimap is a thin strip at the transcript's right edge with one mark
// per prompt, error and question, evenly spaced as T3 Code spaces them; a
// hover names the mark and a click scrolls to its row. Adapted from T3
// Code (MIT): apps/web/src/components/chat/TimelineMinimap.tsx and
// MessagesTimeline.logic.ts's resolveTimelineMinimap* helpers.
const (
	mmSpacing = theme.SpaceS                  // between marks at their natural height
	mmGutter  = 2 * theme.SpaceXL             // the side gutter it needs, else it hides
	mmInset   = theme.SpaceM                  // from the transcript's right edge
	mmMarkW   = theme.SpaceXL                 // a mark's width at its widest
	mmMarkH   = theme.Space2XS                // a mark's height
	mmHitW    = theme.Space2XL + theme.SpaceS // the hit strip's widest
	mmPreview = 20 * theme.SpaceL             // the preview's widest
	mmMin     = 2                             // fewer marks draw none
)

// markKind is what a minimap mark stands for, which sets its color.
type markKind uint8

const (
	markPrompt markKind = iota
	markError
	markQuestion
)

// mmMark is one mark: the row it jumps to, what it is, and its line.
type mmMark struct {
	row  int // the row in t.rows; -1 for the end, a pending request
	kind markKind
	text string
}

// minimap is the minimap's state.
type minimap struct {
	tag    struct{}
	hover  int // the mark under the pointer, -1 for none
	hoverY int // the pointer's y in the strip
	in     bool
}

// minimapMarks are rows' marks: each prompt, each error, each question
// asked, then a pending request or the chat's error at the end.
func minimapMarks(rows []trow, s *session.Snapshot) []mmMark {
	var out []mmMark
	for k := range rows {
		r := &rows[k]
		if r.tail != nil || r.res != nil {
			continue
		}
		switch {
		case r.seg == segBlock && r.b.kind == blockPrompt:
			out = append(out, mmMark{k, markPrompt, firstLine(strings.TrimSpace(s.Content[r.b.lo].Text))})
		case r.seg == segBlock && r.b.kind == blockSystem, r.seg == segProse:
			i := r.b.lo
			if r.seg == segProse {
				i = r.i
			}
			if it := &s.Content[i]; it.Kind == session.KindSystem && it.Phase == session.PhaseError {
				out = append(out, mmMark{k, markError, firstLine(strings.TrimSpace(it.Text))})
			}
		case r.seg == segItem:
			if c := s.Content[r.i].Tool; c != nil && c.ToolName == "AskUserQuestion" {
				out = append(out, mmMark{k, markQuestion, questionLine(c)})
			}
		}
	}
	switch {
	case len(s.PendingUserInputs) > 0:
		q := "Question"
		if in := s.PendingUserInputs[0]; len(in.Questions) > 0 {
			q = in.Questions[0].Prompt
		}
		out = append(out, mmMark{-1, markQuestion, firstLine(q)})
	case len(s.PendingApprovals) > 0:
		out = append(out, mmMark{-1, markQuestion, "Approval: " + firstLine(deref(s.PendingApprovals[0].Detail))})
	}
	if msg, stopped := endLine(s); msg != "" && !stopped {
		out = append(out, mmMark{-1, markError, firstLine(msg)})
	}
	return out
}

// questionLine is an AskUserQuestion call's first question.
func questionLine(c *session.ToolCall) string {
	var in struct {
		Questions []struct {
			Question string `json:"question"`
		} `json:"questions"`
	}
	if json.Unmarshal(c.Input, &in) == nil && len(in.Questions) > 0 && in.Questions[0].Question != "" {
		return firstLine(in.Questions[0].Question)
	}
	return "Question"
}

// markLabel is a mark's hover text, as T3 Code's "Jump to message".
func markLabel(m mmMark) string {
	what := map[markKind]string{markPrompt: "message", markError: "error", markQuestion: "question"}[m.kind]
	if m.text == "" {
		return "Jump to " + what
	}
	return "Jump to " + what + ": " + m.text
}

// mmIndex is the mark nearest y in a rail of n marks h tall, as T3
// Code's resolveTimelineMinimapIndexFromPointer.
func mmIndex(n, h, y int) int {
	if n <= 1 || h <= 0 {
		return 0
	}
	return min(n-1, max(0, (y*(n-1)+h/2)/h))
}

// drawMinimap draws the minimap over the transcript of size, whose
// column is col wide, and takes its hovers and clicks. It hides in a
// chat whose side gutter is narrower than mmGutter, and with fewer than
// mmMin marks.
func (t *Transcript) drawMinimap(gtx layout.Context, d *cdraw, s *session.Snapshot, col int, size image.Point) {
	m := &t.mm
	gutter := (size.X - col) / 2
	marks := minimapMarks(t.rows, s)
	n := len(marks)
	if gutter < gtx.Dp(mmGutter) || n < mmMin {
		m.hover, m.in = -1, false
		return
	}
	hitW := min(gtx.Dp(mmHitW), gutter-gtx.Dp(mmInset))
	natural := (n - 1) * gtx.Dp(mmSpacing)
	h := max(1, min(natural, size.Y-2*gtx.Dp(theme.Space2XL)-gtx.Dp(theme.SpaceXL)))
	top := (size.Y - h) / 2
	strip := image.Rect(size.X-gtx.Dp(mmInset)-hitW, top-gtx.Dp(theme.SpaceS), size.X-gtx.Dp(mmInset), top+h+gtx.Dp(theme.SpaceS))

	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &m.tag, Kinds: pointer.Enter | pointer.Move | pointer.Leave | pointer.Press | pointer.Cancel})
		if !ok {
			break
		}
		e, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		switch e.Kind {
		case pointer.Leave, pointer.Cancel:
			m.in = false
		case pointer.Enter, pointer.Move:
			m.in, m.hoverY = true, int(e.Position.Y)
		case pointer.Press:
			if e.Buttons != pointer.ButtonPrimary {
				break
			}
			k := mmIndex(n, h, int(e.Position.Y)-top) // the position is in the transcript's space
			t.jumpTo(gtx, marks[k].row)
		}
	}
	m.hover = -1
	if m.in {
		m.hover = mmIndex(n, h, m.hoverY-top)
	}

	th := d.th
	first, last := t.list.Position.First-1, t.list.Position.First-1+t.list.Position.Count
	x1 := strip.Max.X
	for i, mk := range marks {
		y := top
		if n > 1 {
			y += i * h / (n - 1)
		}
		// T3 Code's fisheye around the hovered mark, without its easing.
		w := gtx.Dp(mmMarkW) / 3
		if m.hover >= 0 {
			switch dist := max(i-m.hover, m.hover-i); dist {
			case 0:
				w = gtx.Dp(mmMarkW)
			case 1:
				w = gtx.Dp(mmMarkW) * 2 / 3
			case 2:
				w = gtx.Dp(mmMarkW) * 5 / 12
			}
		}
		inView := mk.row >= first && mk.row < last || mk.row < 0 && !t.list.Position.BeforeEnd
		paint.FillShape(gtx.Ops, markColor(th, mk.kind, i == m.hover, inView),
			clip.UniformRRect(image.Rect(x1-w, y-gtx.Dp(mmMarkH)/2, x1, y-gtx.Dp(mmMarkH)/2+gtx.Dp(mmMarkH)), gtx.Dp(1)).Op(gtx.Ops))
	}
	// The wheel passes through to the list, as T3 Code forwards it.
	pass := pointer.PassOp{}.Push(gtx.Ops)
	area := clip.Rect(strip).Push(gtx.Ops)
	gioevent.Op(gtx.Ops, &m.tag)
	pointer.CursorPointer.Add(gtx.Ops)
	area.Pop()
	pass.Pop()

	if m.hover < 0 {
		return
	}
	y := top
	if n > 1 {
		y += m.hover * h / (n - 1)
	}
	t.mmPreview(gtx, d, markLabel(marks[m.hover]), image.Rect(strip.Min.X, y, strip.Max.X, y), size)
}

// markColor is a mark's: a prompt's in the muted text color, an error's
// red, a question's the needs-you yellow; brighter hovered or in view.
func markColor(th *theme.Theme, k markKind, hover, inView bool) color.NRGBA {
	c, a := th.Muted, float32(0.35)
	switch k {
	case markError:
		c, a = th.Readable(th.Red, th.Bg), 0.7
	case markQuestion:
		c, a = th.Readable(th.Yellow, th.Bg), 0.7
	}
	switch {
	case hover:
		a = 1
	case inView:
		a = min(1, a+0.35)
	}
	return theme.Mix(th.Bg, c, a)
}

// mmPreview is the hovered mark's line in a popover left of the strip,
// centered on its mark at anchor's y, kept inside size.
func (t *Transcript) mmPreview(gtx layout.Context, d *cdraw, txt string, anchor image.Rectangle, size image.Point) {
	th := d.th
	px, py := gtx.Dp(theme.SpaceM), gtx.Dp(theme.SpaceS)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(gtx.Dp(mmPreview)-2*px, gtx.Dp(theme.Space2XL))}
	call, td := measure(g, func(gtx layout.Context) layout.Dimensions {
		return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Small), th.Fg, txt)
	})
	box := image.Pt(td.Size.X+2*px, td.Size.Y+2*py)
	p := image.Pt(anchor.Min.X-gtx.Dp(theme.SpaceS)-box.X, anchor.Min.Y-box.Y/2)
	p.X = max(gtx.Dp(theme.SpaceS), p.X)
	p.Y = max(gtx.Dp(theme.SpaceS), min(p.Y, size.Y-box.Y-gtx.Dp(theme.SpaceS)))
	kit.Surface(gtx, image.Rectangle{Min: p, Max: p.Add(box)}, gtx.Dp(theme.RadiusPopover), kit.Floating, th.BorderStrong, th.SurfaceElevated)
	at(gtx, p.Add(image.Pt(px, py)), func(gtx layout.Context) layout.Dimensions { call.Add(gtx.Ops); return td })
}

// jumpTo scrolls row k to the top of the view, with room above it; k -1
// goes to the end and follows it again.
func (t *Transcript) jumpTo(gtx layout.Context, k int) {
	if k < 0 || k >= len(t.rows) {
		t.ToEnd()
	} else {
		t.list.Position.First, t.list.Position.Offset, t.list.Position.BeforeEnd = k+1, -gtx.Dp(theme.SpaceL), true
	}
	gtx.Execute(op.InvalidateCmd{})
}
