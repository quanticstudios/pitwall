package panel

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/claude"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// renderSnap reduces a transcript file's lines into a snapshot, as a pane
// chat does.
func renderSnap(t *testing.T, path string) session.Snapshot {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tr := claude.NewTranscript()
	snap := session.Snapshot{SessionID: "s", Provider: "claude", SessionState: session.StateReady, Running: true}
	for _, l := range bytes.Split(b, []byte("\n")) {
		snap = reduceAll(t, snap, tr.Line(l))
	}
	return snap
}

func reduceAll(t *testing.T, snap session.Snapshot, es []session.Event) session.Snapshot {
	t.Helper()
	for _, e := range es {
		if e.CreatedAt == "" {
			e.CreatedAt = session.Stamp(time.Date(2026, 11, 19, 20, 50, 0, 0, time.UTC))
		}
		for _, d := range session.Reduce(snap, e) {
			d.Version, d.SessionID, d.Seq, d.Revision, d.UpdatedAt = session.DeltaVersion, snap.SessionID, snap.Revision+1, snap.Revision+1, e.CreatedAt
			n, err := session.Apply(&snap, snap.Revision, d)
			if err != nil {
				t.Fatalf("%s: %v", d.Type, err)
			}
			snap = n
		}
	}
	return snap
}

// richSnap is a running turn with everything this package draws besides
// replies and plain calls: a task list, two subagents side by side, one
// running with its calls, thinking with text, a retry, and the cards a
// turn stops on.
func richSnap() session.Snapshot {
	at := func(s int) string { return session.Stamp(time.Date(2026, 11, 19, 20, 0, s, 0, time.UTC)) }
	turn := "t1"
	tp := &turn
	model := "claude-sonnet-5-5"
	item := func(id string, k session.ItemKind, s0, s1 int, text string) session.Item {
		return session.Item{ID: id, Kind: k, TurnID: tp, CreatedAt: at(s0), UpdatedAt: at(s1), Text: text, Model: &model}
	}
	tool := func(id, name string, s0, s1 int, c session.ToolCall) session.Item {
		it := item(id, session.KindToolCall, s0, s1, "")
		c.CallID, c.ToolName, c.StartedAt = id, name, at(s0)
		if c.Status != "running" {
			c.EndedAt = at(s1)
		}
		it.Tool = &c
		return it
	}
	in := func(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
	tasks := []session.Task{{Status: "completed", Subject: "Read the transcript parser"}, {Status: "in_progress", Subject: "Draw the checklist", ActiveForm: "Drawing the checklist"}, {Status: "pending", Subject: "Run the tests"}}
	content := []session.Item{
		{ID: "p0", Kind: session.KindSystem, Phase: session.PhaseCommand, Text: "/model claude-opus-5-5", CreatedAt: at(0), UpdatedAt: at(0)},
		{ID: "p0s", Kind: session.KindSystem, Phase: session.PhaseSetting, Text: "Switched to claude-opus-5-5", CreatedAt: at(0), UpdatedAt: at(0)},
		item("p1", session.KindUserPrompt, 1, 1, "Make the chat draw everything natively.\n\n<pasted_content id=\"ab12\">\nline one\nline two\nline three\n</pasted_content>"),
		item("th", session.KindReasoningText, 2, 14, "The transcript parser already has the stream's shapes; the work is in the panel. I should start with the task list."),
		tool("todo1", "TodoWrite", 15, 15, session.ToolCall{Status: "completed", Tasks: tasks[:1]}),
		tool("ag1", "Agent", 16, 40, session.ToolCall{Status: "completed", Input: in(map[string]string{"description": "Find every harness tag", "subagent_type": "Explore"}),
			Output:        "Found 9 tags: command-name, command-message, command-args, local-command-stdout, local-command-caveat, bash-input, bash-stdout, task-notification, system-reminder.\n\nEach comes in a user line.\nThe caveat is model-facing.",
			SubagentCalls: []session.ToolCall{{CallID: "g1", ToolName: "Grep", Status: "completed", Input: in(map[string]string{"pattern": "<command-"})}, {CallID: "r1", ToolName: "Read", Status: "completed", Input: in(map[string]string{"file_path": "transcript.go"})}}}),
		tool("ag2", "Agent", 16, 50, session.ToolCall{Status: "running", Progress: "Reading stream.go", Input: in(map[string]string{"description": "Check the stream-json shapes", "subagent_type": "general-purpose"}),
			SubagentCalls: []session.ToolCall{{CallID: "b1", ToolName: "Bash", Status: "completed", Input: in(map[string]string{"command": "go test ./..."})}, {CallID: "r2", ToolName: "Read", Status: "running", Input: in(map[string]string{"file_path": "stream.go"})}}}),
		{ID: "hk", Kind: session.KindSystem, Phase: session.PhaseHook, Text: "PreToolUse:Bash hook blocked: Blocked: forbidden commands are not allowed here", TurnID: tp, CreatedAt: at(41), UpdatedAt: at(41)},
		{ID: "rt", Kind: session.KindSystem, Phase: session.PhaseWarning, Text: "API error 529: Overloaded · retrying in 4s (attempt 2 of 10)", TurnID: tp, CreatedAt: at(42), UpdatedAt: at(42)},
		tool("todo2", "TodoWrite", 45, 45, session.ToolCall{Status: "completed", Tasks: tasks}),
	}
	plan := "## Plan\n\n1. Parse the harness tags in `harness.go`.\n2. Draw each as a native row.\n3. Test against live transcripts."
	return session.Snapshot{SessionID: "s", Provider: "claude", Content: content, SessionState: session.StateWaiting, Running: true, Model: &model,
		Turns:            []session.TurnSummary{{ID: turn, StartedAt: at(1), State: session.TurnRunning}},
		PendingApprovals: []session.Approval{{ID: "plan1", RequestType: session.RequestPlan, Detail: &plan, Request: &session.ToolRequest{ToolName: "ExitPlanMode"}}},
		PlanApproval:     &session.PlanApproval{ID: "plan1", Detail: &plan},
		PendingUserInputs: []session.InputRequest{{ID: "q1", Questions: []session.Question{
			{ID: "Which fruits?", Prompt: "Which fruits do you like?", Header: "Fruits", Multi: true, Options: []session.PromptOption{{ID: "Apple", Label: "Apple", Description: "Crisp"}, {ID: "Banana", Label: "Banana", Description: "Soft"}}},
			{ID: "Which layout?", Prompt: "Which layout?", Header: "Layout", Options: []session.PromptOption{
				{ID: "Sidebar", Label: "Sidebar", Description: "Navigation on the left", Preview: "+------+--------+\n| nav  | body   |\n|      |        |\n+------+--------+"},
				{ID: "Top bar", Label: "Top bar", Preview: "+---------------+\n| nav           |\n| body          |\n+---------------+"}}}}}},
		UsageLimit: &session.UsageLimit{Label: "Weekly limit", ResetsAt: ptr(at(59))},
	}
}

func ptr[T any](v T) *T { return &v }

// drawSnap lays s out in a w by h window and returns the screenshot, nil
// without a GPU.
func drawSnap(t *testing.T, th *theme.Theme, s session.Snapshot, w, h int, frames int) *image.RGBA {
	t.Helper()
	win, err := headless.NewWindow(w, h)
	if err != nil {
		t.Skip(err)
	}
	defer win.Release()
	tr := &Transcript{Width: 760}
	tr.init()
	for _, it := range s.Content { // every fold open, to see what is inside
		for _, k := range []string{"log:", "compact:", "calls:", ""} {
			tr.open[k+it.ID] = true
		}
	}
	now := time.Date(2026, 11, 19, 20, 0, 55, 0, time.UTC)
	var ops op.Ops
	for range frames {
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Now: now, Constraints: layout.Exact(image.Pt(w, h))}
		tr.list.ScrollToEnd = false
		layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return tr.Layout(gtx, th, &s, false) }),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return tr.TodoStrip(gtx, th, &s) }),
		)
		if err := win.Frame(&ops); err != nil {
			t.Fatal(err)
		}
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if err := win.Screenshot(img); err != nil {
		t.Fatal(err)
	}
	return img
}

// sendersSnap is prompts pitwall typed from other agents and a script,
// one of them the way Claude Code 2.1 recorded it, as pasted content.
func sendersSnap() session.Snapshot {
	at := session.Stamp(time.Date(2026, 11, 19, 20, 0, 0, 0, time.UTC))
	s := session.Snapshot{SessionID: "s", Provider: "claude", SessionState: session.StateReady}
	for i, text := range []string{
		"[pitwall: message from codex in #6.2; reply with: pitwall send @41d2e7c06a9f3b28 \"...\"]\nRebased onto main, the tests pass. Can you review the diff?",
		"\n\n<pasted_content id=\"44ff\">\n[pitwall: message from claude in #3.1; reply with: pitwall send @e0ef \"...\"]\nchat-system pushed 7a504cff\nCI is green\nmerge when ready\n</pasted_content id=\"44ff\">\n",
		"[pitwall: message from a script]\nThe nightly build is green.",
	} {
		s.Content = append(s.Content, session.Item{ID: "p" + strconv.Itoa(i), Kind: session.KindUserPrompt, CreatedAt: at, UpdatedAt: at, Text: text})
	}
	return s
}

// TestDrawHarnessItems lays out live transcripts and a turn holding every
// item this package draws, without a panic. With PITWALL_RENDER_DIR set
// it saves each frame there as a PNG to look at.
func TestDrawHarnessItems(t *testing.T) {
	c, _ := config.Builtin("aide-dark")
	th, err := theme.New(c, config.Font{UISize: 13})
	if err != nil {
		t.Fatal(err)
	}
	snaps := map[string]session.Snapshot{"rich": richSnap(), "senders": sendersSnap()}
	for _, name := range []string{"tui-commands", "tui-tasks", "print-compact"} {
		snaps[name] = renderSnap(t, filepath.Join("..", "..", "session", "claude", "testdata", "transcripts", name+".jsonl"))
	}
	dir := os.Getenv("PITWALL_RENDER_DIR")
	for name, s := range snaps {
		h := 3000
		if n, err := strconv.Atoi(os.Getenv("PITWALL_RENDER_H")); err == nil {
			h = n
		}
		img := drawSnap(t, th, s, 1000, h, 3)
		if dir == "" {
			continue
		}
		f, err := os.Create(filepath.Join(dir, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		png.Encode(f, img)
		f.Close()
	}
}
