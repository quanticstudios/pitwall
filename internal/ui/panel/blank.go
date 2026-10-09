package panel

import (
	"image/color"

	"gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
)

// blank is what a view shows instead of its rows: a loading line, or an
// empty state's icon and line.
type blank struct {
	wait bool   // still reading; text has a pulsing dot
	icon string // a sidebar.Icon name
	text string
}

// baseName is the branch Changes compares against.
func (in *Input) baseName() string {
	if b := gitstat.BranchName(in.Base); b != "" {
		return b
	}
	return "base"
}

// blankFor is what view v shows instead of its rows, nil when it has
// rows. Flow draws the agent's banner over a blank when there is an
// agent.
func blankFor(in *Input, v View) *blank {
	if v == ViewChanges {
		switch {
		case in.Git && len(in.Files) == 0:
			return &blank{icon: "check", text: "No changes from " + in.baseName() + "."}
		case in.Git:
			return nil
		case in.WaitGit:
			return &blank{wait: true, text: "Reading git…"}
		}
		return &blank{icon: "git-branch", text: "Not in a git repository."}
	}
	ag := in.agent()
	name := sidebar.AgentName(ag)
	switch {
	case in.Pane == nil:
		return &blank{icon: "terminal", text: "No pane is focused."}
	case ag == "":
		return &blank{icon: "bot", text: "No agent in this pane. Start Claude Code, Codex, Gemini CLI or pi here and the panel follows it."}
	case !flow.Reads(ag):
		return &blank{icon: "book-open", text: name + " keeps no session file pitwall reads. Changes shows this folder's work."}
	case in.Feed == nil && in.WaitFeed:
		return &blank{wait: true, text: "Reading the session file…"}
	case in.Feed == nil:
		return &blank{icon: "book-open", text: "No session file yet. The panel reads it once a hook names it, usually at the next prompt or tool call."}
	}
	switch v {
	case ViewSubagents:
		if len(in.Feed.Subagents) == 0 {
			return &blank{icon: "bot", text: "No subagents yet. " + name + " starts them for side tasks."}
		}
	case ViewPlan:
		if len(in.Feed.Plan) == 0 {
			return &blank{icon: "layers", text: "No plan yet. " + name + " writes one for longer work."}
		}
	case ViewTimeline:
		if len(timeline(in)) == 0 {
			return &blank{icon: "zap", text: "Nothing has happened in this session yet."}
		}
	}
	return nil
}

// blank draws b in the panel's width.
func (d *drawer) blank(b *blank) layout.Widget {
	return pad(18, 0, 0, 0, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		if b.wait {
			d.p.live = true
			return kit.Loading(gtx, d.th, b.text)
		}
		icon := func(gtx layout.Context, size int, col color.NRGBA) layout.Dimensions {
			return sidebar.Icon(gtx, b.icon, size, col)
		}
		return kit.Empty(gtx, d.th, icon, b.text, nil)
	})
}
