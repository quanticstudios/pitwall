package panel

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// liveText is the markdown a pane chat reads off fixture name, one of
// internal/agent's recorded screens.
func liveText(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../../agent/testdata/" + name + ".txt")
	if err != nil {
		t.Fatal(err)
	}
	e := vt.New(100, 30, nil)
	e.Write([]byte(strings.ReplaceAll(strings.TrimRight(string(b), "\n"), "\n", "\r\n")))
	p := model.Provider(strings.SplitN(name, "-", 2)[0])
	lv := agent.LiveScreen(p, e.Snapshot())
	return agent.LiveMarkdown(p, lv.Cells, lv.Width)
}

// liveReplySnap is a chat whose running turn's reply is text, in item id:
// the screen's live one or the transcript's.
func liveReplySnap(p model.Provider, id, text string) *session.Snapshot {
	turn := "t1"
	at := time.Date(2026, 11, 26, 22, 24, 0, 0, time.UTC).Format(time.RFC3339Nano)
	return &session.Snapshot{SessionID: "s1", Provider: p, SessionState: session.StateRunning, Running: true,
		Turns: []session.TurnSummary{{ID: turn, StartedAt: at, State: session.TurnRunning}},
		Content: []session.Item{
			{ID: "p1", Kind: session.KindUserPrompt, Text: "Reply with a markdown sample.", TurnID: &turn, CreatedAt: at, UpdatedAt: at},
			{ID: id, Kind: session.KindAssistantText, Text: text, TurnID: &turn, Provider: &p, CreatedAt: at, UpdatedAt: at},
		}}
}

// liveFrame draws s in tr at w x h, three frames for the list to settle.
func liveFrame(t *testing.T, tr *Transcript, s *session.Snapshot, w, h int) *image.RGBA {
	t.Helper()
	win, err := headless.NewWindow(w, h)
	if err != nil {
		t.Skip(err)
	}
	defer win.Release()
	th := theme.Dark()
	var ops op.Ops
	now := time.Date(2026, 11, 26, 22, 25, 0, 0, time.UTC)
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

// When the transcript's message replaces the live reply read off Claude
// Code's screen, the chat's length changes by no more than a heading's
// size, which a bold line on screen cannot tell, and it stays at its end.
func TestLiveReplySwap(t *testing.T) {
	for _, tc := range []struct {
		name string
		max  int // px the two may differ by
	}{
		{"claude-md-prose", 16},   // a ## the screen draws bold
		{"claude-md-blocks", 0},   // the fence's language
		{"claude-md-headings", 8}, // a ### and a #### the screen draws bold
	} {
		landed, err := os.ReadFile("../../agent/testdata/livemd/" + tc.name + ".landed.md")
		if err != nil {
			t.Fatal(err)
		}
		tr := &Transcript{Dir: "/repo", ChatLayout: LayoutColumn, ToolCalls: ToolsAll, Width: 760}
		liveFrame(t, tr, liveReplySnap(model.ProviderClaude, "live-reply", liveText(t, tc.name)), 1000, 300)
		live := tr.list.Position.Length
		liveFrame(t, tr, liveReplySnap(model.ProviderClaude, "a1", strings.TrimSuffix(string(landed), "\n")), 1000, 300)
		got := tr.list.Position.Length
		t.Logf("%s: %d px live, %d landed", tc.name, live, got)
		if d := max(got-live, live-got); d > tc.max || !tr.list.ScrollToEnd || tr.list.Position.BeforeEnd {
			t.Errorf("%s: %d px live, %d landed; at the end %v", tc.name, live, got, tr.list.ScrollToEnd && !tr.list.Position.BeforeEnd)
		}
	}
}

// TestLiveReplyShots renders each TUI's live reply, read off its recorded
// screen, and Claude Code's transcript message for the same reply, to
// $PITWALL_LIVEMD_SHOTS for the PR.
func TestLiveReplyShots(t *testing.T) {
	dir := os.Getenv("PITWALL_LIVEMD_SHOTS")
	if dir == "" {
		t.Skip("set PITWALL_LIVEMD_SHOTS to render the PNGs")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	shot := func(file string, s *session.Snapshot) {
		tr := &Transcript{Dir: "/repo", ChatLayout: LayoutColumn, ToolCalls: ToolsAll, Width: 760}
		img := liveFrame(t, tr, s, 900, 820)
		f, err := os.Create(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []model.Provider{model.ProviderClaude, model.ProviderCodex, model.ProviderAntigravity} {
		for _, kind := range []string{"prose", "blocks"} {
			name := string(p) + "-md-" + kind
			shot("live-"+name+".png", liveReplySnap(p, "live-reply", liveText(t, name)))
		}
	}
	for _, kind := range []string{"prose", "blocks"} {
		landed, err := os.ReadFile("../../agent/testdata/livemd/claude-md-" + kind + ".landed.md")
		if err != nil {
			t.Fatal(err)
		}
		shot("landed-claude-md-"+kind+".png", liveReplySnap(model.ProviderClaude, "a1", string(landed)))
	}
}
