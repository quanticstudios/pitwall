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

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/antigravity"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestAgyApprovalAndQuestionShots renders headless PNGs of the desktop app
// showing an Antigravity pane waiting on a command permission prompt and on
// an ask_question prompt (both sidebar "needs you" state + buttons and the
// desktop chat card) into $PITWALL_AGY_APPROVAL_SHOTS; skips when unset.
func TestAgyApprovalAndQuestionShots(t *testing.T) {
	dir := os.Getenv("PITWALL_AGY_APPROVAL_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_AGY_APPROVAL_SHOTS is not set")
	}
	old := findAide
	findAide = func() []string { return nil }
	t.Cleanup(func() { findAide = old })

	now := time.Date(2026, 11, 22, 19, 30, 0, 0, time.UTC)
	at := func(s int) string { return now.Add(time.Duration(s) * time.Second).Format(time.RFC3339Nano) }
	turn := "step-0"
	prov := model.ProviderAntigravity

	buildSnap := func(prompt string, trEvents []session.Event) session.Snapshot {
		snap := session.Snapshot{
			SessionID:      "h",
			PaneID:         "h",
			WorkspaceID:    "w6",
			Provider:       model.ProviderAntigravity,
			SessionState:   session.StateRunning,
			Running:        true,
			PermissionMode: "default",
			Content: []session.Item{
				{ID: "step-0", Kind: session.KindUserPrompt, Text: prompt, TurnID: &turn, Provider: &prov, CreatedAt: at(0)},
			},
			Turns: []session.TurnSummary{{ID: turn, State: session.TurnRunning, StartedAt: at(0)}},
		}
		for _, e := range trEvents {
			if e.CreatedAt == "" {
				e.CreatedAt = at(4)
			}
			for _, dl := range session.Reduce(snap, e) {
				dl.Version, dl.SessionID, dl.Seq = session.DeltaVersion, snap.SessionID, snap.Revision+1
				dl.Revision, dl.UpdatedAt = snap.Revision+1, e.CreatedAt
				next, err := session.Apply(&snap, snap.Revision, dl)
				if err != nil {
					t.Fatal(err)
				}
				snap = next
			}
		}
		return snap
	}

	trApproval := antigravity.NewTranscript()
	approvalSnap := buildSnap("Run exactly this shell command: echo hello-repro", trApproval.Ask("1", "Approve Bash: echo hello-repro"))

	trQuestion := antigravity.NewTranscript()
	questionSnap := buildSnap(
		"Use your ask_question tool to ask me which framework I prefer, with options Go and Rust.",
		trQuestion.AskInput("2", "Which framework do you prefer?", []session.Question{{
			ID:      "q1",
			Prompt:  "Which framework do you prefer?",
			Options: []session.PromptOption{{Label: "Go"}, {Label: "Rust"}},
		}}),
	)

	cases := []struct {
		name string
		act  model.Activity
		snap session.Snapshot
	}{
		{
			name: "agy-approval-card",
			act: model.Activity{
				PaneID:      "h",
				WorkspaceID: "w6",
				Provider:    model.ProviderAntigravity,
				State:       model.StatePendingApproval,
				Detail:      "Approve Bash: echo hello-repro",
				Buttons:     []string{agent.AllowAlways},
				UpdatedAt:   now.Add(4 * time.Second),
				Unseen:      true,
			},
			snap: approvalSnap,
		},
		{
			name: "agy-question-card",
			act: model.Activity{
				PaneID:      "h",
				WorkspaceID: "w6",
				Provider:    model.ProviderAntigravity,
				State:       model.StateAwaitingInput,
				Detail:      "Which framework do you prefer?",
				Buttons:     []string{"Go", "Rust"},
				Menu:        true,
				UpdatedAt:   now.Add(4 * time.Second),
				Unseen:      true,
			},
			snap: questionSnap,
		},
	}

	size := image.Pt(1180, 760)
	for _, tc := range cases {
		b := agyQueueBackend{NewFakeBackend(), tc.snap}
		pi := slices.IndexFunc(b.st.Panes, func(p model.Pane) bool { return p.ID == "h" })
		b.st.Panes[pi].Provider, b.st.Panes[pi].Transcript = model.ProviderAntigravity, "/tmp/transcript_full.jsonl"
		if ai := slices.IndexFunc(b.st.Activities, func(a model.Activity) bool { return a.PaneID == "h" }); ai >= 0 {
			b.st.Activities[ai] = tc.act
		} else {
			b.st.Activities = append(b.st.Activities, tc.act)
		}
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
			time.Sleep(80 * time.Millisecond)
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
			f.Close()
			t.Fatal(err)
		}
		f.Close()
	}
}
