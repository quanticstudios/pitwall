package panel

import (
	"encoding/json"
	"image"
	"reflect"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func TestANSILines(t *testing.T) {
	ls := ansiLines("\x1b[32mok\x1b[0m  \tpkg\n\x1b[1;33mwarn:\x1b[0m x\nbar 10%\rbar 100%\n\x1b]8;;http://x\x07link\x1b]8;;\x07 \x1b[38;5;196mred\x1b[39m")
	if len(ls) != 4 {
		t.Fatalf("%d lines", len(ls))
	}
	if ls[0].text != "ok      pkg" || !reflect.DeepEqual(ls[0].spans, []span{{a: 0, b: 2, fg: 2}}) {
		t.Errorf("line 0 %+v", ls[0])
	}
	if ls[1].text != "warn: x" || !reflect.DeepEqual(ls[1].spans, []span{{a: 0, b: 5, fg: 3, bold: true}}) {
		t.Errorf("line 1 %+v", ls[1])
	}
	if ls[2].text != "bar 100%" {
		t.Errorf("carriage return %q", ls[2].text)
	}
	if ls[3].text != "link red" || !reflect.DeepEqual(ls[3].spans, []span{{a: 5, b: 8, fg: 196}}) {
		t.Errorf("osc and 256 colors %+v", ls[3])
	}
}

func TestGrepHits(t *testing.T) {
	hits, n := grepHits("content", "a/x.go:3:foo\na/x.go-4-ctx\n--\na/y.go:9:foo")
	if n != 2 || !reflect.DeepEqual(hits, []fileHit{{"a/x.go", 1}, {"a/y.go", 1}}) {
		t.Errorf("content %v %d", hits, n)
	}
	hits, n = grepHits("count", "a/x.go:4\na/y.go:1\n\nFound 5 total occurrences across 2 files.")
	if n != 5 || len(hits) != 2 || hits[0].n != 4 {
		t.Errorf("count %v %d", hits, n)
	}
	hits, _ = grepHits("", "Found 2 files\na/x.go\na/y.go")
	if hitsLine(hits, 0) != "2 files" {
		t.Errorf("files %v", hits)
	}
	if hits, _ := grepHits("", "No files found"); hitsLine(hits, 0) != "No matches" {
		t.Errorf("none %v", hits)
	}
	if g := globHits("/r/a.go\n/r/b.go\n(Results are truncated. Consider a narrower pattern.)"); len(g) != 2 {
		t.Errorf("glob %v", g)
	}
}

func TestWebSources(t *testing.T) {
	out := "Web search results for query: \"q\"\n\nLinks: [{\"title\":\"A\",\"url\":\"https://a.dev/x\"},{\"title\":\"B\",\"url\":\"https://www.b.org/\"}]\n\nLinks: [{\"title\":\"A\",\"url\":\"https://a.dev/x\"}]\nsummary"
	got := webSources(out)
	if len(got) != 2 || got[1].Title != "B" || host(got[1].URL) != "b.org" {
		t.Fatalf("%+v", got)
	}
	if s := shortURL("https://www.gioui.org/doc/learn/"); s != "gioui.org/doc/learn" {
		t.Errorf("short url %q", s)
	}
}

func TestMCPName(t *testing.T) {
	for in, want := range map[string][2]string{
		"mcp__plugin_linear_linear__save_issue":   {"Linear", "save issue"},
		"mcp__plugin_slack_slack__slack_send_msg": {"Slack", "send msg"},
		"mcp__claude_ai_Gmail__search_threads":    {"Gmail", "search threads"},
		"mcp__claude-in-chrome__tabs_context_mcp": {"Claude in chrome", "tabs context"},
		"mcp__linear-server-qs__getIssue":         {"Linear server qs", "get issue"},
	} {
		s, tool, ok := mcpName(in)
		if !ok || s != want[0] || tool != want[1] {
			t.Errorf("%s: %q %q", in, s, tool)
		}
	}
	if _, _, ok := mcpName("Bash"); ok {
		t.Error("Bash is not MCP")
	}
}

func TestFirstArg(t *testing.T) {
	if a := firstArg(json.RawMessage(`{"force":true,"target":"build cache\nmore"}`)); a != "build cache" {
		t.Errorf("first arg %q", a)
	}
	if a := firstArg(json.RawMessage(`{"n":3}`)); a != "3" {
		t.Errorf("number %q", a)
	}
}

func TestErrorLine(t *testing.T) {
	for out, want := range map[string]string{
		"<tool_use_error>File has not been read yet.</tool_use_error>": "File has not been read yet.",
		"Exit code 1\n\nFAIL x":                      "FAIL x",
		"boom\n<system-reminder>x</system-reminder>": "boom",
	} {
		if got := errorLine(&session.ToolCall{Output: out}); got != want {
			t.Errorf("%q: %q", out, got)
		}
	}
}

func TestReadRange(t *testing.T) {
	lo, hi, ok := readRange("    12→a\n    13→\tb\n    14→c")
	if !ok || lo != 12 || hi != 14 {
		t.Fatalf("%d %d %v", lo, hi, ok)
	}
	if _, _, ok := readRange("plain\ntext"); ok {
		t.Fatal("plain text read as numbered")
	}
	if l := readLines("     7→x"); l[0].new != 7 || l[0].text != "x" {
		t.Fatalf("%+v", l)
	}
}

// TestShells: a call that reads or stops a background shell draws on the
// shell's row, which takes its state and output.
func TestShells(t *testing.T) {
	in := func(s string) json.RawMessage { return json.RawMessage(s) }
	content := []session.Item{
		{ID: "b", Tool: &session.ToolCall{ToolName: "Bash", Background: "running", Output: "Command running in background with ID: bx1. Output is"}},
		{ID: "o", Tool: &session.ToolCall{ToolName: "TaskOutput", Status: "completed", Input: in(`{"task_id":"bx1"}`), Output: "<status>completed</status>\n<exit_code>0</exit_code>\n<output>\nready\n</output>"}},
		{ID: "x", Tool: &session.ToolCall{ToolName: "TaskStop", Status: "completed", Input: in(`{"task_id":"agent-7"}`)}},
	}
	var sh shells
	sh.index(content)
	if !sh.isAttached(1) || sh.isAttached(2) || !reflect.DeepEqual(sh.calls["b"], []int{1}) {
		t.Fatalf("%+v", sh)
	}
	state, out := shellState(content[0].Tool, []*session.ToolCall{content[1].Tool})
	if state != "completed" || out != "ready" {
		t.Fatalf("%q %q", state, out)
	}
	stop := &session.ToolCall{ToolName: "TaskStop", Status: "completed"}
	if state, _ := shellState(content[0].Tool, []*session.ToolCall{stop}); state != "killed" {
		t.Fatalf("stopped: %q", state)
	}
}

// TestToolRowClicks: a click on a row's path chip opens the file and
// leaves the row folded; a click elsewhere on the row opens it; a click on
// a picture shows it large until Esc.
func TestToolRowClicks(t *testing.T) {
	turn := "t1"
	read := &session.ToolCall{CallID: "r", ToolName: "Read", Status: "completed", Input: json.RawMessage(`{"file_path":"/repo/a/main.go"}`), Output: "     1→package a"}
	pic := &session.ToolCall{CallID: "p", ToolName: "Read", Status: "completed", Input: json.RawMessage(`{"file_path":"/repo/shot.png"}`),
		Images: []session.Attachment{{ID: "image-1", Kind: "image", Data: "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="}}}
	s := &session.Snapshot{SessionID: "c", SessionState: session.StateReady, Content: []session.Item{
		{ID: "r", Kind: session.KindToolCall, Tool: read, TurnID: &turn},
		{ID: "p", Kind: session.KindToolCall, Tool: pic, TurnID: &turn},
	}}
	var opened []string
	tr := &Transcript{Dir: "/repo", Open: func(p string) { opened = append(opened, p) }}
	tr.init()
	tr.open[logID(s, blocks(s.Content, nil)[0])] = true
	th := theme.Dark()
	var r input.Router
	var ops op.Ops
	frame := func() {
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: layout.Exact(image.Pt(1000, 800)), Now: time.Now()}
		tr.Layout(gtx, th, s, false)
		r.Frame(&ops)
	}
	click := func(x, y float32) {
		frame()
		for _, k := range []pointer.Kind{pointer.Press, pointer.Release} {
			r.Queue(pointer.Event{Kind: k, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: f32.Pt(x, y)})
		}
		frame()
	}
	// The column starts 24 dp in; the log's header is 1+34 dp under the top
	// 24, the Read row's middle 1+17 dp below that. Its chip follows the
	// verb, from about 56+30 dp.
	x0 := float32(24)
	y := float32(24 + 1 + 34 + 1 + 17)
	click(x0+120, y)
	if len(opened) != 1 || opened[0] != "/repo/a/main.go" || isOpen(tr.open, "r", false) {
		t.Fatalf("chip click: opened %v, row open %v", opened, isOpen(tr.open, "r", false))
	}
	click(x0+600, y)
	if !isOpen(tr.open, "r", false) || len(opened) != 1 {
		t.Fatalf("row click: open %v, opened %v", isOpen(tr.open, "r", false), opened)
	}
	click(x0+600, y) // fold it again
	// The picture row's header 1 dp under the Read row, then its thumbnail
	// under it, from 56 dp.
	for range 20 { // the 1 px picture decodes off this goroutine
		frame()
		if tr.thumbs.snapshot(tr.thumbs.getSized("p/image-1@120", &pic.Images[0], 120, nil)).ready {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	click(x0+56+10, y+17+1+34+10)
	if tr.tool.zoom.key != "p/image-1" {
		t.Fatalf("thumbnail click: zoom %q", tr.tool.zoom.key)
	}
	r.Queue(key.Event{Name: key.NameEscape, State: key.Press})
	frame()
	if tr.tool.zoom.key != "" {
		t.Fatal("Esc left the picture open")
	}
}
