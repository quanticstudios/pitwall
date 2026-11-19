package panel

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/session"
)

func turnItem(id string, kind session.ItemKind, turn string) session.Item {
	it := session.Item{ID: id, Kind: kind}
	if turn != "" {
		it.TurnID = &turn
	}
	if kind == session.KindToolCall {
		it.Tool = &session.ToolCall{ToolName: "Bash", Status: "completed"}
	}
	return it
}

// TestBlocks: a prompt is a bubble, its turn's items one block whose
// reply is the text after the last call, and a line outside a turn its
// own row.
func TestBlocks(t *testing.T) {
	c := []session.Item{
		turnItem("p1", session.KindUserPrompt, "t1"),
		turnItem("a1", session.KindAssistantText, "t1"),
		turnItem("c1", session.KindToolCall, "t1"),
		turnItem("a2", session.KindAssistantText, "t1"),
		turnItem("c2", session.KindToolCall, "t1"),
		turnItem("r1", session.KindAssistantText, "t1"),
		turnItem("s1", session.KindSystem, ""),
		turnItem("p2", session.KindUserPrompt, "t2"),
		turnItem("c3", session.KindToolCall, "t2"),
		turnItem("x", session.KindReasoningText, "t3"), // another turn without a prompt
	}
	got := blocks(c, nil)
	want := []block{
		{kind: blockPrompt, lo: 0, hi: 1, turn: "t1", reply: -1},
		{kind: blockTurn, lo: 1, hi: 6, turn: "t1", reply: 5, tools: 2, todo: -1},
		{kind: blockSystem, lo: 6, hi: 7, reply: -1},
		{kind: blockPrompt, lo: 7, hi: 8, turn: "t2", reply: -1},
		{kind: blockTurn, lo: 8, hi: 9, turn: "t2", reply: -1, tools: 1, todo: -1},
		{kind: blockTurn, lo: 9, hi: 10, turn: "t3", reply: -1, todo: -1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("blocks\n got %+v\nwant %+v", got, want)
	}
}

// TestBlocksTodoAndError: a turn's last task-list call is its checklist,
// and an error notice opens its log as a failed call does.
func TestBlocksTodoAndError(t *testing.T) {
	todo := func(id string, done int) session.Item {
		it := turnItem(id, session.KindToolCall, "t1")
		it.Tool = &session.ToolCall{CallID: id, ToolName: "TodoWrite", Status: "completed", Tasks: []session.Task{{Status: "completed"}, {Status: "pending"}}}
		return it
	}
	errItem := turnItem("e", session.KindSystem, "t1")
	errItem.Phase = session.PhaseError
	c := []session.Item{turnItem("p", session.KindUserPrompt, "t1"), todo("w1", 0), turnItem("c", session.KindToolCall, "t1"), todo("w2", 1), errItem}
	b := blocks(c, nil)[1]
	if b.todo != 3 || b.todos != 2 || !b.failed || b.tools != 1 {
		t.Fatalf("block %+v: want the second TodoWrite as its card, two left out of the log, failed", b)
	}
}

// TestOpenRules: the running turn's log starts open and a finished one
// folded; only a failed call starts open; what the user set wins.
func TestOpenRules(t *testing.T) {
	if !logOpen(true, false) || logOpen(false, false) || !logOpen(false, true) {
		t.Fatal("log: running or failed open, finished folded")
	}
	for status, want := range map[string]bool{"failed": true, "running": false, "completed": false} {
		if got := toolOpen(&session.ToolCall{Status: status}); got != want {
			t.Errorf("%s call open = %v", status, got)
		}
	}
	set := map[string]bool{"a": false}
	if isOpen(set, "a", true) || !isOpen(set, "b", true) {
		t.Fatal("a user's fold does not win over the default")
	}
}

func TestImageRefs(t *testing.T) {
	s := "Like [Image #1] and [Image #12], not [Image 3] or [image #4]."
	got := imageRefs(s)
	if len(got) != 2 || got[0].n != 1 || got[1].n != 12 || s[got[0].start:got[0].end] != "[Image #1]" || s[got[1].start:got[1].end] != "[Image #12]" {
		t.Fatalf("refs %+v", got)
	}
	if imageRefs("no images") != nil {
		t.Fatal("refs in plain text")
	}
}

// TestImageNo: a prompt's thumbnails take its tokens' numbers, so a draft
// that lost image #1 shows #2 on both, and images past the tokens count on.
func TestImageNo(t *testing.T) {
	refs := imageRefs("What does this say? [Image #2] and [Image #5]")
	for n, want := range map[int]int{1: 2, 2: 5, 3: 3} {
		if got := imageNo(refs, n); got != want {
			t.Errorf("image %d shows #%d, want #%d", n, got, want)
		}
	}
	if got := imageNo(nil, 1); got != 1 {
		t.Errorf("no tokens: #%d", got)
	}
}

// TestProseCap: prose stops near 96 characters in a wide column, and at
// the column in a narrow one.
func TestProseCap(t *testing.T) {
	if got := proseCap(960, 7); got != 96*7 {
		t.Fatalf("wide column: %d", got)
	}
	if got := proseCap(500, 7); got != 500 {
		t.Fatalf("narrow column: %d", got)
	}
	if got := proseCap(800, 0); got != 800 {
		t.Fatalf("unmeasured font: %d", got)
	}
}

// TestKeyActions: 1, 2 and 3 answer an approval; a question takes its
// option numbers, one more for its typed row, and Enter.
func TestKeyActions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		approval bool
		options  int
		want     keyAction
		ok       bool
	}{
		{"1", true, 0, keyAction{decision: "allow", option: -1}, true},
		{"2", true, 3, keyAction{decision: "allow-session", option: -1}, true},
		{"3", true, 0, keyAction{decision: "deny", option: -1}, true},
		{"4", true, 0, keyAction{option: -1}, false},
		{string(key.NameReturn), true, 0, keyAction{option: -1}, false},
		{"1", false, 3, keyAction{option: 0}, true},
		{"4", false, 3, keyAction{option: 3}, true}, // the typed row
		{"5", false, 3, keyAction{option: -1}, false},
		{string(key.NameReturn), false, 3, keyAction{option: -1, submit: true}, true},
		{string(key.NameEnter), false, 1, keyAction{option: -1, submit: true}, true},
		{"1", false, 0, keyAction{option: -1}, false}, // nothing waits
	} {
		got, ok := actionFor(tc.name, tc.approval, tc.options)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%q approval=%v options=%d: %+v %v", tc.name, tc.approval, tc.options, got, ok)
		}
	}
}

func TestRecommended(t *testing.T) {
	q := session.Question{Options: []session.PromptOption{{Label: "Keep"}, {Label: "Resize (Recommended)"}}}
	if recommended(q) != 1 || recommended(session.Question{Options: q.Options[:1]}) != -1 {
		t.Fatal("recommended option")
	}
}

// TestDiffLines: a unified diff keeps its line numbers; old and new text
// diff by line without them.
func TestDiffLines(t *testing.T) {
	u := diffLines(&session.ToolCall{Diff: "--- a/f\n+++ b/f\n@@ -10,3 +10,3 @@ func f\n a\n-b\n+c\n d"})
	want := []diffLine{{'@', 0, 0, "@@ -10,3 +10,3 @@ func f"}, {' ', 10, 10, "a"}, {'-', 11, 0, "b"}, {'+', 0, 11, "c"}, {' ', 12, 12, "d"}}
	if !reflect.DeepEqual(u, want) {
		t.Fatalf("unified %+v", u)
	}
	in, _ := json.Marshal(map[string]string{"file_path": "x.go", "old_string": "a\nb\nc", "new_string": "a\nB\nc\nd"})
	e := diffLines(&session.ToolCall{ToolName: "Edit", Input: in})
	want = []diffLine{{' ', 0, 0, "a"}, {'-', 0, 0, "b"}, {'+', 0, 0, "B"}, {' ', 0, 0, "c"}, {'+', 0, 0, "d"}}
	if !reflect.DeepEqual(e, want) {
		t.Fatalf("edit %+v", e)
	}
	acp := diffLines(&session.ToolCall{Diff: "x.go\nOld text:\nold\nNew text:\nnew"})
	if len(acp) != 2 || acp[0].kind != '-' || acp[1].kind != '+' {
		t.Fatalf("acp %+v", acp)
	}
}

// TestSummarize: each tool's row says what the call carries and leaves
// out what it does not.
func TestSummarize(t *testing.T) {
	zero, one := 0, 1
	now := time.Date(2026, 11, 18, 14, 0, 14, 0, time.UTC)
	in := func(m map[string]string) json.RawMessage { b, _ := json.Marshal(m); return b }
	for _, tc := range []struct {
		c    session.ToolCall
		verb string
		res  string
	}{
		{session.ToolCall{ToolName: "Read", Status: "completed", Input: in(map[string]string{"file_path": "a/b.go"}), Output: "1\n2\n3\n"}, "Read", "3 lines"},
		{session.ToolCall{ToolName: "Grep", Status: "completed", Input: in(map[string]string{"pattern": "x"}), Output: "a:1:x\nb:2:x"}, "Search", "2 matches"},
		{session.ToolCall{ToolName: "Bash", Status: "completed", Command: "go vet", ExitCode: &zero, StartedAt: "2026-11-18T14:00:00Z", EndedAt: "2026-11-18T14:00:00.8Z"}, "Bash", "0.8s"},
		{session.ToolCall{ToolName: "Bash", Status: "failed", Command: "go test", ExitCode: &one}, "Bash", "exit 1"},
		{session.ToolCall{ToolName: "Bash", Status: "running", Command: "go test ./..."}, "Bash", "0:14"},
	} {
		s := summarize(&tc.c, nil, now.Add(-14*time.Second), now)
		if s.verb != tc.verb || s.result != tc.res {
			t.Errorf("%s: %q %q", tc.c.ToolName, s.verb, s.result)
		}
	}
	s := summarize(&session.ToolCall{ToolName: "Bash", Status: "completed", Command: "true", ExitCode: &zero}, nil, time.Time{}, now)
	if !s.passed || s.result != "" {
		t.Fatalf("passed command without times: %+v", s)
	}
	s = summarize(&session.ToolCall{ToolName: "Edit", Status: "completed"}, []diffLine{{kind: '+'}, {kind: '+'}, {kind: '-'}, {kind: ' '}}, time.Time{}, now)
	if s.add != 2 || s.del != 1 {
		t.Fatalf("edit counts %+v", s)
	}
}

func TestLogTitle(t *testing.T) {
	if got := logTitle(48*time.Second, 4, false); got != "Worked for 48s · 4 tools" {
		t.Fatal(got)
	}
	if got := logTitle(21*time.Second, 1, true); got != "Working for 21s · 1 tool" {
		t.Fatal(got)
	}
}

// TestToolOutput: a running call with no output shows its progress; a
// finished one shows only its output.
func TestToolOutput(t *testing.T) {
	c := &session.ToolCall{Status: "running", Progress: "Compiling 3/8"}
	if got := toolOutput(c); got != "Compiling 3/8" {
		t.Fatalf("running: %q", got)
	}
	c.Output = "ok"
	if got := toolOutput(c); got != "ok" {
		t.Fatalf("with output: %q", got)
	}
	c.Status, c.Output = "completed", ""
	if got := toolOutput(c); got != "" {
		t.Fatalf("finished: %q", got)
	}
}

// TestRelPath: a path under the chat's folder reads relative to it, and
// anything else stays as it came.
func TestRelPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "acme-api") // absolute on every OS
	for _, c := range [][3]string{
		{dir, filepath.Join(dir, "main.go"), "main.go"},
		{dir, filepath.Join(dir, "internal", "x.go"), filepath.Join("internal", "x.go")},
		{dir, dir, dir},
		{dir, filepath.Join(dir+"-old", "main.go"), filepath.Join(dir+"-old", "main.go")},
		{dir, "main.go", "main.go"},
		{"", filepath.Join(dir, "main.go"), filepath.Join(dir, "main.go")},
	} {
		if got := relPath(c[0], c[1]); got != c[2] {
			t.Errorf("relPath(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

func TestPromptParts(t *testing.T) {
	shown, ps := promptParts("\n\n<pasted_content id=\"ba5e\">\nline 1\nline 2\n</pasted_content id=\"ba5e\">\n\nCount the lines.<system-reminder>x</system-reminder>")
	if shown != "Count the lines." || len(ps) != 1 || ps[0].lines != 2 || ps[0].text != "line 1\nline 2" {
		t.Fatalf("shown %q, pastes %+v", shown, ps)
	}
	if !slashCommand("/model claude-opus-5-5") || slashCommand("/tmp/x is a path\nwith lines") || slashCommand("fix /model") {
		t.Fatal("slashCommand")
	}
	if got, cut := summaryCut("The output was:\n\n```\n2 notes.txt\n```", 3, 280); got != "The output was:\n2 notes.txt" || !cut {
		t.Fatalf("summaryCut = %q, %v", got, cut)
	}
}
