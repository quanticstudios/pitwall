package chatin

import (
	"context"
	"image"
	"testing"
	"time"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func TestLedgerStatus(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    *session.Snapshot
		note string
		ok   bool
		msg  string
		tone string
	}{
		{"approval", &session.Snapshot{PendingApprovals: []session.Approval{{ID: "a1"}}}, "", false, "Waiting for approval", "yellow"},
		{"question", &session.Snapshot{PendingUserInputs: []session.InputRequest{{ID: "q1"}}}, "", false, "Waiting for answer", "yellow"},
		{"plan", &session.Snapshot{PlanApproval: &session.PlanApproval{ID: "p1"}}, "", false, "Waiting for plan review", "yellow"},
		{"limit", &session.Snapshot{UsageLimit: &session.UsageLimit{Label: "5-hour"}}, "", false, "Usage limit reached", "yellow"},
		{"working", &session.Snapshot{SessionState: session.StateRunning, Running: true}, "", false, "Working…", "muted"},
		{"idle", &session.Snapshot{SessionState: session.StateReady}, "", false, "", ""},
		{"error-note", &session.Snapshot{SessionState: session.StateReady}, "Failed to attach", false, "Failed to attach", "red"},
		{"ok-note", &session.Snapshot{SessionState: session.StateReady}, "Kept files", true, "Kept files", "muted"},
	} {
		_, msg, tone := ledgerStatus(tc.s, tc.note, tc.ok)
		if msg != tc.msg || tone != tc.tone {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", tc.name, msg, tone, tc.msg, tc.tone)
		}
	}
}

func TestLedgerComposerLayoutAndAttach(t *testing.T) {
	th := theme.Dark()
	used, maxTok := int64(71_000), int64(200_000)
	mdl, eff := "claude-opus-5-5", "high"
	snap := &session.Snapshot{
		SessionID:      "c1",
		Provider:       model.ProviderClaude,
		SessionState:   session.StateReady,
		PermissionMode: ModeAccept,
		Model:          &mdl,
		Effort:         &eff,
		Usage:          &session.Usage{ContextTokens: &used, MaxTokens: &maxTok},
	}
	var c Composer
	readCh := make(chan struct{}, 1)
	env := Env{
		Chat:      "c1",
		Snap:      snap,
		Width:     894,
		Focus:     true,
		Ledger:    true,
		Images:    true,
		Commands:  true,
		Branch:    "chat-ledger",
		Mentions:  func(string) []string { return []string{"main.go"} },
		ReadImage: func(context.Context) ([]byte, error) { readCh <- struct{}{}; return pngOf(4, 4), nil },
	}
	var ops op.Ops
	draw := func() layout.Dimensions {
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(1100, 300)), Now: time.Now()}
		return c.Layout(gtx, th, env)
	}
	d := draw()
	if d.Size.Y <= 0 {
		t.Fatalf("Ledger composer drew zero height: %v", d.Size)
	}

	// Clicking the round + attach button opens its popover; clicking Mention inserts @.
	c.attachBtn.Click()
	draw()
	if !c.menu || c.which != &c.attachBtn {
		t.Fatal("attach button did not open attach menu")
	}
	c.click("attach:file").Click()
	draw()
	if c.Text() != "@" {
		t.Fatalf("attach file inserted %q, want @", c.Text())
	}

	// Clicking Send with text emits proto.ChatSend just like Column.
	c.SetText("hello ledger")
	c.sendBtn.Click()
	draw()
	msgs := c.Messages()
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	if send, ok := msgs[0].(proto.ChatSend); !ok || send.Text != "hello ledger" {
		t.Fatalf("unexpected message %#v", msgs[0])
	}

	// Clicking Paste image in attach menu triggers ReadImage.
	c.attachBtn.Click()
	draw()
	c.click("attach:image").Click()
	draw()
	select {
	case <-readCh:
	case <-time.After(time.Second):
		t.Fatal("Paste image did not call ReadImage")
	}

	// Unknown post-compaction usage renders the empty track meter without panicking.
	snap.Usage = &session.Usage{MaxTokens: &maxTok}
	if d := draw(); d.Size.Y <= 0 {
		t.Fatalf("Ledger composer with unknown meter drew zero height: %v", d.Size)
	}
	_ = key.NameReturn
}
