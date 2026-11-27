package panel

import (
	"strings"

	"gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/session"
)

// isSummary is whether it is the server's summary of Claude's words
// between tool calls, which comes as a thinking block and Claude Code
// shows as a line tagged summary: a row of its own, not a thinking row.
func isSummary(it *session.Item) bool {
	return it.Kind == session.KindReasoningText && it.Phase == session.PhaseSummary
}

// proseRow is item i between a turn's folds, where it came among the calls:
// a summary as quiet prose, the reply's type in a muted color, wrapped at
// the reply's width; the assistant's text as its reply is; a system line
// as it draws outside a work log.
func (d *cdraw) proseRow(gtx layout.Context, i int) layout.Dimensions {
	it := &d.s.Content[i]
	switch {
	case it.Kind == session.KindAssistantText:
		return d.between(gtx, it)
	case it.Kind == session.KindSystem:
		return d.sysItem(gtx, it, false)
	}
	txt := strings.TrimSpace(it.Text)
	if txt == "" {
		return layout.Dimensions{}
	}
	st := d.prose()
	st.c = d.th.Muted
	gtx.Constraints.Max.X = min(d.proseW, gtx.Constraints.Max.X)
	return d.selectable(gtx, d.t.sel(it.ID), st, txt)
}
