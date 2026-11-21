package panel

import (
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"gioui.org/gpu/headless"

	"github.com/quanticstudios/pitwall/internal/session"
)

// richChat is two turns with most of what a chat draws: a work log with
// a command and an edit, a reply with a heading, a list, a table, a link
// and a code block, and a second prompt and reply.
func richChat() *session.Snapshot {
	in := func(m map[string]any) json.RawMessage { b, _ := json.Marshal(m); return b }
	zero := 0
	return &session.Snapshot{Content: []session.Item{
		{ID: "p1", Kind: session.KindUserPrompt, Text: "Why does the cursor wrap one column early?", TurnID: tid("t1")},
		{ID: "c1", Kind: session.KindToolCall, TurnID: tid("t1"), Tool: &session.ToolCall{ToolName: "Bash", Status: "completed", ExitCode: &zero,
			Input: in(map[string]any{"command": "go test ./internal/vt -run TestWrap"}), Output: "--- FAIL: TestWrap (0.00s)\n    wrap_test.go:41: cursor at 79, want 80"}},
		{ID: "c2", Kind: session.KindToolCall, TurnID: tid("t1"), Tool: &session.ToolCall{ToolName: "Edit", Status: "completed",
			Input: in(map[string]any{"file_path": "/repo/internal/vt/wrap.go"}), Diff: "@@ -12,3 +12,3 @@\n func wrap(x, w int) bool {\n-\treturn x >= w-1\n+\treturn x >= w\n }\n"}},
		{ID: "a1", Kind: session.KindAssistantText, TurnID: tid("t1"), Text: "## The cause\n\nThe check compared against `w-1`, so the cursor wrapped **one column early**. See [the spec](https://vt100.net/docs/vt100-ug/chapter3.html).\n\n- `wrap` now compares with `w`\n- the test covers column 80\n\n| Width | Before | After |\n|---|---|---|\n| 80 | 79 | 80 |\n| 132 | 131 | 132 |\n\n```go\nfunc wrap(x, w int) bool {\n\treturn x >= w\n}\n```\n\nThe test passes now."},
		{ID: "p2", Kind: session.KindUserPrompt, Text: "Great, commit it", TurnID: tid("t2")},
		{ID: "a2", Kind: session.KindAssistantText, TurnID: tid("t2"), Text: "Committed as `fix(vt): wrap at the last column`."},
	}}
}

// TestSelectionShots draws a selection over richChat to
// $PITWALL_SELECT_SHOTS: one from the middle of the first reply, through
// its code block, into the second prompt and reply; one over all of it,
// so text that does not select stands out. It skips without it.
func TestSelectionShots(t *testing.T) {
	dir := os.Getenv("PITWALL_SELECT_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_SELECT_SHOTS is not set")
	}
	shot := func(name string, pick func(g *selRig) (image.Point, image.Point)) {
		s := richChat()
		g := &selRig{t: t, s: s, size: image.Pt(1350, 2100), scale: 1.5}
		g.tr.init()
		g.tr.open[logID(s, blocks(s.Content, nil)[1])] = true
		g.tr.open["c1"], g.tr.open["c2"] = true, true
		g.start()
		a, b := pick(g)
		g.drag(a, b)
		win, err := headless.NewWindow(g.size.X, g.size.Y)
		if err != nil {
			t.Skip("no headless GPU:", err)
		}
		defer win.Release()
		if err := win.Frame(&g.ops); err != nil {
			t.Fatal(err)
		}
		img := image.NewRGBA(image.Rectangle{Max: g.size})
		if err := win.Screenshot(img); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s copied %q", name, g.copies)
	}
	shot("select-across.png", func(g *selRig) (image.Point, image.Point) {
		from, to := g.box("a1/1"), g.box("a2/0")
		return image.Pt(from.Min.X+from.Dx()/3, (from.Min.Y+from.Max.Y)/2), image.Pt(to.Min.X+to.Dx()/2, (to.Min.Y+to.Max.Y)/2)
	})
	shot("select-all.png", func(g *selRig) (image.Point, image.Point) {
		r := g.box("p1")
		return image.Pt(r.Max.X-1, (r.Min.Y+r.Max.Y)/2), image.Pt(g.size.X-2, g.size.Y-2)
	})
}
