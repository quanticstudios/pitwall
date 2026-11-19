package panel

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/claude"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// toolChat is a finished turn that called each kind of tool once, with
// what Claude Code's results carry.
func toolChat() *session.Snapshot {
	in := func(m map[string]any) json.RawMessage { b, _ := json.Marshal(m); return b }
	zero, two := 0, 2
	var pic bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 480, 300))
	for y := range 300 {
		for x := range 480 {
			img.Set(x, y, color.RGBA{uint8(40 + x/4), uint8(60 + y/3), 160, 255})
		}
	}
	png.Encode(&pic, img)
	shot := base64.StdEncoding.EncodeToString(pic.Bytes())
	turn := "t1"
	at := func(s int) string { return time.Date(2026, 11, 19, 14, 0, s, 0, time.UTC).Format(time.RFC3339Nano) }
	calls := []session.ToolCall{
		{ToolName: "Read", Status: "completed", Input: in(map[string]any{"file_path": "/repo/internal/ui/panel/chatlog.go", "offset": 120, "limit": 4}),
			Output: "   120→func (d *cdraw) logRow(gtx layout.Context, it *session.Item) layout.Dimensions {\n   121→\tth := d.th\n   122→\tswitch it.Kind {\n   123→\tcase session.KindToolCall:\n\n<system-reminder>\nWhenever you read a file, consider malware.\n</system-reminder>\n"},
		{ToolName: "Grep", Status: "completed", Input: in(map[string]any{"pattern": "func \\(d \\*cdraw\\) tool", "path": "internal/ui", "output_mode": "content", "-n": true}),
			Output: "/repo/internal/ui/panel/tool_row.go:104:func (d *cdraw) toolRow(\n/repo/internal/ui/panel/tool_row.go:340:func (d *cdraw) toolResult(\n/repo/internal/ui/panel/tool_media.go:30:func (d *cdraw) toolImages("},
		{ToolName: "Glob", Status: "completed", Input: in(map[string]any{"pattern": "internal/ui/panel/*_test.go"}),
			Output: "/repo/internal/ui/panel/blank_test.go\n/repo/internal/ui/panel/chatcards_test.go\n/repo/internal/ui/panel/chips_test.go"},
		{ToolName: "Edit", Status: "completed", Input: in(map[string]any{"file_path": "/repo/internal/ui/panel/chatmodel.go", "old_string": "a", "new_string": "b"}),
			Diff: "@@ -88,6 +88,7 @@\n \n // toolOpen is whether a tool row starts open: only a failed call does.\n-func toolOpen(c *session.ToolCall) bool { return c != nil && c.Status == \"failed\" }\n+func toolOpen(c *session.ToolCall) bool {\n+\treturn c != nil && c.Status == \"failed\" // a very long comment that runs past the edge of the box so it scrolls sideways instead of wrapping\n+}\n \n // isOpen is a fold's state: what the user set, else its default.\n"},
		{ToolName: "MultiEdit", Status: "completed", Input: in(map[string]any{"file_path": "/repo/README.md"}),
			Diff: "@@ -3,3 +3,3 @@\n # pitwall\n-A terminal.\n+A native terminal multiplexer.\n@@ -40,2 +40,3 @@\n ## Keys\n+Alt+T switches tabs.\n"},
		{ToolName: "Write", Status: "completed", Input: in(map[string]any{"file_path": "/repo/docs/tools.md", "content": "# Tools\n\nEach tool call draws as a row.\nA click opens it.\n"}),
			Files: []session.ToolFile{{Path: "/repo/docs/tools.md", Status: "add"}}},
		{ToolName: "Bash", Status: "completed", Command: "", Input: in(map[string]any{"command": "go test ./internal/ui/panel/", "description": "Run the panel tests"}), ExitCode: &zero,
			Output: "\x1b[32mok\x1b[0m  \tgithub.com/quanticstudios/pitwall/internal/ui/panel\t1.84s\n\x1b[1;33mwarning:\x1b[0m 2 shots skipped", StartedAt: at(0), EndedAt: at(4)},
		{ToolName: "Bash", Status: "failed", Input: in(map[string]any{"command": "go vet ./..."}), ExitCode: &two,
			Output: "Exit code 2\n# github.com/x/y\n./a.go:3:2: \"fmt\" imported and not used", StartedAt: at(5), EndedAt: at(7)},
		{ToolName: "Bash", Status: "completed", Input: in(map[string]any{"command": "npm run dev", "run_in_background": true, "description": "Start the dev server"}),
			Output: "Command running in background with ID: bx1. Output is being written to: /tmp/x", Background: "running"},
		{ToolName: "TaskOutput", Status: "completed", Input: in(map[string]any{"task_id": "bx1"}),
			Output: "<status>running</status>\n<output>\n> dev\n\x1b[36mready\x1b[0m on http://localhost:5173\n</output>"},
		{ToolName: "Bash", Status: "completed", Input: in(map[string]any{"command": "gh pr create --fill", "description": "Open the pull request"}), ExitCode: &zero,
			Output: "https://github.com/quanticstudios/pitwall/pull/459"},
		{ToolName: "Read", Status: "completed", Input: in(map[string]any{"file_path": "/repo/shots/tools.png"}),
			Images: []session.Attachment{{ID: "image-1", Kind: "image", MimeType: "image/png", Data: shot}}},
		{ToolName: "WebSearch", Status: "completed", Input: in(map[string]any{"query": "gio horizontal scroll gesture"}),
			Output: `Web search results for query: "gio horizontal scroll gesture"` + "\n\nLinks: [{\"title\":\"gesture package - gioui.org/gesture\",\"url\":\"https://pkg.go.dev/gioui.org/gesture\"},{\"title\":\"Gio UI: Scrolling\",\"url\":\"https://www.gioui.org/doc/learn/scroll\"}]\n\nSummary."},
		{ToolName: "WebFetch", Status: "completed", Input: in(map[string]any{"url": "https://www.gioui.org/doc/architecture/input", "prompt": "How does routing work?"}),
			Output: "# Input routing\n\nEvents go to the **topmost** handler, then its parents."},
		{ToolName: "mcp__plugin_linear_linear__save_issue", Status: "completed", Input: in(map[string]any{"title": "Render tool calls natively", "team": "ENG"}),
			Output: `{"id":"ENG-3100","title":"Render tool calls natively","status":"Todo"}`},
		{ToolName: "Skill", Status: "completed", Input: in(map[string]any{"skill": "agent-desktop"}), Output: "Launching skill: agent-desktop"},
		{ToolName: "Edit", Status: "failed", Input: in(map[string]any{"file_path": "/repo/go.mod", "old_string": "x", "new_string": "y"}),
			Output: "<tool_use_error>File has not been read yet. Read it first before writing to it.</tool_use_error>"},
		{ToolName: "Frobnicate", Status: "completed", Input: in(map[string]any{"target": "build cache", "force": true}), Output: "done"},
	}
	s := &session.Snapshot{SessionID: "c1", SessionState: session.StateReady}
	s.Content = append(s.Content, session.Item{ID: "p1", Kind: session.KindUserPrompt, Text: "Draw every tool natively.", TurnID: &turn, CreatedAt: at(0)})
	for i := range calls {
		c := calls[i]
		c.CallID = "c" + string(rune('a'+i))
		s.Content = append(s.Content, session.Item{ID: c.CallID, Kind: session.KindToolCall, Tool: &c, TurnID: &turn, CreatedAt: at(i), UpdatedAt: at(i)})
	}
	s.Content = append(s.Content, session.Item{ID: "r1", Kind: session.KindAssistantText, Text: "Done.", TurnID: &turn, CreatedAt: at(30), UpdatedAt: at(30)})
	return s
}

// TestToolShots writes PNGs of every tool's row, folded and open, to
// $PITWALL_TOOL_SHOTS, for a look at them; it skips without it.
func TestToolShots(t *testing.T) {
	dir := os.Getenv("PITWALL_TOOL_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_TOOL_SHOTS is not set")
	}
	for _, open := range []bool{false, true} {
		s := toolChat()
		tr := &Transcript{Dir: "/repo", PRs: map[string]model.PR{"w1": {Number: 459, State: model.PROpen, URL: "https://github.com/quanticstudios/pitwall/pull/459",
			Checks: []model.Check{{Name: "test", State: model.CheckPass}}}}}
		tr.init()
		tr.open[logID(s, blocks(s.Content, nil)[1])] = true
		if open {
			for _, it := range s.Content {
				tr.open[it.ID] = true
			}
		}
		name := "folded"
		if open {
			name = "open"
		}
		shoot(t, tr, s, filepath.Join(dir, "tools-"+name+".png"), image.Pt(900, 2400), 1.5)
	}
	s := toolChat()
	tr := &Transcript{Dir: "/repo"}
	tr.init()
	for _, it := range s.Content {
		if it.Tool != nil && len(it.Tool.Images) > 0 {
			tr.tool.zoom = zoomed{key: it.ID + "/image-1", a: it.Tool.Images[0]}
		}
	}
	shoot(t, tr, s, filepath.Join(dir, "tools-zoom.png"), image.Pt(900, 700), 1.5)
}

// shoot draws tr a few frames, so its images decode, and writes it.
func shoot(t *testing.T, tr *Transcript, s *session.Snapshot, path string, dp image.Point, scale float32) {
	t.Helper()
	th := theme.Dark()
	size := image.Pt(int(float32(dp.X)*scale), int(float32(dp.Y)*scale))
	win, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		t.Skip("no headless GPU:", err)
	}
	defer win.Release()
	var r input.Router
	var ops op.Ops
	for range 4 {
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Source: r.Source(), Now: time.Date(2026, 11, 19, 14, 1, 0, 0, time.UTC),
			Metric: unit.Metric{PxPerDp: scale, PxPerSp: scale}, Constraints: layout.Exact(size)}
		tr.Layout(gtx, th, s, false)
		r.Frame(&ops)
		time.Sleep(150 * time.Millisecond)
	}
	if err := win.Frame(&ops); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rectangle{Max: size})
	if err := win.Screenshot(img); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

// TestToolShotsReal renders the last turns of a real Claude Code
// transcript, $PITWALL_TOOL_TRANSCRIPT, read as a pane chat reads it, to
// $PITWALL_TOOL_SHOTS.
func TestToolShotsReal(t *testing.T) {
	dir, src := os.Getenv("PITWALL_TOOL_SHOTS"), os.Getenv("PITWALL_TOOL_TRANSCRIPT")
	if dir == "" || src == "" {
		t.Skip("PITWALL_TOOL_SHOTS or PITWALL_TOOL_TRANSCRIPT is not set")
	}
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	snap := session.Snapshot{SessionID: "real", SessionState: session.StateReady}
	tr := claude.NewTranscript()
	for _, l := range bytes.Split(b, []byte("\n")) {
		for _, e := range tr.Line(l) {
			for _, d := range session.Reduce(snap, e) {
				d.Version, d.SessionID, d.Seq, d.Revision, d.UpdatedAt = session.DeltaVersion, snap.SessionID, snap.Revision+1, snap.Revision+1, e.CreatedAt
				if n, err := session.Apply(&snap, snap.Revision, d); err == nil {
					snap = n
				}
			}
		}
	}
	keep := 60
	if len(snap.Content) > keep {
		snap.Content = snap.Content[len(snap.Content)-keep:]
	}
	tp := &Transcript{Dir: os.Getenv("PITWALL_TOOL_DIR")}
	tp.init()
	for _, bl := range blocks(snap.Content, nil) {
		if bl.kind == blockTurn {
			tp.open[logID(&snap, bl)] = true
		}
	}
	shoot(t, tp, &snap, filepath.Join(dir, "tools-real.png"), image.Pt(900, 4000), 1.25)
}
