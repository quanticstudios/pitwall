package panel

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/chatin"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func TestFindRunes(t *testing.T) {
	for _, tc := range []struct {
		text, q string
		want    []int
	}{
		{"Fix the BUG, then the bug", "bug", []int{8, 22}},
		{"aaaa", "aa", []int{0, 2}},             // not overlapping
		{"Ärger über Äpfel", "ä", []int{0, 11}}, // runes, folded
		{"", "x", nil},
		{"x", "", nil},
		{"short", "longer than it", nil},
	} {
		if got := findRunes(fold(tc.text), fold(tc.q)); !slices.Equal(got, tc.want) {
			t.Errorf("findRunes(%q, %q) = %v, want %v", tc.text, tc.q, got, tc.want)
		}
	}
}

// The count covers prompts and Claude's text, not tool calls; a page of
// older history keeps the current match on its item, and a changed item
// is counted again.
func TestFindRecount(t *testing.T) {
	item := func(id string, k session.ItemKind, text string) session.Item {
		return session.Item{ID: id, Kind: k, Text: text}
	}
	s := &session.Snapshot{Content: []session.Item{
		item("p2", session.KindUserPrompt, "deploy the api"),
		{ID: "c", Kind: session.KindToolCall, Tool: &session.ToolCall{Command: "deploy"}},
		item("a2", session.KindAssistantText, "Deployed. The deploy took 3 s."),
	}}
	f := finder{on: true, cur: -1, q: fold("DEPLOY")}
	if !f.recount(s) || len(f.hits) != 3 || f.recount(s) {
		t.Fatalf("hits %+v: want 3, counted once", f.hits)
	}
	f.cur = 1 // a2's first
	s.Content = append([]session.Item{item("p1", session.KindUserPrompt, "how do I deploy?"), item("a1", session.KindAssistantText, "Run make deploy.")}, s.Content...)
	f.recount(s)
	if len(f.hits) != 5 || f.hits[f.cur].item != "a2" || f.hits[f.cur].n != 0 || f.hits[f.cur].i != 4 {
		t.Fatalf("after a page: cur %d of %+v", f.cur, f.hits)
	}
	if got := f.counted(true); got != "4 of 5+" {
		t.Errorf("count %q", got)
	}
	s.Content[4].Text += " Deploy again?"
	s.Content[4].UpdatedAt = "later"
	f.recount(s)
	if len(f.hits) != 6 {
		t.Errorf("after an edit: %d hits", len(f.hits))
	}
	f.q = fold("nothing like it")
	f.recount(s)
	if len(f.hits) != 0 || f.cur != -1 || f.counted(false) != "No results" || f.counted(true) != "Searching…" {
		t.Errorf("no match: %+v %d %q", f.hits, f.cur, f.counted(false))
	}
	f.q = nil
	f.recount(s)
	if f.counted(false) != "" {
		t.Errorf("no query: %q", f.counted(false))
	}
}

// The find bar on a long chat: the newest match is current and in view,
// Enter walks up to the one before, and a match in a closed work log
// opens it.
func TestFindJump(t *testing.T) {
	th := theme.Dark()
	s := longChat(400)
	s.Content[3].Text = "the needle is here"    // a reply
	s.Content[203].Text = "a needle in between" // a reply
	var tr Transcript
	var ops op.Ops
	now := time.Now()
	frame := func() {
		for i := range 4 {
			transcriptFrame(&tr, th, s, &ops, now.Add(time.Duration(i)*time.Millisecond))
		}
	}
	frame()
	tr.OpenFind()
	tr.msg.find.ed.SetText("NEEDLE")
	frame()
	f := &tr.msg.find
	if len(f.hits) != 2 || f.cur != 1 || f.counted(false) != "2 of 2" {
		t.Fatalf("hits %+v cur %d", f.hits, f.cur)
	}
	row := func() int { return rowItem(tr.rows[max(0, tr.list.Position.First-1)]) }
	if got := row(); got != 203 {
		t.Errorf("view starts at item %d, want 203", got)
	}
	f.cur, f.jump = 0, true
	frame()
	if got := row(); got != 3 {
		t.Errorf("view starts at item %d, want 3", got)
	}
	// Claude's text in a work log: its log opens to show it.
	s.Content[103].Text = "needle"
	s.Content = slices.Insert(s.Content, 104, session.Item{ID: "tool", Kind: session.KindToolCall, Tool: &session.ToolCall{ToolName: "Bash", Status: "completed"}})
	frame()
	f.cur = slices.IndexFunc(f.hits, func(h findHit) bool { return h.i == 103 })
	f.jump = true
	frame()
	if got := row(); got != 103 {
		t.Errorf("view starts at item %d, want 103 in its opened log", got)
	}
}

func TestAsideQuestion(t *testing.T) {
	for in, want := range map[string]string{
		"/btw why Go?":      "why Go?",
		"  /btw\nwhy Go?  ": "why Go?",
		"/btw":              "",
	} {
		if got, ok := AsideQuestion(in); !ok || got != want {
			t.Errorf("AsideQuestion(%q) = %q, %v", in, got, ok)
		}
	}
	for _, in := range []string{"/btwx", "why /btw", "/model x"} {
		if _, ok := AsideQuestion(in); ok {
			t.Errorf("AsideQuestion(%q) asked", in)
		}
	}
	var tr Transcript
	id := tr.Ask("why Go?")
	tr.Answered("other", "late", "")
	if !tr.msg.aside.waiting {
		t.Error("another question's answer filled the card")
	}
	tr.Answered(id, "", "")
	if tr.msg.aside.waiting || tr.msg.aside.err == "" {
		t.Errorf("an empty answer: %+v", tr.msg.aside)
	}
}

// Edit puts a prompt back in the composer as the user typed it: without
// pitwall's sender line or the image tokens it cannot bring back.
func TestEditRoundTrip(t *testing.T) {
	for _, tc := range []struct{ prompt, want string }{
		{"fix the bug\nin parse.go", "fix the bug\nin parse.go"},
		{"[Image #1] what is wrong here?", "what is wrong here?"},
		{"  spaced  ", "spaced"},
	} {
		it := &session.Item{ID: "p", Kind: session.KindUserPrompt, Text: tc.prompt}
		var c chatin.Composer
		c.SetText(promptText(it))
		if got := c.Text(); got != tc.want {
			t.Errorf("edit of %q: composer %q, want %q", tc.prompt, got, tc.want)
		}
	}
}

func TestSpans(t *testing.T) {
	var m msgState
	m.laid = []laidRow{{5, 30}, {3, 10}, {4, 20}, {6, 40}}
	m.spans(layout.Position{First: 4, Offset: 5})
	want := map[int]rowSpan{3: {-15, 10}, 4: {-5, 20}, 5: {15, 30}, 6: {45, 40}}
	if fmt.Sprint(m.ys) != fmt.Sprint(want) {
		t.Errorf("spans %v, want %v", m.ys, want)
	}
}

// A chat that rewinds and forks whole turns offers them on the prompt
// that opened a turn, not on one sent into it mid-turn.
func TestOpensTurn(t *testing.T) {
	t1, t2 := "t1", "t2"
	content := []session.Item{
		{ID: "p1", Kind: session.KindUserPrompt, TurnID: &t1},
		{ID: "a1", Kind: session.KindAssistantText, TurnID: &t1},
		{ID: "p1b", Kind: session.KindUserPrompt, TurnID: &t1}, // sent now, mid-turn
		{ID: "p2", Kind: session.KindUserPrompt, TurnID: &t2},
		{ID: "none", Kind: session.KindUserPrompt},
	}
	for i, want := range []bool{true, false, false, true, false} {
		if got := opensTurn(content, &content[i]); got != want {
			t.Errorf("opensTurn(%s) = %v", content[i].ID, got)
		}
	}
}
