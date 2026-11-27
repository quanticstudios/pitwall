package panel

import (
	"encoding/json"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// agyChat is an Antigravity turn like the one in the redesign's
// screenshot: work rows, a report between calls, more work, a reply.
func agyChat() *session.Snapshot {
	turn := "t1"
	when := time.Date(2026, 11, 27, 16, 52, 0, 0, time.UTC).Format(time.RFC3339Nano)
	call := func(id, title, cmd string) session.Item {
		in, _ := json.Marshal(map[string]string{"command": cmd, "description": title})
		return session.Item{ID: id, Kind: session.KindToolCall, TurnID: &turn, CreatedAt: when, UpdatedAt: when, Tool: &session.ToolCall{ToolName: "Bash", Status: "completed", Command: cmd, Input: in}}
	}
	report := "**H100 `v4-SFT` training and quantization are done**, the last 8 to 9 minutes of Stage A:\n\n" +
		"- **SFT (`3,478` steps):** completed cleanly. Best checkpoint at step 2000: `gen_parse = 100.0%`, `ppl = 1.11`.\n" +
		"- **Quantization:** `sinter_polyglot_1_5b_v4_sft_q4_k_m.gguf` (941 MB) built in `10.8s`.\n" +
		"- **Budget:** `$13.01` left of `$20.04` (Stage B DPO needs about `$1.50`).\n\n" +
		"As soon as the download and sampling complete, I'll terminate the pod, grade all `12,000` on-policy draws locally, and kick off Stage B DPO."
	return &session.Snapshot{SessionID: "s1", Provider: model.ProviderAntigravity, SessionState: session.StateReady, Content: []session.Item{
		{ID: "p1", Kind: session.KindUserPrompt, Text: "Check Stage A, then start Stage B when sampling is done.", TurnID: &turn, CreatedAt: when, UpdatedAt: when},
		call("c1", "Check H100 Stage A progress, outputs, and Runpod balance", "ssh -o StrictHostKeyChecking=no runpod"),
		{ID: "n1", Kind: session.KindAssistantText, Text: "Let me check the pod.", TurnID: &turn, CreatedAt: when},
		call("c2", "Download v4-SFT Q4_K_M GGUF and best_hf while sampling finishes", "mkdir -p /home/u/sinter"),
		{ID: "a1", Kind: session.KindAssistantText, Text: report, TurnID: &turn, CreatedAt: when},
		call("c3", "Check if H100 on-policy sampling finished", "ssh -o StrictHostKeyChecking=no runpod"),
		{ID: "a2", Kind: session.KindAssistantText, Text: "Sampling finished and the pod is gone. Stage B DPO is running on the local box; the first eval lands in about 40 minutes, and I'll report the verdict suite's score against the SFT baseline then.", TurnID: &turn, CreatedAt: when},
	}}
}

// TestChatTypeShots writes the agy turn in both layouts, narrow and wide,
// to $PITWALL_SHOT_DIR; without it the test does nothing.
func TestChatTypeShots(t *testing.T) {
	dir := os.Getenv("PITWALL_SHOT_DIR")
	if dir == "" {
		t.Skip("PITWALL_SHOT_DIR not set")
	}
	for _, lay := range []string{LayoutLedger, LayoutColumn} {
		for _, w := range []int{600, 1440} {
			img, _ := ledgerFrame(t, theme.Dark(), agyChat(), lay, w, 900)
			f, err := os.Create(filepath.Join(dir, lay+"-"+strconv.Itoa(w)+".png"))
			if err != nil {
				t.Fatal(err)
			}
			if err := png.Encode(f, img); err != nil {
				t.Fatal(err)
			}
			f.Close()
		}
	}
}

// TestReplyMeasure draws a long reply in both layouts in a wide pane and
// checks its text stops at a 65 to 75 character measure, not the pane's
// or the column's width.
func TestReplyMeasure(t *testing.T) {
	th := theme.Dark()
	turn := "t1"
	long := strings.Repeat("The refresh uses the server clock, so the test fails by one second. ", 12)
	s := &session.Snapshot{SessionID: "s1", Provider: model.ProviderClaude, SessionState: session.StateReady, Content: []session.Item{
		{ID: "p1", Kind: session.KindUserPrompt, Text: "Why?", TurnID: &turn},
		{ID: "a1", Kind: session.KindAssistantText, Text: long, TurnID: &turn},
	}}
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
	d := &cdraw{th: th}
	avg := d.avgChar(gtx)
	const w, h = 1440, 700
	for _, lay := range []string{LayoutLedger, LayoutColumn} {
		img, ch := ledgerFrame(t, th, s, lay, w, h)
		var x0, col int
		if lay == LayoutLedger {
			g := ledgerMetrics(gtx, s.Provider, w)
			x0, col = g.textX, g.textW
		} else {
			col = 760
			x0 = (w - col) / 2
		}
		lo, hi := inkSpan(img, x0, w, 120, h-ch-gtx.Dp(40), th.Bg)
		if lo < 0 {
			t.Fatalf("%s: no reply drawn", lay)
		}
		chars := (hi - x0) / avg
		if chars < 65 || chars > 75 || hi-x0 >= col-gtx.Dp(40) {
			t.Errorf("%s: reply runs %d px, about %d characters, in a %d px column; want 65 to 75", lay, hi-x0, chars, col)
		}
	}
}
