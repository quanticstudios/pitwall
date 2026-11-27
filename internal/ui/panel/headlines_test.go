package panel

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/chatin"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// headlineChat is a six-turn Antigravity chat, each turn a prompt, a
// command and a reply, all finished; headlineLines are the headlines of
// its first five turns, from the route C mock.
func headlineChat() *session.Snapshot {
	prompts := []string{"Start the SFT run on the H100", "Quantize the best checkpoint", "Sample the outputs",
		"Check the sampling", "Benchmark v4-SFT", "Write up the results"}
	cmds := []string{"python train.py --steps 3478", "llama-quantize best.gguf Q4_K_M", "python sample.py --n 400",
		"python check.py", "python bench.py --prompts 1691", "cat results.md"}
	replies := []string{"Training started.", "Quantized to 941 MB.", "Sampling 400 of 2,000.",
		"Sampling looks right.", "Benchmark running.", "Best checkpoint at step 2000: gen_parse 100.0%, val_gen_em 67.2%, ppl 1.11. The GGUF downloads while sampling finishes."}
	base := time.Date(2026, 11, 27, 16, 20, 0, 0, time.Local)
	s := &session.Snapshot{SessionID: "c", Provider: model.ProviderAntigravity, SessionState: "ready"}
	for i := range prompts {
		turn := fmt.Sprintf("t%d", i+1)
		at := session.Stamp(base.Add(time.Duration(i) * 11 * time.Minute))
		end := session.Stamp(base.Add(time.Duration(i)*11*time.Minute + 3*time.Minute))
		s.Content = append(s.Content,
			session.Item{ID: "p" + turn, Kind: session.KindUserPrompt, Text: prompts[i], TurnID: &turn, CreatedAt: at, UpdatedAt: at},
			session.Item{ID: "c" + turn, Kind: session.KindToolCall, TurnID: &turn, CreatedAt: at, UpdatedAt: end,
				Tool: &session.ToolCall{ToolName: "run_command", Command: cmds[i], Status: "completed"}},
			session.Item{ID: "a" + turn, Kind: session.KindAssistantText, Text: replies[i], TurnID: &turn, CreatedAt: end, UpdatedAt: end})
		s.Turns = append(s.Turns, session.TurnSummary{ID: turn, State: session.TurnCompleted, StartedAt: at, CompletedAt: &end})
	}
	return s
}

var headlineLines = []model.Headline{
	{Turn: "t1", Text: "Started SFT on the H100; 3,478 steps planned"},
	{Turn: "t2", Text: "Quantized the best checkpoint to Q4_K_M, 941 MB"},
	{Turn: "t3", Text: "Training and quantization done; sampling 400 of 2,000"},
	{Turn: "t5", Text: "Benchmarking v4-SFT on 1,691 prompts"},
	{Turn: "t6", Text: "Wrote up the results"},
}

// headlineSegs is what each row of tr shows: "hl:<turn>" for a headline,
// "p:<turn>" for a prompt, "x" for any other row.
func headlineSegs(tr *Transcript, s *session.Snapshot) []string {
	var out []string
	for i, r := range tr.rows {
		switch {
		case r.seg == segHeadline:
			out = append(out, "hl:"+r.b.turn)
		case !r.isTail() && r.res == nil && r.b.kind == blockPrompt:
			out = append(out, "p:"+rowTurn(s, tr.rows, i))
		case len(out) == 0 || out[len(out)-1] != "x":
			out = append(out, "x")
		}
	}
	return out
}

// TestHeadlineRows: in the Headlines view each finished turn with a line
// folds to it; the latest turn, one without a line and one that asks the
// user stay open; a click opens one in place; both layouts; the other
// modes ignore headlines.
func TestHeadlineRows(t *testing.T) {
	for _, lay := range []string{LayoutColumn, LayoutLedger} {
		s := headlineChat()
		tr := &Transcript{ToolCalls: ToolsHeadlines, ChatLayout: lay, Headlines: headlineLines}
		var ops op.Ops
		now := time.Date(2026, 11, 27, 17, 30, 0, 0, time.UTC)
		transcriptFrame(tr, theme.Dark(), s, &ops, now)
		got := headlineSegs(tr, s)
		want := []string{"hl:t1", "hl:t2", "hl:t3", "p:t4", "x", "hl:t5", "p:t6", "x"}
		if !slices.Equal(got, want) {
			t.Fatalf("%s: rows %v, want %v", lay, got, want)
		}
		tr.open[headlineID("t2")] = true // a click
		transcriptFrame(tr, theme.Dark(), s, &ops, now)
		if got := headlineSegs(tr, s); !slices.Equal(got[:4], []string{"hl:t1", "hl:t2", "p:t2", "x"}) {
			t.Errorf("%s: opened t2: %v", lay, got)
		}
		tr.ToolCalls = ToolsAll
		transcriptFrame(tr, theme.Dark(), s, &ops, now)
		if got := headlineSegs(tr, s); slices.Contains(got, "hl:t1") {
			t.Errorf("%s: all mode folds turns: %v", lay, got)
		}
	}
	s := headlineChat()
	ask := s.Content[3*2+1].Tool // t3's call
	ask.ToolName, ask.Status = "AskUserQuestion", "running"
	tr := &Transcript{ToolCalls: ToolsHeadlines, Headlines: headlineLines}
	var ops op.Ops
	transcriptFrame(tr, theme.Dark(), s, &ops, time.Now())
	if got := headlineSegs(tr, s); slices.Contains(got, "hl:t3") {
		t.Errorf("a turn that asks the user folded: %v", got)
	}
}

// TestHeadlineShots draws the six-turn chat in the Headlines view, Column
// and Ledger, at 1440 wide, with the third turn opened, and saves the
// pictures in $PITWALL_HEADLINE_SHOTS when set.
func TestHeadlineShots(t *testing.T) {
	dir := os.Getenv("PITWALL_HEADLINE_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_HEADLINE_SHOTS is not set")
	}
	for _, lay := range []string{LayoutColumn, LayoutLedger} {
		for _, mode := range []string{ToolsAll, ToolsHeadlines} {
			s := headlineChat()
			th := theme.Dark()
			w, h := 1440, 900
			win, err := headless.NewWindow(w, h)
			if err != nil {
				t.Skip(err)
			}
			tr := Transcript{Dir: "/repo", ChatLayout: lay, ToolCalls: mode, Width: 760, Headlines: headlineLines}
			var cmp chatin.Composer
			var ops op.Ops
			now := time.Date(2026, 11, 27, 17, 30, 0, 0, time.UTC)
			for i := range 4 {
				if i == 2 {
					tr.open[headlineID("t3")] = true
				}
				ops.Reset()
				now = now.Add(16 * time.Millisecond)
				gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(w, h)), Now: now}
				env := chatin.Env{Chat: "c", Snap: s, Width: 760, Gutter: LedgerGutter(s.Provider), Ledger: lay == LayoutLedger, Key: tr.Key, Answer: tr.Answer}
				layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return tr.Layout(gtx, th, s, false) }),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return cmp.Layout(gtx, th, env) }),
				)
				if err := win.Frame(&ops); err != nil {
					t.Fatal(err)
				}
			}
			img := image.NewRGBA(image.Rect(0, 0, w, h))
			if err := win.Screenshot(img); err != nil {
				t.Fatal(err)
			}
			win.Release()
			f, err := os.Create(filepath.Join(dir, "headlines-"+lay+"-"+mode+".png"))
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
