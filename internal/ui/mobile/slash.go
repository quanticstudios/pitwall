package mobile

import (
	"image"

	"gioui.org/io/event"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/chatin"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// slashMenu is the / menu over the chat: the window's own SlashMenu,
// which finds the /word at the caret, ranks the chat's commands and puts
// a pick in, drawn here in rows a thumb can tap. Back closes it until the
// draft changes.
type slashMenu struct {
	chatin.SlashMenu
	cmds []session.Command // the chat's commands; nil until a "commands" event, which an older daemon never sends
	tag  int               // the menu's own press target
}

// slashRowH is a row's height: a name over its description, a thumb's
// target. slashMax rows show at most, best first.
const (
	slashRowH = 56
	slashMax  = 6
)

// slash reads the menu's rows from the draft and draws them above the
// composer, whose top is at top. The composer ran its Events before the
// field took its keys.
func (c *chatView) slash(gtx C, th *theme.Theme, env chatEnv, top int) {
	k, s := &c.cmp, &c.slashes
	p := model.Provider(env.Tab.Agent)
	cmds := s.cmds
	if p == model.ProviderCodex {
		// why: the phone keeps Codex to the draft's start; its $skill mid-draft is the window's for now
		if start, _, ok := chatin.Slash(k.ed.Text(), caretOf(&k.ed)); !ok || start > 0 {
			cmds = nil
		}
	}
	s.Update(gtx, &k.ed, cmds, p)
	rows := s.Rows()
	if len(rows) == 0 || k.menu != "" || k.sending || !env.Online || !env.Open || !env.Tab.Send {
		return
	}
	margin, pad := gtx.Dp(10), gtx.Dp(6)
	n := min(len(rows), slashMax, max(1, (top-2*margin-2*pad)/gtx.Dp(slashRowH)))
	first := min(max(0, s.Selected()-n+1), len(rows)-n) // the highlighted row stays in view
	w := gtx.Constraints.Max.X - 2*margin
	h := n*gtx.Dp(slashRowH) + 2*pad
	defer op.Offset(image.Pt(margin, top-h-gtx.Dp(6))).Push(gtx.Ops).Pop()
	rect := image.Rectangle{Max: image.Pt(w, h)}
	kit.Surface(gtx, rect, gtx.Dp(11), kit.Floating, th.BorderStrong, th.SurfaceElevated)
	cl := clip.UniformRRect(rect, gtx.Dp(11)).Push(gtx.Ops)
	event.Op(gtx.Ops, &s.tag) // why: a tap between rows goes no further
	cl.Pop()
	g := gtx
	g.Constraints = layout.Exact(image.Pt(w-2*pad, gtx.Dp(slashRowH)))
	for i, cmd := range rows[first : first+n] {
		o := op.Offset(image.Pt(pad, pad+i*gtx.Dp(slashRowH))).Push(gtx.Ops)
		slashRow(g, th, s.Button(cmd.Name), s.Mark(cmd)+cmd.Name, cmd, first+i == s.Selected())
		o.Pop()
	}
}

func caretOf(ed *widget.Editor) int {
	_, caret := ed.Selection()
	return caret
}

// slashRow is one command: name, as /name or $name, in mono with its argument hint muted,
// its description muted under it, and where it comes from, or that it
// opens the agent's terminal, at the right. The first row, the best
// match, is highlighted.
func slashRow(gtx C, th *theme.Theme, b *widget.Clickable, name string, cmd session.Command, on bool) D {
	return b.Layout(gtx, func(gtx C) D {
		semantic.LabelOp(name).Add(gtx.Ops)
		size := gtx.Constraints.Min
		fill := theme.Transparent
		switch {
		case b.Pressed():
			fill = theme.Mix(th.SurfaceElevated, th.Fg, 0.08)
		case on:
			fill = theme.Mix(th.SurfaceElevated, th.Primary, 0.15)
		}
		paint.FillShape(gtx.Ops, fill, clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(8)).Op(gtx.Ops))
		layout.Inset{Left: 12, Right: 12}.Layout(gtx, func(gtx C) D {
			return midFlex(gtx,
				layout.Flexed(1, func(gtx C) D {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							gtx.Constraints.Min.X = 0
							if cmd.Hint != "" {
								return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
									layout.Rigid(func(gtx C) D { return label(gtx, th, th.MonoFont, 15.5, th.Fg, 1, name) }),
									layout.Rigid(layout.Spacer{Width: 6}.Layout),
									layout.Flexed(1, func(gtx C) D { return label(gtx, th, th.MonoFont, 13.5, th.Muted, 1, cmd.Hint) }),
								)
							}
							return label(gtx, th, th.MonoFont, 15.5, th.Fg, 1, name)
						}),
						layout.Rigid(func(gtx C) D {
							if cmd.Description == "" {
								return D{}
							}
							return label(gtx, th, th.UIFont, 13.5, th.Muted, 1, cmd.Description)
						}),
					)
				}),
				layout.Rigid(func(gtx C) D {
					note := chatin.RowNote(cmd)
					if note == "" {
						return D{}
					}
					return layout.Inset{Left: 10}.Layout(gtx, func(gtx C) D { return label(gtx, th, th.UIFont, 13, th.Muted, 1, note) })
				}),
			)
		})
		return D{Size: size}
	})
}
