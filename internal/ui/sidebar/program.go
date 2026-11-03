package sidebar

import (
	"image"
	"image/color"
	"slices"
	"strconv"
	"strings"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/progstatus"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// A program's status (OSC 7501 or pitwall status) gets a line of its own
// under a row's line 2: the program's name, its message, and a progress
// bar when it reports progress. Where no agent owns the pane, the daemon
// also makes it the pane's activity, so the row's glyph and pill show its
// state; under an agent, the agent's state stays on line 1 and the line
// shows what the program does.

// tabProgram is the program report tab ws's row shows: its panes' most
// urgent, nil for none. The panes in subs have sub-rows, which show their
// own.
func tabProgram(st *model.State, ws model.Workspace, subs []model.AgentPane) *model.Program {
	var best *model.Program
	for i := range st.Panes {
		p := st.Panes[i].Program
		if p == nil || st.Panes[i].WorkspaceID != ws.ID || slices.ContainsFunc(subs, func(ap model.AgentPane) bool { return ap.Pane.ID == st.Panes[i].ID }) || agentReport(st, st.Panes[i]) {
			continue
		}
		if best == nil || progstatus.Urgency(progstatus.State(p.State)) > progstatus.Urgency(progstatus.State(best.State)) {
			best = p
		}
	}
	return best
}

// agentReport reports whether pane's program report comes from the pane's
// own agent, as Codex sends one naming itself: the agent's mark and pill
// already show that, so no row repeats it as a program.
func agentReport(st *model.State, pane model.Pane) bool {
	if pane.Program == nil || pane.Program.App == "" {
		return false
	}
	is := func(p model.Provider) bool {
		return model.IsAgent(p) && (strings.EqualFold(pane.Program.App, string(p)) || strings.EqualFold(pane.Program.App, AgentName(p)))
	}
	if is(pane.Provider) {
		return true
	}
	return slices.ContainsFunc(st.Activities, func(a model.Activity) bool { return a.PaneID == pane.ID && is(a.Provider) })
}

// linesHeight is what tab id's program and status lines add to its row.
func (v *view) linesHeight(gtx layout.Context, id string) int {
	h := statusHeight(gtx, v.th, v.status[id])
	if v.program[id] != nil {
		_, l2 := rowLines(gtx, v.th)
		h += gtx.Dp(statusGap) + l2
	}
	return h
}

// ProgramState is the state a program line names, "Working" or "Error",
// from the same labels as the pill.
func ProgramState(p model.Program) string {
	return model.PillLabel(model.Activity{State: p.Activity()})
}

// ProgramChip is a program in a few words, for a sub-row: its name, else
// its state, then its progress, "cargo 40%".
func ProgramChip(p model.Program) string {
	s := p.App
	if s == "" {
		s = ProgramState(p)
	}
	if p.Progress >= 0 && p.Progress <= 100 {
		s += " " + strconv.Itoa(p.Progress) + "%"
	}
	return s
}

// programLine draws p in a row's line: its name in the state's color, its
// message (else its state) muted, and its progress at the right.
func programLine(gtx layout.Context, th *theme.Theme, p model.Program, base, muted color.NRGBA, h int) layout.Dimensions {
	col := th.Readable(StateColor(th, p.Activity()), base)
	var items []item
	if p.App != "" {
		items = append(items, item{w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, semibold(th.UIFont), th.Sp(theme.Caption), col, p.App)
		}})
	}
	text := p.Message
	if text == "" {
		text = ProgramState(p)
	}
	items = append(items, item{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
		return label(gtx, th, th.UIFont, th.Sp(theme.Caption), muted, text)
	}})
	if p.Progress >= 0 && p.Progress <= 100 {
		items = append(items, item{right: true, w: func(gtx layout.Context) layout.Dimensions {
			return progressBar(gtx, th, p, base, h)
		}}, item{w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, medium(th.UIFont), th.Sp(theme.Caption), muted, strconv.Itoa(p.Progress)+"%")
		}})
	}
	return hrow(gtx, h, gtx.Dp(6), items...)
}

// progressBar is the limit meter's bar, 36dp wide, filled in the state's
// color.
func progressBar(gtx layout.Context, th *theme.Theme, p model.Program, base color.NRGBA, h int) layout.Dimensions {
	bw, bh := gtx.Dp(36), gtx.Dp(4)
	y := (h - bh) / 2
	paint.FillShape(gtx.Ops, theme.Mix(base, th.Fg, 0.12), clip.UniformRRect(image.Rect(0, y, bw, y+bh), bh/2).Op(gtx.Ops))
	if fw := bw * p.Progress / 100; fw > 0 {
		paint.FillShape(gtx.Ops, StateColor(th, p.Activity()), clip.UniformRRect(image.Rect(0, y, max(fw, bh), y+bh), bh/2).Op(gtx.Ops))
	}
	return layout.Dimensions{Size: image.Pt(bw, h)}
}
