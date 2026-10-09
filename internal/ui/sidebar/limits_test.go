package sidebar

import (
	"testing"
	"time"
)

// TestLimitText: window names by length, spans in their two largest
// units, and clock times with the day only when it is not today.
func TestLimitText(t *testing.T) {
	for m, want := range map[int64]string{300: "5-hour", 10080: "weekly", 1440: "daily", 2880: "2-day", 90: "90-minute"} {
		if got := WindowName(m); got != want {
			t.Errorf("WindowName(%d) = %q, want %q", m, got, want)
		}
	}
	for d, want := range map[time.Duration]string{30 * time.Second: "<1m", 45 * time.Minute: "45m", 134 * time.Minute: "2h 14m", 76 * time.Hour: "3d 4h"} {
		if got := Span(d); got != want {
			t.Errorf("Span(%v) = %q, want %q", d, got, want)
		}
	}
	now := time.Date(2026, 10, 9, 23, 0, 0, 0, time.Local) // a Friday
	for at, want := range map[time.Time]string{
		time.Date(2026, 10, 9, 16, 40, 0, 0, time.Local):  "16:40",
		time.Date(2026, 10, 10, 0, 30, 0, 0, time.Local):  "Sat 00:30",
		time.Date(2026, 10, 14, 9, 5, 0, 0, time.Local):   "Wed 09:05",
		time.Date(2026, 10, 21, 16, 40, 0, 0, time.Local): "Oct 21 16:40",
	} {
		if got := Clock(at, now); got != want {
			t.Errorf("Clock(%v) = %q, want %q", at, got, want)
		}
	}
}
