package app

import (
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/model"
)

// TestLimitNotice: a window reaching 90% shows one notice, later reads of
// the same window cycle, its reset time drifting a second, show no other,
// and the notice goes when the window resets; the next cycle notifies
// again.
func TestLimitNotice(t *testing.T) {
	now := time.Date(2026, 10, 9, 14, 0, 0, 0, time.Local)
	resets := time.Date(2026, 10, 9, 16, 40, 0, 0, time.Local)
	read := func(used float64, resets time.Time) []flow.Limit {
		return []flow.Limit{{Provider: model.ProviderClaude, Seen: now, Windows: []flow.Window{
			{Minutes: 300, Used: used, Resets: resets},
			{Minutes: 10080, Used: 30, Resets: resets.Add(72 * time.Hour)},
		}}}
	}
	var lw limitWatch
	lw.set(read(85, resets), now)
	if lw.shown() != "" {
		t.Fatalf("notice at 85%%: %q", lw.shown())
	}
	lw.set(read(91.2, resets), now)
	if want := "Claude 5-hour window at 91%, resets 16:40"; lw.shown() != want {
		t.Fatalf("got %q, want %q", lw.shown(), want)
	}
	lw.notice = "" // dismissed
	lw.set(read(96, resets.Add(time.Second)), now.Add(time.Minute))
	if lw.shown() != "" {
		t.Fatalf("the same window notified again: %q", lw.shown())
	}
	lw.notice = "x"
	lw.key = limitKey(read(0, resets)[0], read(0, resets)[0].Windows[0])
	lw.set(read(96, resets), resets.Add(time.Minute))
	if lw.shown() != "" {
		t.Fatalf("the notice outlived the reset: %q", lw.shown())
	}
	next := resets.Add(5 * time.Hour)
	lw.set(read(90, next), resets.Add(4*time.Hour))
	if lw.shown() == "" {
		t.Fatal("the next cycle did not notify")
	}
}
