package panel

import (
	"encoding/json"
	"fmt"
	"image"
	"slices"
	"strings"
	"testing"
	"time"

	gioevent "gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// With a daemon that finds, the bar asks it once the query rests, drops
// a stale answer, starts at the newest match, and a match in history the
// window has not read reads older pages until it can show it. It never
// reads the whole history for a query, as the window's own find does.
func TestFindRemote(t *testing.T) {
	th := theme.Dark()
	full := longChat(400)
	full.Content[3].Text = "the needle is old"  // a reply in history not read yet
	full.Content[303].Text = "a needle in view" // a reply
	s := *full
	s.Content = full.Content[200:]
	var tr Transcript
	var asks []string
	older := 0
	tr.Find = func(q string) { asks = append(asks, q) }
	tr.Older = func() bool { older++; return true }
	var ops op.Ops
	now := time.Now()
	frame := func(at time.Duration) {
		for i := range 4 {
			transcriptFrame(&tr, th, &s, &ops, now.Add(at+time.Duration(i)*time.Millisecond))
		}
	}
	frame(0)
	before := older
	tr.OpenFind()
	tr.msg.find.SetQuery(" NEEDLE ")
	frame(time.Second)
	f := &tr.msg.find
	if len(asks) != 0 || f.counted(tr.older) != "Searching…" {
		t.Fatalf("asked %q before the query rested; bar %q", asks, f.counted(tr.older))
	}
	frame(time.Second + 2*findDebounce)
	if !slices.Equal(asks, []string{"needle"}) {
		t.Fatalf("asked %q, want needle once", asks)
	}
	if older != before {
		t.Errorf("the query read %d older pages; the daemon finds", older-before)
	}
	tr.Found("needl", []session.FindHit{{Item: "i250", At: []int{0}, Runes: 9}}, false, "")
	tr.Found("needle", []session.FindHit{{Item: "i3", At: []int{4}, Runes: 17}, {Item: "i303", At: []int{2}, Runes: 16}}, false, "")
	frame(2 * time.Second)
	if got := f.counted(tr.older); got != "2 of 2" || f.hits[f.cur].item != "i303" {
		t.Fatalf("bar %q, current %+v", got, f.hits)
	}
	bar := f.Height(layout.Context{Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}})
	shown := func(i int) bool {
		sp, ok := tr.msg.ys[tr.findRow(&s, i)+1]
		return ok && sp.y >= bar-int(theme.SpaceS) && sp.y < 800/2
	}
	if !shown(303 - 200) {
		t.Errorf("i303 at %+v: want it in view", tr.msg.ys[tr.findRow(&s, 103)+1])
	}
	// Previous: i3, not read yet. The window reads pages until it has it.
	f.cur, f.jump = wrap(f.cur, -1, len(f.hits)), true
	frame(3 * time.Second)
	if older == before || f.hits[f.cur].i != -1 || !f.jump {
		t.Fatalf("a hit not read yet read %d pages, hit %+v", older-before, f.hits[f.cur])
	}
	s.Content = full.Content // the pages came
	frame(4 * time.Second)
	frame(5 * time.Second)
	if f.hits[f.cur].item != "i3" || f.hits[f.cur].i != 3 || !shown(3) {
		t.Errorf("i3 after its page came: hit %+v at %+v", f.hits[f.cur], tr.msg.ys[tr.findRow(&s, 3)+1])
	}
	// The daemon's failure says so; a hit no page holds stops the jump.
	tr.msg.find.SetQuery("gone")
	frame(6 * time.Second)
	frame(6*time.Second + 2*findDebounce)
	tr.Found("gone", nil, false, "no chat c")
	frame(7 * time.Second)
	if got := f.counted(tr.older); got != "Search failed" {
		t.Errorf("a failed search reads %q", got)
	}
	tr.Older = func() bool { return false }
	tr.Found("gone", []session.FindHit{{Item: "nowhere", At: []int{0}, Runes: 4}}, false, "")
	frame(8 * time.Second)
	if f.jump {
		t.Error("a hit in no page kept jumping")
	}
}

// A match in folded rows opens them: the turn's Worked for fold, the
// work log, the thought's own fold, and a plan's row, which then shows the
// plan with the match marked.
func TestFindUnfolds(t *testing.T) {
	th := theme.Dark()
	turn := "t1"
	plan, _ := json.Marshal(map[string]string{"plan": "# Plan\n\nThread the needle through"})
	s := &session.Snapshot{SessionID: "c", SessionState: session.StateWaiting, Content: []session.Item{
		{ID: "p1", Kind: session.KindUserPrompt, Text: "go", TurnID: &turn},
		{ID: "r1", Kind: session.KindReasoningText, Text: "first a needle thought", TurnID: &turn},
		{ID: "b1", Kind: session.KindToolCall, TurnID: &turn, Tool: &session.ToolCall{ToolName: "Bash", Command: "ls", Status: "completed"}},
		{ID: "x1", Kind: session.KindToolCall, TurnID: &turn, Tool: &session.ToolCall{ToolName: "ExitPlanMode", Input: plan, Status: "completed"}},
		{ID: "b2", Kind: session.KindToolCall, TurnID: &turn, Tool: &session.ToolCall{ToolName: "Bash", Command: "pwd", Status: "completed"}},
		{ID: "a1", Kind: session.KindAssistantText, Text: "Done.", TurnID: &turn},
	}}
	var tr Transcript
	var ops op.Ops
	now := time.Now()
	frame := func(at time.Duration) {
		for i := range 4 {
			transcriptFrame(&tr, th, s, &ops, now.Add(at+time.Duration(i)*time.Millisecond))
		}
	}
	frame(0)
	b := tr.blocks[slices.IndexFunc(tr.blocks, func(b block) bool { return b.kind == blockTurn })]
	tr.open[turnID(s, b)] = false // the turn's work hidden
	frame(time.Second)
	tr.OpenFind()
	tr.msg.find.SetQuery("needle")
	frame(2 * time.Second)
	f := &tr.msg.find
	if len(f.hits) != 2 || f.hits[f.cur].item != "x1" {
		t.Fatalf("hits %+v, cur %d", f.hits, f.cur)
	}
	frame(3 * time.Second)
	if !isOpen(tr.open, turnID(s, b), true) || !tr.open["x1"] || f.drawn["x1"] == 0 {
		t.Errorf("plan: turn open %v, row open %v, %d marks", isOpen(tr.open, turnID(s, b), true), tr.open["x1"], f.drawn["x1"])
	}
	f.cur, f.jump = 0, true
	frame(4 * time.Second)
	if !tr.open["r1"] || f.drawn["r1"] == 0 {
		t.Errorf("thought: open %v, %d marks, rows %d", tr.open["r1"], f.drawn["r1"], len(tr.rows))
	}
}

// The bar's keys: Enter goes down, or up for a chat's bar, Shift the
// other way, the arrows as they point, and Escape shuts it.
func TestFindBarKeys(t *testing.T) {
	for _, up := range []bool{false, true} {
		var r input.Router
		var ops op.Ops
		b := FindBar{EnterUp: up}
		b.Open()
		frame := func(evs ...gioevent.Event) (int, bool) {
			for _, e := range evs {
				r.Queue(e)
			}
			ops.Reset()
			gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(800, 600)), Now: time.Now(), Source: r.Source()}
			step, shut := b.Update(gtx)
			b.Layout(gtx, theme.Dark(), image.Pt(800, 600), "Find", "")
			r.Frame(&ops)
			return step, shut
		}
		frame()
		frame()
		press := func(n key.Name, m key.Modifiers) key.Event { return key.Event{Name: n, Modifiers: m, State: key.Press} }
		enter := 1
		if up {
			enter = -1
		}
		for _, c := range []struct {
			e    key.Event
			want int
		}{
			{press(key.NameReturn, 0), enter},
			{press(key.NameReturn, key.ModShift), -enter},
			{press(key.NameEnter, 0), enter},
			{press(key.NameDownArrow, 0), 1},
			{press(key.NameUpArrow, 0), -1},
		} {
			if step, _ := frame(c.e); step != c.want {
				t.Errorf("up %v, %v: step %d, want %d", up, c.e, step, c.want)
			}
		}
		if _, shut := frame(press(key.NameEscape, 0)); !shut || b.On() {
			t.Errorf("up %v: Escape left the bar open", up)
		}
	}
	for _, c := range [][4]int{{0, -1, 3, 2}, {2, 1, 3, 0}, {-1, 1, 3, 0}, {1, 5, 3, 0}} {
		if got := wrap(c[0], c[1], c[2]); got != c[3] {
			t.Errorf("wrap(%d, %d, %d) = %d, want %d", c[0], c[1], c[2], got, c[3])
		}
	}
}

// The Changes find looks in every file's diff, as each box draws it,
// tabs spaced out, folded; each hit knows its box, its place in it and
// its line.
func TestSideHits(t *testing.T) {
	fs := []fileChange{
		{path: "a.go", diffs: [][]diffLine{{{kind: '@', text: "@@ -1,2 +1,2 @@"}, {kind: '-', text: "\tneedle := 1"}, {kind: '+', text: "\tNeedle := 2 // needle"}}}},
		{path: "b.go", diffs: [][]diffLine{{{kind: '+', text: "x"}}, {{kind: '+', text: "the NEEDLE"}}}},
		{path: "c.go", diffs: [][]diffLine{{{kind: '+', text: "nothing"}}}},
	}
	got := fmt.Sprint(sideHits("t", fs, session.Fold("needle")))
	// b.go's two edits draw under "Edit 1 of 2" and "Edit 2 of 2" lines.
	want := "[{0 0 1 side:t:a.go} {0 1 2 side:t:a.go} {0 2 2 side:t:a.go} {1 0 3 side:t:b.go}]"
	if got != want {
		t.Errorf("sideHits = %s, want %s", got, want)
	}
	if h := sideHits("t", fs, nil); h != nil {
		t.Errorf("no query: %v", h)
	}
}

// Ctrl+F with the pointer on the Changes panel finds in it, not in the
// chat; the first match is current and in view, and closing the panel
// closes its find.
func TestSideFind(t *testing.T) {
	th := theme.Dark()
	s := toolChat()
	tr := &Transcript{Dir: "/repo"}
	var ops op.Ops
	now := time.Now()
	frame := func(at time.Duration) {
		for i := range 3 {
			transcriptFrame(tr, th, s, &ops, now.Add(at+time.Duration(i)*time.Millisecond))
		}
	}
	frame(0)
	tr.showChanges("", "")
	frame(time.Second)
	tr.OpenFind()
	if tr.side.find.On() || !tr.msg.find.On() {
		t.Fatal("Ctrl+F off the panel did not open the chat's find")
	}
	tr.msg.find.close()
	tr.side.find.over = true
	tr.OpenFind()
	if !tr.side.find.On() || tr.msg.find.On() || !tr.FindOpen() {
		t.Fatal("Ctrl+F on the panel did not open its find")
	}
	tr.side.find.SetQuery("ALT+T")
	frame(2 * time.Second)
	sf := &tr.side.find
	if len(sf.hits) != 1 || sf.cur != 0 || sf.count() != "1 of 1" {
		t.Fatalf("hits %+v cur %d count %q", sf.hits, sf.cur, sf.count())
	}
	// A match far down a long diff: its file's row, scrolled to its line.
	var diff strings.Builder
	diff.WriteString("@@ -1,300 +1,300 @@\n")
	for i := range 300 {
		fmt.Fprintf(&diff, "+line %d\n", i)
	}
	turn := "t9"
	s.Content = append(s.Content,
		session.Item{ID: "p9", Kind: session.KindUserPrompt, Text: "long", TurnID: &turn},
		session.Item{ID: "e9", Kind: session.KindToolCall, TurnID: &turn, Tool: &session.ToolCall{ToolName: "Edit", Status: "completed",
			Input: json.RawMessage(`{"file_path":"/repo/long.go"}`), Diff: diff.String()}},
		session.Item{ID: "a9", Kind: session.KindAssistantText, Text: "Done.", TurnID: &turn})
	tr.showChanges("", "")
	tr.side.find.SetQuery("line 250")
	frame(3 * time.Second)
	if len(sf.hits) != 1 || sf.hits[0].line != 251 {
		t.Fatalf("long diff: hits %+v", sf.hits)
	}
	if p := tr.side.list.Position; p.First != 1 || p.Offset < 200*10 {
		t.Errorf("list at %+v, want row 1 well down to line 251", p)
	}
	tr.side.find.SetQuery("no such text")
	frame(3 * time.Second)
	if sf.count() != "No results" {
		t.Errorf("count %q", sf.count())
	}
	tr.CloseFiles()
	if tr.side.find.On() {
		t.Error("closing the panel left its find open")
	}
}
