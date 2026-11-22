package app

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

type agyQueueBackend struct {
	*FakeBackend
	snap session.Snapshot
}

func (b agyQueueBackend) Chat(string) (session.Snapshot, bool) { return b.snap, true }

// TestAgyHeldQueueShots renders headless PNGs of an Antigravity pane chat
// showing a message held while a menu is open in the terminal and a failed
// message with Retry into $PITWALL_AGY_SHOTS; it skips when unset.
func TestAgyHeldQueueShots(t *testing.T) {
	dir := os.Getenv("PITWALL_AGY_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_AGY_SHOTS is not set")
	}
	old := findAide
	findAide = func() []string { return nil }
	t.Cleanup(func() { findAide = old })

	at := func(s int) string { return time.Date(2026, 11, 22, 18, 0, s, 0, time.UTC).Format(time.RFC3339Nano) }
	turn := "t1"
	prov := model.ProviderAntigravity
	base := session.Snapshot{
		SessionID:      "h",
		PaneID:         "h",
		WorkspaceID:    "w6",
		Provider:       model.ProviderAntigravity,
		SessionState:   session.StateRunning,
		Running:        true,
		PermissionMode: "auto",
		Content: []session.Item{
			{ID: "p1", Kind: session.KindUserPrompt, Text: "Draft a 5-step plan for the CLI and check edge cases.", TurnID: &turn, Provider: &prov, CreatedAt: at(0)},
			{ID: "r1", Kind: session.KindAssistantText, Text: "1. Define CLI flags and expression syntax.\n2. Implement tokenizer for numbers and operators.\n3. Parse and evaluate with precedence.\n4. Guard division by zero.\n5. Add unit and integration tests.", TurnID: &turn, Provider: &prov, CreatedAt: at(12), UpdatedAt: at(12)},
		},
	}

	cases := []struct {
		name   string
		reason string
		queued []session.QueuedInput
	}{
		{
			name:   "agy-held-menu",
			reason: "Waiting: a menu is open in the agent's terminal",
			queued: []session.QueuedInput{{ID: "q1", Text: "Also include a division-by-zero test case.", CreatedAt: at(20)}},
		},
		{
			name: "agy-failed-retry",
			queued: []session.QueuedInput{
				{ID: "q1", Text: "Also include a division-by-zero test case.", Error: "pane h is not reading its input", CreatedAt: at(20)},
			},
		},
	}

	size := image.Pt(1100, 760)
	for _, tc := range cases {
		snap := base
		snap.QueueReason = tc.reason
		snap.QueuedInputs = tc.queued
		b := agyQueueBackend{NewFakeBackend(), snap}
		i := slices.IndexFunc(b.st.Panes, func(p model.Pane) bool { return p.ID == "h" })
		b.st.Panes[i].Provider, b.st.Panes[i].Transcript = model.ProviderAntigravity, "/tmp/transcript.jsonl"
		u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: aide}}
		u.cfg.Font.UISize, u.cfg.Font.ChatSize = 13, 13
		u.cfg.Chat.Claude, u.cfg.Chat.Codex, u.cfg.Chat.Antigravity = true, true, true
		st := b.State()
		u.nav.sync(&st)
		u.nav.selectWorkspace(&st, "w6", "")
		u.nav.setFocus("h")

		win, err := headless.NewWindow(size.X, size.Y)
		if err != nil {
			t.Skip("no headless GPU:", err)
		}
		var r input.Router
		var ops op.Ops
		for range 4 {
			ops.Reset()
			u.layout(gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
				Constraints: gl.Exact(size), Now: time.Now()})
			r.Frame(&ops)
			time.Sleep(100 * time.Millisecond)
		}
		if err := win.Frame(&ops); err != nil {
			t.Fatal(err)
		}
		out := image.NewRGBA(image.Rectangle{Max: size})
		if err := win.Screenshot(out); err != nil {
			t.Fatal(err)
		}
		win.Release()
		f, err := os.Create(filepath.Join(dir, tc.name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, out); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}
