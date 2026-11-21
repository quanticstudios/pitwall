package panel

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestTimelineShots writes PNGs of the tool call modes, the work log's
// labels with failed calls, the usage limit notice and the minimap to
// $PITWALL_TIMELINE_SHOTS, for a look at them; it skips without it.
func TestTimelineShots(t *testing.T) {
	dir := os.Getenv("PITWALL_TIMELINE_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_TIMELINE_SHOTS is not set")
	}
	shotAt := time.Date(2026, 11, 19, 14, 1, 0, 0, time.UTC)
	// Each mode over Claude's and Codex's real transcripts.
	for _, f := range turnFixtures[:2] {
		for _, mode := range []string{ToolsAll, ToolsFailed, ToolsNone, ToolsZen} {
			tr := &Transcript{Dir: "/home/user/calc", ToolCalls: mode}
			tr.init()
			shoot(t, tr, f.replay(t, f.finished, false), filepath.Join(dir, "tools-"+f.name+"-"+mode+".png"), image.Pt(820, 900), 1.5)
		}
	}
	// Labels: failed calls are not counted as done and tint the line.
	turn := "t1"
	call := func(id, name, status, cmd string) session.Item {
		return session.Item{ID: id, Kind: session.KindToolCall, TurnID: &turn, Tool: &session.ToolCall{ToolName: name, Status: status, Command: cmd}}
	}
	labels := &session.Snapshot{SessionID: "s", Provider: "claude", Content: []session.Item{
		{ID: "p1", Kind: session.KindUserPrompt, Text: "Run the checks and fix what fails", TurnID: &turn},
		call("b1", "Bash", "completed", "go build ./..."), call("b2", "Bash", "failed", "go vet ./..."), call("r1", "Read", "completed", ""), call("r2", "Read", "completed", ""),
		{ID: "a1", Kind: session.KindAssistantText, Text: "Vet fails on an unused import. Fixing it.", TurnID: &turn},
		call("e1", "Edit", "failed", ""), call("g1", "Grep", "completed", ""), call("w1", "WebFetch", "completed", ""),
		{ID: "a2", Kind: session.KindAssistantText, Text: "The edit did not apply; the file changed under me.", TurnID: &turn},
		call("b3", "Bash", "failed", "go test ./..."),
		{ID: "a3", Kind: session.KindAssistantText, Text: "Tests still fail. Stopping here.", TurnID: &turn},
	}}
	shoot(t, &Transcript{}, labels, filepath.Join(dir, "labels.png"), image.Pt(820, 560), 1.5)
	// The usage limit notice: Continue at and Snooze, once scheduled, and snoozed.
	soon := shotAt.Add(73 * time.Minute).Format(time.RFC3339)
	for _, c := range []struct {
		name string
		r    *LimitResume
	}{
		{"limit-offer", &LimitResume{CanContinue: true, CanSnooze: true}},
		{"limit-scheduled", &LimitResume{CanContinue: true, CanSnooze: true, At: shotAt.Add(74 * time.Minute)}},
		{"limit-snoozed", &LimitResume{CanContinue: true, CanSnooze: true, At: shotAt.Add(74 * time.Minute), Snoozed: shotAt.Add(74 * time.Minute)}},
	} {
		s := visibilityChat()
		s.Provider, s.SessionState = "claude", session.StateError
		s.UsageLimit = &session.UsageLimit{Label: "Session limit", ResetsAt: &soon}
		shoot(t, &Transcript{Limit: c.r}, s, filepath.Join(dir, c.name+".png"), image.Pt(820, 700), 1.5)
	}
	// The minimap, a mark hovered: prompts, an error and a question.
	s := &session.Snapshot{SessionID: "s", Provider: "claude", SessionState: session.StateReady}
	asks := []string{"Add a dark theme toggle", "Make the toggle remember its state", "Why does the build fail on CI?", "Bump Go to 1.27.1", "Write a test for the toggle", "Ship it"}
	for i, ask := range asks {
		tid := fmt.Sprint("t", i)
		s.Content = append(s.Content, session.Item{ID: "p" + tid, Kind: session.KindUserPrompt, Text: ask, TurnID: &tid},
			session.Item{ID: "b" + tid, Kind: session.KindToolCall, TurnID: &tid, Tool: &session.ToolCall{ToolName: "Bash", Status: "completed", Command: "go test ./..."}},
			session.Item{ID: "a" + tid, Kind: session.KindAssistantText, Text: "Done. " + ask + " is in, with its test passing.", TurnID: &tid})
		if i == 2 {
			s.Content = append(s.Content, session.Item{ID: "e" + tid, Kind: session.KindSystem, Phase: session.PhaseError, Text: "API Error: 529 overloaded", TurnID: &tid})
		}
	}
	s.PendingUserInputs = []session.InputRequest{{ID: "u1", Questions: []session.Question{{Prompt: "Which branch should I push to?", Options: []session.PromptOption{{Label: "main"}, {Label: "dark-theme"}}}}}}
	tr := &Transcript{Width: 640}
	var ops op.Ops
	transcriptFrame(tr, theme.Dark(), s, &ops, shotAt)
	n := len(minimapMarks(tr.rows, s))
	size := image.Pt(1500, 1350)
	h := (n - 1) * 12 // 8 dp apart at 1.5 px per dp
	shootPointer(t, tr, s, filepath.Join(dir, "minimap.png"), size, 1.5, image.Pt(size.X-18-6, (size.Y-h)/2+4*h/(n-1)))
	// Code at 1.4 times the chat's text.
	f := turnFixtures[0]
	tr = &Transcript{Dir: "/home/user/calc", CodeScale: 1.4}
	tr.init()
	transcriptFrame(tr, theme.Dark(), f.replay(t, f.finished, false), &ops, shotAt)
	for _, r := range tr.rows {
		if r.seg == segHead {
			tr.open[logID(f.replay(t, f.finished, false), r.b)] = true
		}
	}
	shoot(t, tr, f.replay(t, f.finished, false), filepath.Join(dir, "code-size-1.4.png"), image.Pt(820, 1000), 1.5)
}

// shootPointer is shoot with the pointer at p, in px, from the first frame.
func shootPointer(t *testing.T, tr *Transcript, s *session.Snapshot, path string, size image.Point, scale float32, p image.Point) {
	t.Helper()
	win, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		t.Skip("no headless GPU:", err)
	}
	defer win.Release()
	var r input.Router
	var ops op.Ops
	for i := range 4 {
		r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(float32(p.X), float32(p.Y+i%2))})
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Source: r.Source(), Now: time.Date(2026, 11, 19, 14, 1, 0, 0, time.UTC),
			Metric: unit.Metric{PxPerDp: scale, PxPerSp: scale}, Constraints: layout.Exact(size)}
		tr.Layout(gtx, theme.Dark(), s, false)
		r.Frame(&ops)
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
