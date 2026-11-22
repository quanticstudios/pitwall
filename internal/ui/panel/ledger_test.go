package panel

import (
	"encoding/json"
	"image"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/chatin"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func ledgerSnapshot() *session.Snapshot {
	in := func(m map[string]any) json.RawMessage { b, _ := json.Marshal(m); return b }
	at := func(min, sec int) string {
		return time.Date(2026, 11, 21, 14, min, sec, 0, time.UTC).Format(time.RFC3339Nano)
	}
	turn1, turn2 := "t1", "t2"
	zero, one := 0, 1
	diff := "@@ -10,4 +10,6 @@\n \timg, err := decode(b)\n-\treturn nil\n+\timg = fit(img, 2048)\n+\treturn img\n"
	tasks := []session.Task{
		{Subject: "Find where large pastes drop", Status: "completed"},
		{Subject: "Resize to 2048 px", Status: "completed"},
		{Subject: "Run tests", Status: "completed"},
		{Subject: "Commit changes", Status: "completed"},
		{Subject: "Push the branch", ActiveForm: "Pushing the branch", Status: "in_progress"},
	}
	return &session.Snapshot{
		SessionID:    "s1",
		Provider:     model.ProviderClaude,
		SessionState: session.StateWaiting,
		Running:      true,
		Content: []session.Item{
			{ID: "p1", Kind: session.KindUserPrompt, Text: "Fix large pasted images.", TurnID: &turn1, CreatedAt: at(1, 0), UpdatedAt: at(1, 0)},
			{ID: "c1", Kind: session.KindToolCall, TurnID: &turn1, CreatedAt: at(1, 2), UpdatedAt: at(1, 3),
				Tool: &session.ToolCall{CallID: "c1", ToolName: "Read", Status: "completed", Input: in(map[string]any{"file_path": "/repo/composer.go"}), Output: "line1\nline2\n"}},
			{ID: "c2", Kind: session.KindToolCall, TurnID: &turn1, CreatedAt: at(1, 5), UpdatedAt: at(1, 6),
				Tool: &session.ToolCall{CallID: "c2", ToolName: "Edit", Status: "completed", Input: in(map[string]any{"file_path": "/repo/composer.go"}), Diff: diff}},
			{ID: "c3", Kind: session.KindToolCall, TurnID: &turn1, CreatedAt: at(1, 8), UpdatedAt: at(1, 10),
				Tool: &session.ToolCall{CallID: "c3", ToolName: "Bash", Status: "failed", Input: in(map[string]any{"command": "go vet ./..."}), ExitCode: &one, Output: "vet failed"}},
			{ID: "c4", Kind: session.KindToolCall, TurnID: &turn1, CreatedAt: at(1, 12), UpdatedAt: at(1, 15),
				Tool: &session.ToolCall{CallID: "c4", ToolName: "Bash", Status: "completed", Input: in(map[string]any{"command": "go test ./..."}), ExitCode: &zero, Output: "ok", StartedAt: at(1, 12), EndedAt: at(1, 15)}},
			{ID: "td1", Kind: session.KindToolCall, TurnID: &turn1, CreatedAt: at(1, 16), UpdatedAt: at(1, 16),
				Tool: &session.ToolCall{CallID: "td1", ToolName: "TodoWrite", Status: "completed", Tasks: tasks}},
			{ID: "r1", Kind: session.KindAssistantText, Text: "Resized large pastes to 2048 px.\n\n```go\nfunc fit() {}\n```", TurnID: &turn1, CreatedAt: at(1, 20), UpdatedAt: at(1, 20)},
			{ID: "p2", Kind: session.KindUserPrompt, Text: "Now push the branch.", TurnID: &turn2, CreatedAt: at(3, 0), UpdatedAt: at(3, 0)},
			{ID: "r2", Kind: session.KindAssistantText, Text: "Pushing next.", TurnID: &turn2, CreatedAt: at(3, 2), UpdatedAt: at(3, 2)},
		},
		PendingApprovals: []session.Approval{{
			ID:          "a1",
			RequestType: session.RequestCommand,
			Request:     &session.ToolRequest{ToolName: "Bash", Command: "git push -u origin fix/images"},
		}},
	}
}

func TestLedgerGeometry(t *testing.T) {
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}

	if got := ledgerMetrics(gtx, model.ProviderClaude, 3440).gutterW; got != 44+12+58+12 {
		t.Fatalf("claude gutterW = %d, want 126", got)
	}
	agy, cl := ledgerMetrics(gtx, model.ProviderAntigravity, 3440), ledgerMetrics(gtx, model.ProviderClaude, 3440)
	if agy.nameW <= cl.nameW || agy.textW != 768 {
		t.Fatalf("agy nameW = %d (claude %d), textW = %d, want wider name and 768", agy.nameW, cl.nameW, agy.textW)
	}
	for _, prov := range []model.Provider{model.ProviderClaude, model.ProviderAntigravity} {
		for _, w := range []int{320, 560, 648, 760, 1000, 1280, 1600, 3440} {
			g := ledgerMetrics(gtx, prov, w)
			// The composer card's column: its left and right edges are the text's.
			cx, cw, _ := chatin.LedgerColumn(gtx, w, int(LedgerGutter(prov)))
			if g.textX != cx || g.textW != cw {
				t.Fatalf("%s at %d: text %d+%d, composer %d+%d", prov, w, g.textX, g.textW, cx, cw)
			}
			if off := w - 2*g.textX - g.textW; g.textW > 768 || g.textW <= 0 || off != 0 && off != 1 {
				t.Fatalf("%s at %d: text %d+%d not centered or out of 1..768", prov, w, g.textX, g.textW)
			}
			// Nothing clips: the gutter and the text stay in the pane.
			if g.x0 < 0 || g.x0+g.ledgerW > w {
				t.Fatalf("%s at %d: ledger %d+%d leaves the pane", prov, w, g.x0, g.ledgerW)
			}
			if g.outside {
				if g.x0 != g.textX-g.gutterW || g.x0 < gtx.Dp(theme.SpaceXL) {
					t.Fatalf("%s at %d: gutter at %d, text at %d", prov, w, g.x0, g.textX)
				}
			} else if g.x0 != g.textX || g.ledgerW != g.textW {
				t.Fatalf("%s at %d: gutter above the text should share its span", prov, w)
			}
		}
	}
	// Wide panes cap the text at 768 dp with the gutter beside it; narrow
	// ones move the gutter above the text.
	for _, c := range []struct {
		w       int
		outside bool
		textW   int
	}{{3440, true, 768}, {1600, true, 768}, {1000, true, 1000 - 2*(28+126)}, {760, true, 760 - 2*(28+126)}, {700, false, 700 - 56}, {560, false, 560 - 56}} {
		g := ledgerMetrics(gtx, model.ProviderClaude, c.w)
		if g.outside != c.outside || g.textW != c.textW {
			t.Fatalf("at %d: outside=%v textW=%d, want %v %d", c.w, g.outside, g.textW, c.outside, c.textW)
		}
	}
}

func TestLedgerRowsAndToolVisibility(t *testing.T) {
	th := theme.Dark()
	s := ledgerSnapshot()

	runMode := func(mode string) *Transcript {
		tr := &Transcript{Dir: "/repo", ChatLayout: LayoutLedger, ToolCalls: mode}
		var ops op.Ops
		gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(1100, 900)), Now: time.Now()}
		tr.Layout(gtx, th, s, false)
		return tr
	}

	// All: each tool call is its own segItem row, no segHead or segTurn folds.
	all := runMode(ToolsAll)
	items, heads, turns, gutters := 0, 0, 0, 0
	for _, r := range all.rows {
		switch r.seg {
		case segItem:
			items++
		case segHead:
			heads++
		case segTurn:
			turns++
		}
		if r.gutter {
			gutters++
		}
		if ledgerPromptRow(&r) && (!r.gutter || r.inTurn) {
			t.Fatalf("prompt row should have a gutter and stay out of the turn: %+v", r)
		}
	}
	if items != 4 || heads != 0 || turns != 0 {
		t.Fatalf("ToolsAll: items=%d heads=%d turns=%d, want 4/0/0", items, heads, turns)
	}
	if gutters != 4 {
		t.Fatalf("ToolsAll: gutters=%d, want 4 (one per prompt and per agent turn)", gutters)
	}

	// Failed: keeps only the failed Bash call (c3).
	failed := runMode(ToolsFailed)
	failedItems := 0
	for _, r := range failed.rows {
		if r.seg == segItem {
			failedItems++
		}
	}
	if failedItems != 1 {
		t.Fatalf("ToolsFailed: items=%d, want 1", failedItems)
	}

	// None: hides all calls.
	none := runMode(ToolsNone)
	for _, r := range none.rows {
		if r.seg == segItem {
			t.Fatalf("ToolsNone: unexpected segItem %d", r.i)
		}
	}

	// Zen: prompts + final replies only (plus pending approval).
	zen := runMode(ToolsZen)
	for _, r := range zen.rows {
		if r.seg == segItem {
			t.Fatalf("ToolsZen: unexpected segItem %d", r.i)
		}
	}

	// TodoStrip is suppressed in Ledger (PlanStrip draws under the header instead).
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(1100, 900)), Now: time.Now()}
	if d := all.TodoStrip(gtx, th, s); d.Size != (image.Point{}) {
		t.Fatalf("TodoStrip in Ledger = %v, want zero", d.Size)
	}
}

func TestLedgerPlanStripAndApproval(t *testing.T) {
	th := theme.Dark()
	s := ledgerSnapshot()
	tr := &Transcript{Dir: "/repo", ChatLayout: LayoutLedger}
	var ops op.Ops
	frame := func() {
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(1100, 900)), Now: time.Now()}
		tr.Layout(gtx, th, s, false)
	}
	frame()

	// Plan strip sees both tasks and changed files.
	if got := latestTodo(s); len(got) != 5 {
		t.Fatalf("latestTodo = %d, want 5", len(got))
	}
	d := &cdraw{t: tr, th: th, s: s}
	if _, fs := tr.latestChanges(d); len(fs) != 1 {
		t.Fatalf("latestChanges = %d, want 1", len(fs))
	}

	// Clicking the plan strip's changes button opens the Changes side panel.
	tr.click("plan-strip:changes").Click()
	frame()
	if !tr.FilesOpen() {
		t.Fatal("clicking plan strip changes did not open the side panel")
	}

	// Number key 1 approves the compact Ledger approval card.
	if !tr.Key("1") {
		t.Fatal("Key(1) did not approve pending request in Ledger")
	}
	ans := tr.Answers()
	if len(ans) != 1 || ans[0].Request != "a1" || ans[0].Decision != "allow" {
		t.Fatalf("Answers = %+v", ans)
	}
}

func TestLayoutSwitchKeepsScroll(t *testing.T) {
	th := theme.Dark()
	at := func(i int) string {
		return time.Date(2026, 11, 21, 14, i/60, i%60, 0, time.UTC).Format(time.RFC3339Nano)
	}
	s := &session.Snapshot{SessionID: "s1", Provider: model.ProviderClaude, SessionState: session.StateReady}
	for i := range 20 {
		turn := "t" + string(rune('a'+i))
		s.Content = append(s.Content,
			session.Item{ID: "p" + turn, Kind: session.KindUserPrompt, Text: "Prompt " + turn, TurnID: &turn, CreatedAt: at(i * 3)},
			session.Item{ID: "r" + turn, Kind: session.KindAssistantText, Text: "Reply paragraph for " + turn + "\n\nMore details.", TurnID: &turn, CreatedAt: at(i*3 + 1), UpdatedAt: at(i*3 + 2)},
		)
	}
	tr := &Transcript{Dir: "/repo", ChatLayout: LayoutColumn}
	var ops op.Ops
	draw := func() {
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(900, 400)), Now: time.Now()}
		tr.Layout(gtx, th, s, false)
	}
	draw()
	draw()
	// Scroll up to an earlier item.
	tr.list.Position.First = 8
	tr.list.Position.BeforeEnd = true
	draw()
	wantAnchor := tr.anchor
	if wantAnchor == "" {
		t.Fatal("expected anchor to be recorded while scrolled up")
	}

	// Switch to Ledger live: stays scrolled up on the same anchored item.
	tr.ChatLayout = LayoutLedger
	draw()
	if !tr.list.Position.BeforeEnd {
		t.Fatal("switching to Ledger jumped to end instead of keeping scroll")
	}
	if tr.anchor != wantAnchor {
		t.Fatalf("anchor after switch to Ledger = %q, want %q", tr.anchor, wantAnchor)
	}

	// Switch back to Column: keeps the same anchored item.
	tr.ChatLayout = LayoutColumn
	draw()
	if !tr.list.Position.BeforeEnd || tr.anchor != wantAnchor {
		t.Fatalf("after switch back: BeforeEnd=%v anchor=%q, want true/%q", tr.list.Position.BeforeEnd, tr.anchor, wantAnchor)
	}
}
