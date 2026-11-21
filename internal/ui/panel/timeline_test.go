package panel

import (
	"encoding/json"
	"image"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// visibilityChat is one turn with every kind of row the visibility
// setting touches: thinking, a call that succeeded, words between calls,
// a failed call, a question, an error, a subagent, an edit and the reply.
func visibilityChat() *session.Snapshot {
	turn := "t1"
	call := func(id, name, status string) session.Item {
		return session.Item{ID: id, Kind: session.KindToolCall, TurnID: &turn, Tool: &session.ToolCall{ToolName: name, Status: status, Command: "make"}}
	}
	q := call("q1", "AskUserQuestion", "completed")
	q.Tool.Input, _ = json.Marshal(map[string]any{"questions": []map[string]string{{"question": "Which port?\nmore"}}})
	return &session.Snapshot{SessionID: "s", SessionState: session.StateReady, Content: []session.Item{
		{ID: "p1", Kind: session.KindUserPrompt, Text: "fix the build", TurnID: &turn},
		{ID: "r1", Kind: session.KindReasoningText, Text: "think", TurnID: &turn},
		call("b1", "Bash", "completed"),
		{ID: "a1", Kind: session.KindAssistantText, Text: "Now the tests.", TurnID: &turn},
		call("b2", "Bash", "failed"),
		q,
		call("g1", "Agent", "completed"),
		{ID: "e1", Kind: session.KindSystem, Phase: session.PhaseError, Text: "API error: overloaded", TurnID: &turn},
		call("w1", "Edit", "completed"),
		{ID: "a2", Kind: session.KindAssistantText, Text: "Fixed.", TurnID: &turn},
	}}
}

// visRows lays s out in tr with every fold open and names its rows:
// "item:<id>", "prose:<id>", "head", "reply", "turn", "turn-running".
func visRows(t *testing.T, tr *Transcript, s *session.Snapshot) []string {
	t.Helper()
	th := theme.Dark()
	var ops op.Ops
	now := time.Date(2026, 11, 19, 14, 1, 0, 0, time.UTC)
	transcriptFrame(tr, th, s, &ops, now)
	for _, r := range tr.rows {
		if r.seg == segHead {
			tr.open[logID(s, r.b)] = true
		}
	}
	transcriptFrame(tr, th, s, &ops, now)
	var out []string
	for _, r := range tr.rows {
		switch {
		case r.tail != nil || r.res != nil:
		case r.seg == segItem:
			out = append(out, "item:"+s.Content[r.i].ID)
		case r.seg == segProse:
			out = append(out, "prose:"+s.Content[r.i].ID)
		case r.seg == segHead:
			out = append(out, "head")
		case r.seg == segReply:
			out = append(out, "reply")
		case r.seg == segTurn && r.running:
			out = append(out, "turn-running")
		case r.seg == segTurn:
			out = append(out, "turn")
		}
	}
	return out
}

// TestToolCallsVisibility: [chat] tool_calls hides calls as T3 Code's
// toolCallVisibility does. failed keeps a call that failed and drops one
// that completed; none drops every call but keeps thinking; zen keeps
// only the reply, errors and questions, with no Worked for line. A
// question and a subagent show in every mode but zen's subagent.
func TestToolCallsVisibility(t *testing.T) {
	for _, c := range []struct {
		mode      string
		want, not []string
	}{
		{"", []string{"turn", "item:r1", "item:b1", "prose:a1", "item:b2", "item:q1", "item:g1", "prose:e1", "item:w1", "reply"}, nil},
		{ToolsAll, []string{"item:b1", "item:w1"}, nil},
		{ToolsFailed, []string{"item:r1", "item:b2", "item:q1", "item:g1", "prose:a1", "prose:e1", "reply"}, []string{"item:b1", "item:w1"}},
		{ToolsNone, []string{"item:r1", "item:q1", "item:g1", "prose:a1", "prose:e1", "reply"}, []string{"item:b1", "item:b2", "item:w1"}},
		{ToolsZen, []string{"item:q1", "prose:e1", "reply"}, []string{"turn", "head", "item:r1", "item:b1", "prose:a1", "item:b2", "item:g1", "item:w1"}},
		{"bogus", []string{"item:b1"}, nil},
	} {
		tr := &Transcript{ToolCalls: c.mode}
		got := visRows(t, tr, visibilityChat())
		for _, w := range c.want {
			if !slices.Contains(got, w) {
				t.Errorf("%q: no %s in %v", c.mode, w, got)
			}
		}
		for _, n := range c.not {
			if slices.Contains(got, n) {
				t.Errorf("%q: %s shows in %v", c.mode, n, got)
			}
		}
	}
}

// TestToolCallsRunning: a call still running shows under failed, the
// newest one live; zen hides a running turn's words until it ends and
// keeps its working line.
func TestToolCallsRunning(t *testing.T) {
	s := visibilityChat()
	s.Content = s.Content[:len(s.Content)-1] // no reply yet
	s.Content[len(s.Content)-1].Tool.Status = "running"
	s.Content = append(s.Content, session.Item{ID: "a3", Kind: session.KindAssistantText, Text: "Checking.", TurnID: s.Content[0].TurnID})
	s.SessionState, s.Running = session.StateRunning, true
	s.Turns = []session.TurnSummary{{ID: "t1", State: session.TurnRunning}}
	got := visRows(t, &Transcript{ToolCalls: ToolsFailed}, s)
	if !slices.Contains(got, "item:w1") && !slices.Contains(got, "head") {
		t.Errorf("failed: the running edit is gone: %v", got)
	}
	got = visRows(t, &Transcript{ToolCalls: ToolsZen}, s)
	if slices.Contains(got, "reply") || !slices.Contains(got, "turn-running") {
		t.Errorf("zen while running: %v, want no reply and the working line", got)
	}
	got = visRows(t, &Transcript{ToolCalls: ToolsAll}, s)
	if !slices.Contains(got, "reply") {
		t.Errorf("all while running: %v, want the words so far", got)
	}
}

// TestToolCallsZenFind: with the find bar open, zen shows the words
// between calls, which find looks in, and still no calls.
func TestToolCallsZenFind(t *testing.T) {
	tr := &Transcript{ToolCalls: ToolsZen}
	tr.OpenFind()
	got := visRows(t, tr, visibilityChat())
	if !slices.Contains(got, "prose:a1") || slices.Contains(got, "item:b1") {
		t.Errorf("zen with find open: %v", got)
	}
}

// TestToolCallsLoneHooks: a fold left with only hooks once its calls
// hide draws no line.
func TestToolCallsLoneHooks(t *testing.T) {
	turn := "t1"
	s := &session.Snapshot{SessionID: "s", Content: []session.Item{
		{ID: "p1", Kind: session.KindUserPrompt, Text: "go", TurnID: &turn},
		{ID: "b1", Kind: session.KindToolCall, TurnID: &turn, Tool: &session.ToolCall{ToolName: "Bash", Status: "completed"}},
		{ID: "h1", Kind: session.KindSystem, Phase: session.PhaseHook, Text: "hook ran", TurnID: &turn},
		{ID: "a1", Kind: session.KindAssistantText, Text: "done", TurnID: &turn},
	}}
	if got := visRows(t, &Transcript{ToolCalls: ToolsNone}, s); slices.Contains(got, "head") || slices.Contains(got, "item:h1") || slices.Contains(got, "turn") {
		t.Errorf("none: %v, want the reply alone", got)
	}
	if got := visRows(t, &Transcript{}, s); !slices.Contains(got, "head") {
		t.Errorf("all: %v, want the fold", got)
	}
}

// TestCodeScale: code takes CodeScale over the chat's text size; prose
// keeps its size.
func TestCodeScale(t *testing.T) {
	th := theme.Dark()
	d := &cdraw{t: &Transcript{}, th: th}
	base, prose := d.mono().size, d.prose().size
	d.t.CodeScale = 1.5
	if got := d.mono().size; got != base*1.5 || d.prose().size != prose {
		t.Errorf("code %v at 1.5 of %v, prose %v of %v", got, base, d.prose().size, prose)
	}
	d.t.CodeScale = 0
	if d.mono().size != base {
		t.Errorf("CodeScale 0: %v, want %v", d.mono().size, base)
	}
}

// TestLimitView: the limit notice offers Continue at the reset plus a
// minute and Snooze until then; once continue waits it says when and
// offers Cancel; past the reset it offers Continue now; with no reset it
// offers no continue; snoozed, Wake now. Without the window's state it has
// no buttons.
func TestLimitView(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.Local)
	soon := now.Add(time.Hour).UTC().Format(time.RFC3339)
	past := now.Add(-time.Hour).UTC().Format(time.RFC3339)
	l := &session.UsageLimit{Label: "Usage limit", ResetsAt: &soon}
	acts := func(as []limitAction) []string {
		var out []string
		for _, a := range as {
			out = append(out, a.label)
		}
		return out
	}
	full := &LimitResume{CanContinue: true, CanSnooze: true}
	for _, c := range []struct {
		name string
		l    *session.UsageLimit
		r    *LimitResume
		msg  string
		want []string
	}{
		{"no state", l, nil, "Resets 13:00.", nil},
		{"upcoming", l, full, "Resets 13:00.", []string{"Continue at 13:01", "Snooze until then"}},
		{"scheduled", l, &LimitResume{At: now.Add(61 * time.Minute), CanContinue: true}, "Continues at 13:01.", []string{"Cancel"}},
		{"old daemon", l, &LimitResume{CanSnooze: true}, "Resets 13:00.", []string{"Snooze until then"}},
		{"snoozed", l, &LimitResume{CanSnooze: true, Snoozed: now.Add(time.Hour)}, "Resets 13:00.", []string{"Wake now"}},
		{"past", &session.UsageLimit{Label: "Usage limit", ResetsAt: &past}, full, "Reset at 11:00.", []string{"Continue now"}},
		{"unknown", &session.UsageLimit{Label: "Usage limit"}, full, "Codex stopped until the limit resets.", nil},
	} {
		_, msg, as := limitView(c.l, c.r, "Codex", now)
		if msg != c.msg || !slices.Equal(acts(as), c.want) {
			t.Errorf("%s: %q %v, want %q %v", c.name, msg, acts(as), c.msg, c.want)
		}
	}
	if _, _, as := limitView(l, full, "Claude", now); as[0].act != LimitContinue || !as[0].at.Equal(now.Add(61*time.Minute)) || as[1].act != LimitSnooze || !as[1].at.Equal(now.Add(61*time.Minute)) {
		t.Errorf("upcoming actions %+v", as)
	}
}

// TestLimitCardClick: the notice's buttons run LimitResume.Do with
// their action, from real clicks.
func TestLimitCardClick(t *testing.T) {
	th := theme.Dark()
	soon := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	s := visibilityChat()
	s.UsageLimit = &session.UsageLimit{Label: "Usage limit", ResetsAt: &soon}
	var got []string
	tr := &Transcript{Limit: &LimitResume{CanContinue: true, CanSnooze: true, Do: func(a string, at time.Time) { got = append(got, a) }}}
	var r input.Router
	var ops op.Ops
	frame := func(evs ...pointer.Event) {
		for _, e := range evs {
			r.Queue(e)
		}
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(1000, 800)), Now: time.Now(), Source: r.Source()}
		tr.Layout(gtx, th, s, false)
		r.Frame(&ops)
	}
	frame()
	frame()
	for y := 0; y < 800 && len(got) == 0; y += 3 {
		for x := 100; x < 900 && len(got) == 0; x += 6 {
			p := f32.Pt(float32(x), float32(y))
			frame(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: p})
			if c := tr.clicks["limit:"+LimitContinue]; c == nil || !c.Hovered() {
				continue
			}
			frame(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p})
			frame(pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p})
			frame()
		}
	}
	if !slices.Equal(got, []string{LimitContinue}) {
		t.Errorf("clicked Continue at: %v", got)
	}
}

// TestMinimapMarks: a mark per prompt, error and question asked, then a
// pending question or approval and the chat's error at the end.
func TestMinimapMarks(t *testing.T) {
	s := visibilityChat()
	turn2 := "t2"
	s.Content = append(s.Content, session.Item{ID: "p2", Kind: session.KindUserPrompt, Text: "now ship it\nplease", TurnID: &turn2})
	s.PendingUserInputs = []session.InputRequest{{ID: "u1", Questions: []session.Question{{Prompt: "Which branch?"}}}}
	tr := &Transcript{}
	visRows(t, tr, s)
	var got []string
	for _, m := range minimapMarks(tr.rows, s) {
		got = append(got, markLabel(m))
	}
	want := []string{"Jump to message: fix the build", "Jump to question: Which port?", "Jump to error: API error: overloaded", "Jump to message: now ship it", "Jump to question: Which branch?"}
	if !slices.Equal(got, want) {
		t.Errorf("marks %q\nwant %q", got, want)
	}
	// Hidden calls take their marks with them; the prompts stay.
	tr = &Transcript{ToolCalls: ToolsZen}
	visRows(t, tr, s)
	if n := len(minimapMarks(tr.rows, s)); n != 5 {
		t.Errorf("zen: %d marks, want 5 (the question shows in zen)", n)
	}
}

// TestMmIndex: the pointer picks the nearest mark on the rail.
func TestMmIndex(t *testing.T) {
	for _, c := range []struct{ n, h, y, want int }{
		{1, 0, 5, 0}, {2, 8, 0, 0}, {2, 8, 3, 0}, {2, 8, 5, 1}, {5, 32, 16, 2}, {5, 32, -10, 0}, {5, 32, 99, 4},
	} {
		if got := mmIndex(c.n, c.h, c.y); got != c.want {
			t.Errorf("mmIndex(%d, %d, %d) = %d, want %d", c.n, c.h, c.y, got, c.want)
		}
	}
}

// TestMinimapJump: hovering the strip names the mark under the pointer
// and a click scrolls to its prompt; a narrow chat draws no strip.
func TestMinimapJump(t *testing.T) {
	th := theme.Dark()
	s := longChat(400)
	s.PendingApprovals = nil
	s.SessionState, s.Running = session.StateReady, false
	tr := &Transcript{Width: 600}
	var r input.Router
	var ops op.Ops
	width := 1000
	now := time.Now()
	frame := func(evs ...pointer.Event) {
		for _, e := range evs {
			r.Queue(e)
		}
		ops.Reset()
		now = now.Add(time.Millisecond)
		gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(width, 800)), Now: now, Source: r.Source()}
		tr.Layout(gtx, th, s, false)
		r.Frame(&ops)
	}
	frame()
	frame()
	if tr.list.Position.First < len(tr.rows)/2 {
		t.Fatalf("starts at row %d of %d, want the end", tr.list.Position.First, len(tr.rows))
	}
	marks := minimapMarks(tr.rows, s)
	// The rail is centred; its top mark is the first prompt.
	x := float32(width - int(mmInset) - 4)
	n := len(marks)
	h := min((n-1)*int(mmSpacing), 800-2*int(theme.Space2XL)-int(theme.SpaceXL))
	top := float32((800 - h) / 2)
	p := f32.Pt(x, top)
	frame(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: p})
	frame(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: p.Add(f32.Pt(0, 1))})
	if tr.mm.hover != 0 {
		t.Fatalf("hovering the top mark: hover %d", tr.mm.hover)
	}
	frame(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p})
	frame(pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p})
	frame()
	if first := tr.list.Position.First; first > 2 || !tr.list.Position.BeforeEnd {
		t.Errorf("after clicking the first prompt's mark: row %d, before end %v", first, tr.list.Position.BeforeEnd)
	}
	if !strings.HasPrefix(markLabel(marks[0]), "Jump to message: item 0") {
		t.Errorf("first mark %q", markLabel(marks[0]))
	}
	// Narrow: the column fills the chat, so no gutter holds the strip.
	width = 640
	frame(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(float32(width-int(mmInset)-4), top)})
	frame()
	if tr.mm.hover != -1 {
		t.Errorf("narrow chat: hover %d, want no strip", tr.mm.hover)
	}
}
