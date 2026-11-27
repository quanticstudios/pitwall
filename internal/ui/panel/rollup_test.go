package panel

import (
	"strings"
	"testing"
	"time"

	"gioui.org/op"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestRollupCounts: the 20-step turn is 15 tools, 2 files, 1 test failed,
// 5 thoughts; two failures of two kinds are "2 calls failed".
func TestRollupCounts(t *testing.T) {
	s := rollupSnap()
	r := rollupOf(s.Content, 0, len(s.Content))
	if r.tools != 15 || r.files != 2 || r.failed != 1 || r.failWhat != "test" || r.thoughts != 5 {
		t.Fatalf("rollup = %+v", r)
	}
	var got []string
	for _, c := range r.chips() {
		got = append(got, c.text)
	}
	if want := "15 tools|2 files|1 test failed"; strings.Join(got, "|") != want {
		t.Fatalf("chips = %q, want %q", strings.Join(got, "|"), want)
	}
	if c := r.chips(); !c[2].bad || c[0].bad {
		t.Fatalf("only the failure is red: %+v", c)
	}
	s.Content = append(s.Content, session.Item{ID: "x", Kind: session.KindToolCall, Tool: &session.ToolCall{ToolName: "Read", Status: "failed"}})
	if r := rollupOf(s.Content, 0, len(s.Content)); r.failed != 2 || r.failWhat != "call" {
		t.Fatalf("mixed failures = %+v", r)
	}
	if (rollup{}).any() || len((rollup{thoughts: 1}).chips()) != 0 {
		t.Fatal("no work is no line; thoughts alone make no chip")
	}
}

// TestRollupDuration: the line times the turn from its summary.
func TestRollupDuration(t *testing.T) {
	s := rollupSnap()
	start, end, _ := turnTimes(s, block{turn: "t1", lo: 0, hi: len(s.Content)}, true)
	if got := duration(end.Sub(start)); got != "2m 14s" {
		t.Fatalf("took %s, want 2m 14s", got)
	}
}

// TestDiffShare: the bar's green part is the share of lines added.
func TestDiffShare(t *testing.T) {
	for _, c := range []struct{ w, add, del, want int }{{80, 18, 4, 65}, {80, 0, 0, 0}, {80, 5, 0, 80}, {80, 0, 3, 0}} {
		if got := addShare(c.w, c.add, c.del); got != c.want {
			t.Errorf("addShare(%d, %d, %d) = %d, want %d", c.w, c.add, c.del, got, c.want)
		}
	}
	s := rollupSnap()
	tr := &Transcript{Dir: "/repo"}
	var ops op.Ops
	transcriptFrame(tr, theme.Dark(), s, &ops, time.Now())
	b := block{kind: blockTurn, turn: "t1", lo: 1, from: 1, hi: len(s.Content)}
	fs := tr.changesOf(&cdraw{t: tr, s: s}, b)
	if add, del := changeTotals(fs); len(fs) != 2 || add != 7 || del != 3 {
		t.Fatalf("changes = %d files +%d −%d, want 2 files +7 −3", len(fs), add, del)
	}
}

// TestRollupModes: each tool call mode's folded and open rows under the
// Worked line, in Column and Ledger.
func TestRollupModes(t *testing.T) {
	failedCall := 12 // "go test ./auth/..."
	for _, l := range []string{LayoutColumn, LayoutLedger} {
		for _, c := range []struct {
			mode       string
			open       bool
			failedOnly bool // the folded rows are the failed call alone
		}{
			{ToolsAll, true, false},
			{ToolsFailed, false, true},
			{ToolsNone, false, false},
			{ToolsZen, false, false},
		} {
			s := rollupSnap()
			tr := &Transcript{Dir: "/repo", ChatLayout: l, ToolCalls: c.mode, Width: 760}
			var ops op.Ops
			transcriptFrame(tr, theme.Dark(), s, &ops, time.Now())
			var line *trow
			var work []int
			for i := range tr.rows {
				r := &tr.rows[i]
				switch r.seg {
				case segTurn:
					line = r
				case segItem, segHead, segProse:
					work = append(work, r.i)
				}
			}
			if line == nil || line.open != c.open {
				t.Fatalf("%s %s: line %+v, want open %v", l, c.mode, line, c.open)
			}
			switch {
			case c.open && len(work) == 0:
				t.Errorf("%s %s: open line shows no work", l, c.mode)
			case c.failedOnly && (len(work) != 1 || work[0] != failedCall):
				t.Errorf("%s %s: folded rows %v, want the failed call %d", l, c.mode, work, failedCall)
			case !c.open && !c.failedOnly && len(work) != 0:
				t.Errorf("%s %s: folded rows %v, want none", l, c.mode, work)
			}
			// A click opens it: every call shows.
			tr.open[turnID(s, line.b)] = true
			transcriptFrame(tr, theme.Dark(), s, &ops, time.Now())
			n := 0
			for _, r := range tr.rows {
				if r.seg == segItem || r.seg == segHead {
					n++
				}
			}
			if n == 0 {
				t.Errorf("%s %s: opened line shows no calls", l, c.mode)
			}
		}
	}
}
