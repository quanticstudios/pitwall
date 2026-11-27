package panel

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// reportChat is an orchestrator's chat of two turns with three worker
// cards: two between the turns, one after the last.
func reportChat() (*session.Snapshot, []model.Report) {
	at := func(min int) time.Time { return time.Date(2026, 11, 27, 14, min, 0, 0, time.UTC) }
	ts := func(min int) string { return at(min).Format(time.RFC3339Nano) }
	t1, t2 := "t1", "t2"
	s := &session.Snapshot{SessionID: "s1", Provider: model.ProviderClaude, SessionState: session.StateReady, Content: []session.Item{
		{ID: "p1", Kind: session.KindUserPrompt, Text: "Run the fleet on the chat redesign.", TurnID: &t1, CreatedAt: ts(1), UpdatedAt: ts(1)},
		{ID: "r1", Kind: session.KindAssistantText, Text: "Started three workers: phone-polish, since-you-left and chat-type.", TurnID: &t1, CreatedAt: ts(2), UpdatedAt: ts(2)},
		{ID: "p2", Kind: session.KindUserPrompt, Text: "Where is since-you-left stuck?", TurnID: &t2, CreatedAt: ts(30), UpdatedAt: ts(30)},
		{ID: "r2", Kind: session.KindAssistantText, Text: "It needs the card's placement. I asked you in the inbox.", TurnID: &t2, CreatedAt: ts(31), UpdatedAt: ts(31)},
	}}
	reps := []model.Report{
		{Tab: "w1", Pane: "a1", Name: "phone-polish", PR: 566, Status: model.ReportDone, Turn: 1, At: at(20),
			Line: "4 gaps fixed and 11 found in the audit. Suite green with the network blocked.", Body: "4 gaps fixed and 11 found in the audit.\nSuite green with the network blocked."},
		{Tab: "w2", Pane: "a2", Name: "since-you-left", Status: model.ReportBlocked, Turn: 2, At: at(24), Line: "Needs the card's placement before it builds the UI."},
		{Tab: "w3", Pane: "a3", Name: "chat-type", Status: model.ReportProgress, Turn: 1, At: at(40), Line: "Reply prose moved to the 14 sp scale; tool rows next."},
	}
	return s, reps
}

// reportFrame draws tr over s, w by h px at scale 1.
func reportFrame(t *testing.T, th *theme.Theme, tr *Transcript, s *session.Snapshot, w, h int) *image.RGBA {
	t.Helper()
	win, err := headless.NewWindow(w, h)
	if err != nil {
		t.Skip(err)
	}
	defer win.Release()
	var ops op.Ops
	now := time.Date(2026, 11, 27, 14, 45, 0, 0, time.UTC)
	for range 3 {
		ops.Reset()
		now = now.Add(16 * time.Millisecond)
		gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(w, h)), Now: now}
		tr.Layout(gtx, th, s, false)
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

// TestReportRows checks the cards sit in the log by time, in both
// layouts: the first two after the first turn, the third after the last.
func TestReportRows(t *testing.T) {
	s, reps := reportChat()
	for _, lay := range []string{LayoutLedger, LayoutColumn} {
		tr := &Transcript{Dir: "/repo", ChatLayout: lay, ToolCalls: ToolsAll, Width: 760, Reports: reps}
		reportFrame(t, theme.Dark(), tr, s, 1000, 700)
		var order []string
		for _, r := range tr.rows {
			switch {
			case r.rep != nil:
				order = append(order, r.rep.Name)
			case !r.isTail() && r.res == nil && r.b.kind == blockPrompt:
				order = append(order, s.Content[r.b.lo].ID)
			}
		}
		want := []string{"p1", "phone-polish", "since-you-left", "p2", "chat-type"}
		if len(order) != len(want) {
			t.Fatalf("%s: rows %v, want %v", lay, order, want)
		}
		for i := range want {
			if order[i] != want[i] {
				t.Fatalf("%s: rows %v, want %v", lay, order, want)
			}
		}
	}
}

// TestReportCardOpens checks a click asks the window to open the tab,
// and opens the report in place once that tab is gone.
func TestReportCardOpens(t *testing.T) {
	_, reps := reportChat()
	var asked []string
	live := true
	tr := &Transcript{OpenReport: func(r model.Report) bool { asked = append(asked, r.Tab); return live }}
	tr.init()
	key := "report:" + reps[0].Key()
	click := func() {
		c := tr.click(key)
		c.Click()
		var ops op.Ops
		gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(600, 400))}
		d := &cdraw{t: tr, th: theme.Dark()}
		d.reportCard(gtx, &reps[0])
	}
	click()
	if len(asked) != 1 || asked[0] != "w1" || tr.open[key] {
		t.Fatalf("asked %v, open %v: want the tab opened, the card closed", asked, tr.open[key])
	}
	live = false
	click()
	if !tr.open[key] {
		t.Fatal("the card did not open in place with its tab gone")
	}
}

// TestReportShots renders the three cards in Ledger and Column at 1440
// px, dark, to PITWALL_REPORT_SHOTS for the PR.
func TestReportShots(t *testing.T) {
	dir := os.Getenv("PITWALL_REPORT_SHOTS")
	if dir == "" {
		t.Skip("set PITWALL_REPORT_SHOTS to render report card PNGs")
	}
	s, reps := reportChat()
	for _, c := range []struct {
		name, lay string
		reps      []model.Report
	}{
		{"reports-ledger-1440.png", LayoutLedger, reps}, {"reports-column-1440.png", LayoutColumn, reps},
		{"reports-ledger-1440-before.png", LayoutLedger, nil}, {"reports-column-1440-before.png", LayoutColumn, nil},
	} {
		tr := &Transcript{Dir: "/repo", ChatLayout: c.lay, ToolCalls: ToolsAll, Width: 760, Reports: c.reps}
		img := reportFrame(t, theme.Dark(), tr, s, 1440, 640)
		f, err := os.Create(filepath.Join(dir, c.name))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}
