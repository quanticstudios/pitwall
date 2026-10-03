package sidebar

import (
	"image"
	"image/color"
	"math"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/model"
)

// Agent accents: Claude's brand orange, and a soft violet for Codex that no
// state color uses.
var (
	claudeColor = color.NRGBA{R: 0xd9, G: 0x77, B: 0x57, A: 0xff}
	codexColor  = color.NRGBA{R: 0x9b, G: 0x8c, B: 0xff, A: 0xff}
)

func isAgent(p model.Provider) bool { return p == model.ProviderClaude || p == model.ProviderCodex }

// AgentOf is the agent running in tab ws, idle or busy: the one with the
// tab's highest-priority agent activity, else the first pane's agent, else
// "".
func AgentOf(st *model.State, ws model.Workspace) model.Provider {
	var acts []model.Activity
	for _, a := range st.Activities {
		if a.WorkspaceID == ws.ID && isAgent(a.Provider) {
			acts = append(acts, a)
		}
	}
	if a := model.Aggregate(acts); a != nil {
		return a.Provider
	}
	for _, p := range st.Panes {
		if p.WorkspaceID == ws.ID && isAgent(p.Provider) {
			return p.Provider
		}
	}
	return ""
}

// AgentName is how a row's second line names the agent.
func AgentName(p model.Provider) string {
	switch p {
	case model.ProviderClaude:
		return "Claude"
	case model.ProviderCodex:
		return "Codex"
	}
	return ""
}

// AgentColor is the agent's accent.
func AgentColor(p model.Provider) color.NRGBA {
	if p == model.ProviderCodex {
		return codexColor
	}
	return claudeColor
}

// AgentMark draws the agent's mark in a size box: Claude's sparkle, a
// starburst of alternating long and short rays; Codex a rounded square
// with ">_" cut out of it. bg is what the cut-out shows.
func AgentMark(gtx layout.Context, p model.Provider, size int, bg color.NRGBA) layout.Dimensions {
	s := float32(size)
	if p == model.ProviderCodex {
		r := image.Rectangle{Max: image.Pt(size, size)}
		paint.FillShape(gtx.Ops, codexColor, clip.UniformRRect(r, size/4).Op(gtx.Ops))
		inset := op.Offset(image.Pt(size/8, size/8)).Push(gtx.Ops)
		drawIcon(gtx, icTerminal, size*3/4, bg, 0)
		inset.Pop()
		return layout.Dimensions{Size: image.Pt(size, size)}
	}
	c := f32.Pt(s/2, s/2)
	const rays = 10
	var path clip.Path
	path.Begin(gtx.Ops)
	for i := range rays {
		a := float64(i)*2*math.Pi/rays - math.Pi/2
		long := s / 2
		if i%2 == 1 {
			long = s * 0.38
		}
		dir := f32.Pt(float32(math.Cos(a)), float32(math.Sin(a)))
		n := f32.Pt(-dir.Y, dir.X)
		base, w := float32(0), s*0.08
		path.MoveTo(c.Add(dir.Mul(base)).Add(n.Mul(w)))
		path.LineTo(c.Add(dir.Mul(long)).Add(n.Mul(w * 0.35)))
		path.LineTo(c.Add(dir.Mul(long)).Sub(n.Mul(w * 0.35)))
		path.LineTo(c.Add(dir.Mul(base)).Sub(n.Mul(w)))
		path.Close()
	}
	paint.FillShape(gtx.Ops, claudeColor, clip.Outline{Path: path.End()}.Op())
	return layout.Dimensions{Size: image.Pt(size, size)}
}

// TabMark draws what marks tab ws in lists outside the sidebar: its agent's
// mark, else a terminal glyph in col.
func TabMark(gtx layout.Context, st *model.State, ws model.Workspace, size int, col, bg color.NRGBA) layout.Dimensions {
	if p := AgentOf(st, ws); p != "" {
		return AgentMark(gtx, p, size, bg)
	}
	return drawIcon(gtx, icTerminal, size, col, 0)
}
