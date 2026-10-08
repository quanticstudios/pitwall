package settings

import (
	"fmt"
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"

	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func TestUsageFormats(t *testing.T) {
	for _, c := range []struct {
		n    int64
		want string
	}{{0, "0"}, {950, "950"}, {35_800, "35.8K"}, {64_700_000, "64.7M"}, {220_000_000, "220M"}, {7_990_000_000, "7.99B"},
		{30_800_000_000, "30.8B"}, {999_600, "1M"}, {8_000_000_000, "8B"}, {1_500, "1.5K"}} {
		if got := compact(c.n); got != c.want {
			t.Errorf("compact(%d) = %q, want %q", c.n, got, c.want)
		}
	}
	for c, want := range map[float64]string{16624.19: "$16,624.19", 0: "$0.00", 0.004: "$0.00", 1234567.891: "$1,234,567.89", 999.999: "$1,000.00"} {
		if got := money(c); got != want {
			t.Errorf("money(%v) = %q, want %q", c, got, want)
		}
	}
	if got := percent(0.30123); got != "30.1%" {
		t.Errorf("percent = %q", got)
	}
	if got := plural(1664, "session"); got != "1,664 sessions" {
		t.Errorf("plural = %q", got)
	}
	if top, step := niceScale(730, 4); top != 800 || step != 200 {
		t.Errorf("niceScale(730) = %v, %v", top, step)
	}
	if got := axisMoney(1200, 200); got != "$1,200" {
		t.Errorf("axisMoney = %q", got)
	}
}

// TestUsagePage: opening the category scans in the background, the page
// draws while it runs and once the report lands, and both breakdowns lay
// out.
func TestUsagePage(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	var lines string
	for d := range 10 {
		ts := now.Add(-time.Duration(d) * 24 * time.Hour).UTC().Format(time.RFC3339)
		lines += fmt.Sprintf(`{"type":"assistant","timestamp":%q,"sessionId":"s%d","requestId":"r%d","message":{"id":"m%d","model":"claude-opus-5-5","usage":{"input_tokens":1000000,"output_tokens":0}}}`+"\n", ts, d%3, d, d)
	}
	os.MkdirAll(filepath.Join(dir, "p"), 0o755)
	os.WriteFile(filepath.Join(dir, "p", "s.jsonl"), []byte(lines), 0o644)
	usageSources = func() []flow.Source { return []flow.Source{{Provider: model.ProviderClaude, Dir: dir}} }
	defer func() { usageSources = func() []flow.Source { return nil } }()

	var p Page
	p.Show(filepath.Join(t.TempDir(), "config.toml"))
	p.cat = catUsage
	var ops op.Ops
	draw := func() {
		ops.Reset()
		gtx := gl.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(image.Pt(1000, 700)), Now: time.Now()}
		p.Layout(gtx, theme.Dark(), p.s, nil)
	}
	draw()
	var r *flow.Report
	for range 200 {
		if r = p.us.shown(); r != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if r == nil || r.Sessions != 3 || r.Cost != 40 || len(r.Days) != 30 {
		t.Fatalf("report = %+v", r)
	}
	draw()
	p.us.byDay = true
	draw()
	// A narrow window stacks the chart under the summary.
	ops.Reset()
	p.Layout(gl.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(image.Pt(600, 700)), Now: time.Now()}, theme.Dark(), p.s, nil)
	// Reopening scans again.
	before := p.us.of
	p.Show(filepath.Join(t.TempDir(), "config.toml"))
	if p.us.shown() == nil {
		t.Error("the last report should stay up while the next scan runs")
	}
	draw()
	p.us.mu.Lock()
	scanning := p.us.scanning || p.us.of != before
	p.us.mu.Unlock()
	if !scanning {
		t.Error("reopening did not scan again")
	}
}
