package panel

import (
	"image"
	"strconv"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// transcriptWidth is the widest the transcript's column grows.
const transcriptWidth unit.Dp = 760

// outputLines is how much of a tool's output or diff an opened call shows,
// from its end.
const outputLines = 60

// Transcript draws a chat's transcript in the main area with the panel's
// rows: prompts, assistant text, one line per tool call that a click
// opens, reasoning folded, and what the chat waits on: an approval with
// Allow, Allow for session and Deny, a question with the pitwall chat
// command that answers it. Only the items in view are laid out, so a
// 2,000-item chat costs a frame what a short one does.
type Transcript struct {
	chat  string // the chat drawn; another one starts afresh
	list  layout.List
	open  map[string]bool              // item ids unfolded
	btn   map[string]*widget.Clickable // per item that folds, made when first drawn
	epoch time.Time
	laid  int // items laid out in the last frame

	answer  map[string]*[3]widget.Clickable // per pending approval, one per decisions entry
	sent    map[string]bool                 // approvals answered here that the chat has not resolved yet
	answers []Answer                        // not taken by Answers yet
	aideBtn widget.Clickable
	aide    bool // Open in aide was clicked, until OpenAide takes it
}

// Answer is an approval answered with its buttons.
type Answer struct {
	Request  string
	Decision string // allow, allow-session, deny
}

// decisions are what the approval buttons answer, in their order.
var decisions = [3]string{"allow", "allow-session", "deny"}

// Answers returns the approvals answered since the last call, once each.
func (t *Transcript) Answers() []Answer {
	a := t.answers
	t.answers = nil
	return a
}

// OpenAide reports, once, that Open in aide was clicked.
func (t *Transcript) OpenAide() bool {
	o := t.aide
	t.aide = false
	return o
}

// Layout draws s filling gtx's constraints, with an Open in aide button
// when aide is set.
func (t *Transcript) Layout(gtx layout.Context, th *theme.Theme, s *session.Snapshot, aide bool) layout.Dimensions {
	if t.open == nil || t.chat != s.SessionID {
		*t = Transcript{chat: s.SessionID, open: map[string]bool{}, btn: map[string]*widget.Clickable{}, answer: map[string]*[3]widget.Clickable{}, sent: map[string]bool{}}
		t.list.Axis = layout.Vertical
	}
	if t.epoch.IsZero() {
		t.epoch = gtx.Now
	}
	for id, b := range t.btn {
		if b.Clicked(gtx) {
			t.open[id] = !t.open[id]
		}
	}
	pending := map[string]bool{}
	for _, a := range s.PendingApprovals {
		pending[a.ID] = true
	}
	for id, bs := range t.answer {
		for i := range bs {
			if bs[i].Clicked(gtx) && pending[id] && !t.sent[id] {
				t.sent[id] = true
				t.answers = append(t.answers, Answer{Request: id, Decision: decisions[i]})
			}
		}
		if !pending[id] { // resolved, here or elsewhere
			delete(t.answer, id)
			delete(t.sent, id)
		}
	}
	if t.aideBtn.Clicked(gtx) && aide {
		t.aide = true
	}
	t.laid = 0
	size := gtx.Constraints.Max
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	paint.FillShape(gtx.Ops, th.Bg, clip.Rect{Max: size}.Op())
	d := &drawer{p: &Panel{}, th: th, c: palette(th), t: gtx.Now.Sub(t.epoch).Seconds()}
	tail := t.tail(d, s)
	w := max(0, min(size.X-2*gtx.Dp(24), gtx.Dp(transcriptWidth)))
	column := func(gtx layout.Context, it layout.Widget) layout.Dimensions {
		gtx.Constraints = layout.Constraints{Max: image.Pt(w, gtx.Constraints.Max.Y)}
		off := op.Offset(image.Pt((size.X-w)/2, 0)).Push(gtx.Ops)
		dims := it(gtx)
		off.Pop()
		return layout.Dimensions{Size: image.Pt(size.X, dims.Size.Y)}
	}
	// It follows the newest item once it overflows, from the end the first
	// time it does; a short one stays at the top.
	follow := t.list.Position.Length > size.Y
	if follow && !t.list.ScrollToEnd {
		t.list.Position.BeforeEnd = false
	}
	t.list.ScrollToEnd = follow
	t.list.Layout(gtx, 1+len(s.Content)+len(tail)+1, func(gtx layout.Context, i int) layout.Dimensions {
		switch i--; {
		case i < 0:
			return space(20)(gtx)
		case i < len(s.Content):
			t.laid++
			return column(gtx, t.item(d, &s.Content[i]))
		case i < len(s.Content)+len(tail):
			return column(gtx, tail[i-len(s.Content)])
		}
		return space(32)(gtx)
	})
	if aide {
		m := op.Record(gtx.Ops)
		bgtx := gtx
		bgtx.Constraints.Min = image.Point{}
		bd := kit.Button(bgtx, th, &t.aideBtn, kit.Secondary, kit.Small, "Open in aide")
		call := m.Stop()
		off := op.Offset(image.Pt(size.X-bd.Size.X-gtx.Dp(16), gtx.Dp(12))).Push(gtx.Ops)
		call.Add(gtx.Ops)
		off.Pop()
	}
	if d.p.live {
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(time.Second / 30)})
	}
	return layout.Dimensions{Size: size}
}

// item is one transcript item's widget.
func (t *Transcript) item(d *drawer, it *session.Item) layout.Widget {
	body := d.th.Sp(theme.Body)
	switch it.Kind {
	case session.KindUserPrompt:
		return pad(14, 0, 6, 0, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return boxed(gtx, layout.Inset{Top: 9, Bottom: 10, Left: 12, Right: 12}, d.text(d.th.UIFont, body, d.c.fg, it.Text, 0), func(sz image.Point) {
				rrect(gtx, image.Rectangle{Max: sz}, gtx.Dp(8), d.c.card2, d.c.border)
			})
		})
	case session.KindAssistantText:
		return pad(6, 0, 6, 0, d.text(d.th.UIFont, body, d.c.fg, it.Text, 0))
	case session.KindSystem:
		return pad(4, 0, 4, 0, d.text(d.th.UIFont, d.th.Sp(theme.Small), d.c.muted, it.Text, 0))
	case session.KindReasoningText, session.KindReasoningSummaryText:
		return t.fold(d, it.ID, d.ui(d.th.Sp(theme.Small), d.c.quiet, "Thinking"), d.text(d.th.UIFont, d.th.Sp(theme.Small), d.c.muted, it.Text, 0))
	case session.KindToolCall:
		if it.Tool == nil {
			return space(0)
		}
		return t.fold(d, it.ID, d.call(*it.Tool), d.output(*it.Tool))
	case session.KindCommandOutput, session.KindFileChangeOutput:
		return t.fold(d, it.ID, d.ui(d.th.Sp(theme.Small), d.c.quiet, "Output"), d.text(d.th.MonoFont, d.th.Sp(theme.Caption), d.c.muted, lastLines(it.Text, outputLines), 0))
	}
	return space(0)
}

// fold is a one-line head that a click opens to show body under it.
func (t *Transcript) fold(d *drawer, id string, head, body layout.Widget) layout.Widget {
	b := t.btn[id]
	if b == nil {
		b = new(widget.Clickable)
		t.btn[id] = b
	}
	open := t.open[id]
	chev := "chevron-right"
	if open {
		chev = "chevron-down"
	}
	return func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		ws := []layout.FlexChild{layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return clickable(gtx, b, func(gtx layout.Context) layout.Dimensions {
				return boxed(gtx, layout.Inset{Top: 4, Bottom: 4, Left: 6, Right: 6}, func(gtx layout.Context) layout.Dimensions {
					return row(gtx,
						part{w: pad(3, 0, 0, 0, func(gtx layout.Context) layout.Dimensions { return sidebar.Icon(gtx, chev, gtx.Dp(12), d.c.quiet) })},
						part{gap: gtx.Dp(6), flex: true, w: head},
					)
				}, func(sz image.Point) {
					if b.Hovered() {
						rrect(gtx, image.Rectangle{Max: sz}, gtx.Dp(6), d.th.SurfaceSecondary, theme.Transparent)
					}
				})
			})
		})}
		if open {
			ws = append(ws, layout.Rigid(pad(2, 6, 6, 22, body)))
		}
		return pad(1, 0, 1, 0, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, ws...)
		})(gtx)
	}
}

// call is a tool call's line: the tool, then what it ran on; a spinner
// while it runs, red when it failed.
func (d *drawer) call(c session.ToolCall) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		toolCol, argCol := d.c.fg, d.c.muted
		tool := d.text(semibold(d.th.UIFont), d.th.Sp(theme.Small), toolCol, c.ToolName, 1)
		switch c.Status {
		case "failed":
			tool = d.text(semibold(d.th.UIFont), d.th.Sp(theme.Small), d.c.red, c.ToolName, 1)
		case "running":
			argCol = d.c.blue
		}
		arg := flow.ToolArg(c)
		if c.Status == "running" && c.Progress != "" {
			arg = firstLine(c.Progress)
		}
		parts := []part{{w: tool}, {gap: gtx.Dp(8), flex: true, w: d.text(d.th.MonoFont, d.th.Sp(theme.Small), argCol, arg, 1)}}
		if c.Status == "running" {
			parts = append([]part{{w: func(gtx layout.Context) layout.Dimensions {
				d.p.live = true
				off := op.Offset(image.Pt(0, gtx.Dp(3))).Push(gtx.Ops)
				defer off.Pop()
				return spinner(gtx, gtx.Dp(9), d.c.bg, d.c.blue, d.t)
			}}}, parts...)
			parts[1].gap = gtx.Dp(6)
		}
		return row(gtx, parts...)
	}
}

// output is an opened call's command, diff and output, the end of each.
func (d *drawer) output(c session.ToolCall) layout.Widget {
	var b strings.Builder
	if c.Command != "" {
		b.WriteString("$ " + c.Command + "\n")
	}
	for _, s := range []string{c.Diff, c.Output} {
		if s = strings.TrimRight(s, "\n"); s != "" {
			b.WriteString(lastLines(s, outputLines) + "\n")
		}
	}
	if c.ExitCode != nil && *c.ExitCode != 0 {
		b.WriteString("exit " + strconv.Itoa(*c.ExitCode))
	}
	txt := strings.TrimRight(b.String(), "\n")
	if txt == "" {
		txt = "No output."
	}
	return d.text(d.th.MonoFont, d.th.Sp(theme.Caption), d.c.muted, txt, 0)
}

// tail is what follows the items: the chat's error, what it waits on, a
// spinner while it works.
func (t *Transcript) tail(d *drawer, s *session.Snapshot) []layout.Widget {
	var out []layout.Widget
	if s.LastError != nil && *s.LastError != "" {
		out = append(out, pad(8, 0, 4, 0, d.text(d.th.UIFont, d.th.Sp(theme.Small), d.c.red, *s.LastError, 0)))
	}
	chat := s.SessionID
	if len(chat) > 8 {
		chat = chat[:8]
	}
	for _, a := range s.PendingApprovals {
		what := strings.TrimSpace(deref(a.Detail))
		if what == "" && a.Request != nil {
			what = strings.TrimSpace(a.Request.ToolName + " " + firstLine(a.Request.Command))
		}
		out = append(out, d.waiting("Waiting for approval", what, "", t.buttons(d, a.ID)))
	}
	for _, in := range s.PendingUserInputs {
		what := deref(in.Detail)
		for _, q := range in.Questions {
			what = strings.TrimSpace(what + "\n" + q.Prompt)
		}
		out = append(out, d.waiting("Waiting for an answer", what, "pitwall chat answer "+chat+" "+in.ID+" -- answer", nil))
	}
	if p := s.PlanApproval; p != nil {
		out = append(out, d.waiting("Plan ready", deref(p.Detail), "", nil))
	}
	if s.SessionState == session.StateRunning {
		out = append(out, pad(8, 0, 0, 6, func(gtx layout.Context) layout.Dimensions {
			return row(gtx,
				part{w: func(gtx layout.Context) layout.Dimensions {
					d.p.live = true
					off := op.Offset(image.Pt(0, gtx.Dp(3))).Push(gtx.Ops)
					defer off.Pop()
					return spinner(gtx, gtx.Dp(9), d.c.bg, d.c.blue, d.t)
				}},
				part{gap: gtx.Dp(8), flex: true, w: d.ui(d.th.Sp(theme.Small), d.c.muted, "Working…")},
			)
		}))
	}
	return out
}

// buttons are an approval's Allow, Allow for session and Deny, or a line
// saying its answer went out.
func (t *Transcript) buttons(d *drawer, id string) layout.Widget {
	bs := t.answer[id]
	if bs == nil {
		bs = new([3]widget.Clickable)
		t.answer[id] = bs
	}
	if t.sent[id] {
		return d.ui(d.th.Sp(theme.Small), d.c.muted, "Answer sent…")
	}
	return func(gtx layout.Context) layout.Dimensions {
		btn := func(i int, k kit.Kind, label string) layout.FlexChild {
			return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Right: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return kit.Button(gtx, d.th, &bs[i], k, kit.Small, label)
				})
			})
		}
		return layout.Flex{}.Layout(gtx, btn(0, kit.Primary, "Allow"), btn(1, kit.Secondary, "Allow for session"), btn(2, kit.Danger, "Deny"))
	}
}

// waiting is a yellow card for a request the chat waits on, with the
// command that answers it or the buttons that do.
func (d *drawer) waiting(eyebrow, what, how string, actions layout.Widget) layout.Widget {
	return pad(10, 0, 4, 0, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return boxed(gtx, layout.Inset{Top: 11, Bottom: 12, Left: 14, Right: 14}, func(gtx layout.Context) layout.Dimensions {
			ws := []layout.FlexChild{layout.Rigid(d.text(semibold(d.th.UIFont), d.th.Sp(theme.Caption), d.c.yellow, strings.ToUpper(eyebrow), 1))}
			if what != "" {
				ws = append(ws, layout.Rigid(pad(5, 0, 0, 0, d.text(d.th.UIFont, d.th.Sp(theme.Body), d.c.fg, what, 6))))
			}
			if how != "" {
				ws = append(ws, layout.Rigid(pad(6, 0, 0, 0, d.text(d.th.MonoFont, d.th.Sp(theme.Caption), d.c.muted, how, 1))))
			}
			if actions != nil {
				ws = append(ws, layout.Rigid(pad(10, 0, 0, 0, actions)))
			}
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, ws...)
		}, func(sz image.Point) {
			rrect(gtx, image.Rectangle{Max: sz}, gtx.Dp(10), theme.Mix(d.c.bg, d.c.yellow, 0.06), theme.Mix(d.c.bg, d.c.yellow, 0.38))
		})
	})
}

// lastLines is s cut to its last n lines.
func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return "…\n" + strings.Join(lines[len(lines)-n:], "\n")
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
