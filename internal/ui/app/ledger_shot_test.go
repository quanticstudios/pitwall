package app

import (
	"encoding/json"
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

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/panel"
	"github.com/quanticstudios/pitwall/internal/ui/settings"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

type ledgerShotBackend struct {
	*FakeBackend
	snap session.Snapshot
}

func (b ledgerShotBackend) Chat(string) (session.Snapshot, bool) { return b.snap, true }

func ledgerBaseSnap(prov model.Provider, state string, running bool) session.Snapshot {
	in := func(m map[string]any) json.RawMessage { b, _ := json.Marshal(m); return b }
	at := func(min, sec int) string {
		return time.Date(2026, 11, 21, 14, min, sec, 0, time.UTC).Format(time.RFC3339Nano)
	}
	turn1, turn2 := "t1", "t2"
	zero := 0
	used, maxTok := int64(71_000), int64(200_000)
	mdl, eff := "claude-opus-5-5", "high"
	if prov == model.ProviderCodex {
		mdl, eff = "gpt-5.4", "medium"
	} else if prov == model.ProviderAntigravity {
		mdl, eff = "gemini-4-neon", "high"
	}
	diff1 := "@@ -412,7 +412,10 @@ func (c *composer) paste\n \timg, err := decodeClip(b)\n-\tif len(b) > maxPaste {\n-\t\treturn nil\n+\tif len(b) > maxPaste {\n+\t\timg = fitLongSide(img, 2048)\n+\t\tb, err = encodeJPEG(img, 85)\n \t}\n"
	diff2 := "@@ -88,4 +88,6 @@ func (c *camera) capture\n \traw := takePhoto()\n-\treturn raw\n+\timg := fitLongSide(raw, 2048)\n+\treturn encodeJPEG(img, 85)\n"
	reply := "The drop happens in `composer.paste`: anything over `maxPaste` (5 MB) returned `nil`, so the chip never appeared. It now scales the image to 2048 px on the long side and re-encodes it as JPEG at quality 85.\n\n" +
		"```go\nfunc fitLongSide(m image.Image, side int) image.Image {\n\tlong := max(m.Bounds().Dx(), m.Bounds().Dy())\n\tif long > side {\n\t\t// scale by side/long; CatmullRom keeps screenshot text legible\n\t\treturn scaleImage(m, side)\n\t}\n\treturn m\n}\n```\n\n" +
		"| Path | Limit | Action |\n| --- | --- | --- |\n| `mobile/composer.go` | 5 MB | Resize to 2048 px |\n| `mobile/camera.go` | 5 MB | Same `fitLongSide` path |\n| `mobile/image.go` | JPEG 85 | Keep text legible |"
	tasks := []session.Task{
		{Subject: "Find where large pastes are dropped", Status: "completed"},
		{Subject: "Resize to 2048 px instead of dropping", Status: "completed"},
		{Subject: "Same path for camera photos", Status: "completed"},
		{Subject: "Run the full test suite", Status: "completed"},
		{Subject: "Push the branch", ActiveForm: "Push the branch", Status: "in_progress"},
	}
	s := session.Snapshot{
		SessionID:      "c1",
		Provider:       prov,
		SessionState:   state,
		Running:        running,
		PermissionMode: "acceptEdits",
		Model:          &mdl,
		Effort:         &eff,
		Usage:          &session.Usage{ContextTokens: &used, MaxTokens: &maxTok},
		UsageReports: []session.UsageReport{{
			TurnID: &turn1,
			Models: []session.ModelUsage{{InputTokens: 9800, OutputTokens: 2600}},
		}},
		Content: []session.Item{
			{ID: "p1", Kind: session.KindUserPrompt, Text: "The phone composer drops pasted images over 5 MB without saying anything. Find where, and resize them instead of dropping.", TurnID: &turn1, CreatedAt: at(1, 0), UpdatedAt: at(1, 0)},
			{ID: "c1", Kind: session.KindToolCall, TurnID: &turn1, CreatedAt: at(1, 2), UpdatedAt: at(1, 3),
				Tool: &session.ToolCall{CallID: "c1", ToolName: "Read", Status: "completed", Input: in(map[string]any{"file_path": "/repo/internal/ui/mobile/composer.go"}), Output: "package mobile\n"}},
			{ID: "c2", Kind: session.KindToolCall, TurnID: &turn1, CreatedAt: at(1, 4), UpdatedAt: at(1, 5),
				Tool: &session.ToolCall{CallID: "c2", ToolName: "Grep", Status: "completed", Input: in(map[string]any{"pattern": "maxPaste", "path": "internal/"}), Output: "a\nb\nc\n"}},
			{ID: "c3", Kind: session.KindToolCall, TurnID: &turn1, CreatedAt: at(1, 8), UpdatedAt: at(1, 10),
				Tool: &session.ToolCall{CallID: "c3", ToolName: "Edit", Status: "completed", Input: in(map[string]any{"file_path": "/repo/internal/ui/mobile/composer.go"}), Diff: diff1}},
			{ID: "c4", Kind: session.KindToolCall, TurnID: &turn1, CreatedAt: at(1, 12), UpdatedAt: at(1, 14),
				Tool: &session.ToolCall{CallID: "c4", ToolName: "Bash", Status: "completed", Input: in(map[string]any{"command": "go test ./internal/ui/mobile/"}), ExitCode: &zero, Output: "ok\tinternal/ui/mobile\t1.9s", StartedAt: at(1, 12), EndedAt: at(1, 14)}},
			{ID: "td1", Kind: session.KindToolCall, TurnID: &turn1, CreatedAt: at(1, 15), UpdatedAt: at(1, 15),
				Tool: &session.ToolCall{CallID: "td1", ToolName: "TodoWrite", Status: "completed", Tasks: tasks}},
			{ID: "r1", Kind: session.KindAssistantText, Text: reply, TurnID: &turn1, CreatedAt: at(1, 48), UpdatedAt: at(1, 48)},
			{ID: "p2", Kind: session.KindUserPrompt, Text: "Good. Also cap phone photos the same way and run the whole suite, then push.", TurnID: &turn2, CreatedAt: at(3, 0), UpdatedAt: at(3, 0)},
			{ID: "c5", Kind: session.KindToolCall, TurnID: &turn2, CreatedAt: at(3, 4), UpdatedAt: at(3, 6),
				Tool: &session.ToolCall{CallID: "c5", ToolName: "Edit", Status: "completed", Input: in(map[string]any{"file_path": "/repo/internal/ui/mobile/camera.go"}), Diff: diff2}},
			{ID: "c6", Kind: session.KindToolCall, TurnID: &turn2, CreatedAt: at(3, 8), UpdatedAt: at(3, 46),
				Tool: &session.ToolCall{CallID: "c6", ToolName: "Bash", Status: "completed", Input: in(map[string]any{"command": "go test ./..."}), ExitCode: &zero, Output: "ok", StartedAt: at(3, 8), EndedAt: at(3, 46)}},
			{ID: "r2", Kind: session.KindAssistantText, Text: "All packages pass and the change is committed. Pushing the branch next.", TurnID: &turn2, CreatedAt: at(3, 50), UpdatedAt: at(3, 50)},
		},
	}
	return s
}

func TestChatLayoutLiveSwitch(t *testing.T) {
	s := ledgerBaseSnap(model.ProviderClaude, session.StateReady, false)
	b := ledgerShotBackend{NewFakeBackend(), s}
	b.st.Workspaces = append(b.st.Workspaces, model.Workspace{ID: "wc", SessionID: "s1", Name: "chat", Label: "phone image paste", Path: "/repo", Chat: "c1", Branch: "fix/phone-image-size"})
	s1 := &b.st.Sessions[slices.IndexFunc(b.st.Sessions, func(s model.Session) bool { return s.ID == "s1" })]
	s1.Order = append(s1.Order, "wc")
	b.st.Chats = []model.Chat{{ID: "c1", WorkspaceID: "wc", Provider: model.ProviderClaude, State: session.StateReady, Title: "phone image paste"}}
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: aide}}
	u.cfg.Chat.Layout = config.ChatColumn
	st := b.State()
	u.nav.sync(&st)
	u.nav.selectWorkspace(&st, "wc", "")
	cmp := u.composer(&st, "c1")
	cmp.SetText("keep this draft")

	var r input.Router
	var ops op.Ops
	frame := func() {
		ops.Reset()
		u.layout(gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(image.Pt(1200, 900)), Now: time.Now()})
		r.Frame(&ops)
	}
	frame()
	tr := u.transcripts["c1"]
	if tr == nil || tr.ChatLayout != panel.LayoutColumn {
		t.Fatalf("initial layout = %v", tr)
	}
	u.cfg.Chat.Layout = config.ChatLedger
	frame()
	if tr.ChatLayout != panel.LayoutLedger {
		t.Fatalf("switched layout = %q, want ledger", tr.ChatLayout)
	}
	if cmp.Text() != "keep this draft" {
		t.Fatalf("draft lost on switch: %q", cmp.Text())
	}
}

// TestLedgerShots renders headless screenshots of both Column and Ledger
// layouts across Claude, Codex and Antigravity (pane chats and New task
// chats), every key state (finished, running, approval, question, queued
// message, long reply with code and table), narrow (900 dp) and wide
// (3440 dp), plus the Settings Appearance page. Writes PNGs to
// $PITWALL_LEDGER_SHOTS; skips when unset.
func TestLedgerShots(t *testing.T) {
	dir := os.Getenv("PITWALL_LEDGER_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_LEDGER_SHOTS is not set")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := findAide
	findAide = func() []string { return nil }
	t.Cleanup(func() { findAide = old })

	shotNow := time.Date(2026, 11, 21, 14, 4, 12, 0, time.UTC)
	render := func(name string, size image.Point, layoutName string, prov model.Provider, pane bool, snap session.Snapshot) {
		t.Helper()
		if pane {
			snap.PaneID = "h"
		}
		b := ledgerShotBackend{NewFakeBackend(), snap}
		b.st.Workspaces = append(b.st.Workspaces, model.Workspace{ID: "wc", SessionID: "s1", Name: "chat", Label: "phone image paste", Path: "/repo", Chat: "c1", Branch: "fix/phone-image-size"})
		s1 := &b.st.Sessions[slices.IndexFunc(b.st.Sessions, func(s model.Session) bool { return s.ID == "s1" })]
		s1.Order = append(s1.Order, "wc")
		b.st.Chats = []model.Chat{{ID: "c1", WorkspaceID: "wc", Provider: prov, State: snap.SessionState, Title: "phone image paste"}}
		ws := "wc"
		if pane {
			i := slices.IndexFunc(b.st.Panes, func(p model.Pane) bool { return p.ID == "h" })
			b.st.Panes[i].Provider, b.st.Panes[i].Transcript = prov, "/tmp/s.jsonl"
			ws = "w6"
		}
		u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: aide}}
		u.cfg.Font.UISize = 13
		u.cfg.Chat.Claude, u.cfg.Chat.Codex, u.cfg.Chat.Antigravity = true, true, true
		u.cfg.Chat.Width = config.ChatStandard
		u.cfg.Chat.Layout = layoutName
		st := b.State()
		u.nav.sync(&st)
		u.nav.selectWorkspace(&st, ws, "")
		if pane {
			u.nav.setFocus("h")
		}
		win, err := headless.NewWindow(size.X, size.Y)
		if err != nil {
			t.Skip("no headless GPU:", err)
		}
		defer win.Release()
		var r input.Router
		var ops op.Ops
		for range 4 {
			ops.Reset()
			u.layout(gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(size), Now: shotNow})
			r.Frame(&ops)
		}
		if err := win.Frame(&ops); err != nil {
			t.Fatal(err)
		}
		out := image.NewRGBA(image.Rectangle{Max: size})
		if err := win.Screenshot(out); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(filepath.Join(dir, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, out); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}

	at := time.Date(2026, 11, 21, 14, 4, 0, 0, time.UTC).Format(time.RFC3339Nano)
	why := "Bash is not in this session's allow list. Accept edits covers file edits only."
	approval := session.Approval{
		ID:          "a1",
		RequestType: session.RequestCommand,
		Detail:      &why,
		Request:     &session.ToolRequest{ToolName: "Bash", Command: "git push -u origin fix/phone-image-size"},
	}
	question := session.InputRequest{
		ID: "q1",
		Questions: []session.Question{{
			ID:     "target",
			Header: "Resize",
			Prompt: "Which image formats should be re-encoded as JPEG?",
			Options: []session.PromptOption{
				{ID: "o1", Label: "PNG and JPEG (Recommended)", Description: "Keep GIFs animated and resize large screenshots and photos."},
				{ID: "o2", Label: "Every image over 5 MB", Description: "Convert any oversized image before upload."},
			},
		}},
	}
	queued := []session.QueuedInput{{ID: "qu1", Text: "Also add a changelog entry once tests pass", CreatedAt: at}}

	claudeFinished := ledgerBaseSnap(model.ProviderClaude, session.StateReady, false)

	claudeApproval := ledgerBaseSnap(model.ProviderClaude, session.StateWaiting, true)
	claudeApproval.PendingApprovals = []session.Approval{approval}
	claudeApproval.QueuedInputs = queued

	codexRunning := ledgerBaseSnap(model.ProviderCodex, session.StateRunning, true)
	codexRunning.Content = codexRunning.Content[:len(codexRunning.Content)-1] // still running after Bash

	codexQuestion := ledgerBaseSnap(model.ProviderCodex, session.StateWaiting, true)
	codexQuestion.PendingUserInputs = []session.InputRequest{question}

	agyFinished := ledgerBaseSnap(model.ProviderAntigravity, session.StateReady, false)

	agyApproval := ledgerBaseSnap(model.ProviderAntigravity, session.StateWaiting, true)
	agyApproval.PendingApprovals = []session.Approval{approval}
	agyApproval.QueuedInputs = queued

	cases := []struct {
		name string
		size image.Point
		prov model.Provider
		pane bool
		snap session.Snapshot
	}{
		{"claude-tab-finished", image.Pt(1360, 1050), model.ProviderClaude, false, claudeFinished},
		{"claude-pane-approval", image.Pt(1360, 1050), model.ProviderClaude, true, claudeApproval},
		{"codex-pane-running", image.Pt(1360, 1050), model.ProviderCodex, true, codexRunning},
		{"codex-tab-question", image.Pt(1360, 1050), model.ProviderCodex, false, codexQuestion},
		{"agy-tab-finished", image.Pt(1360, 1050), model.ProviderAntigravity, false, agyFinished},
		{"agy-pane-approval", image.Pt(1360, 1050), model.ProviderAntigravity, true, agyApproval},
		{"narrow-900", image.Pt(900, 1000), model.ProviderClaude, false, claudeApproval},
		{"wide-3440", image.Pt(3440, 1400), model.ProviderClaude, false, claudeApproval},
	}

	for _, tc := range cases {
		for _, lay := range []string{config.ChatLedger, config.ChatColumn} {
			render(tc.name+"-"+lay, tc.size, lay, tc.prov, tc.pane, tc.snap)
		}
	}

	// Also render Settings > Appearance showing the Chat layout segmented control.
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	_ = os.WriteFile(cfgPath, []byte("[chat]\nlayout = \"ledger\"\n"), 0o600)
	cs, _ := config.LoadFile(cfgPath)
	var sp settings.Page
	sp.Show(cfgPath)
	ssize := image.Pt(1100, 1300)
	if win, err := headless.NewWindow(ssize.X, ssize.Y); err == nil {
		defer win.Release()
		var r input.Router
		var ops op.Ops
		for range 3 {
			ops.Reset()
			sp.Layout(gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(ssize), Now: time.Now()}, theme.Dark(), cs, nil)
			r.Frame(&ops)
		}
		if err := win.Frame(&ops); err == nil {
			out := image.NewRGBA(image.Rectangle{Max: ssize})
			if err := win.Screenshot(out); err == nil {
				if f, err := os.Create(filepath.Join(dir, "settings-chat-layout.png")); err == nil {
					_ = png.Encode(f, out)
					f.Close()
				}
			}
		}
	}
}
