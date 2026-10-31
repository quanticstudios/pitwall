package model

import (
	"testing"
	"time"
)

func TestParseTaskSchedule(t *testing.T) {
	zone := time.FixedZone("local", 2*60*60)
	now := time.Date(2026, 10, 30, 1, 30, 0, 0, zone)
	for _, c := range []struct {
		name, at, every string
		next            time.Time
		repeat          time.Duration
		invalid         bool
	}{
		{name: "immediate"},
		{name: "today", at: "02:00", next: now.Add(30 * time.Minute)},
		{name: "tomorrow", at: "01:00", next: now.Add(23*time.Hour + 30*time.Minute)},
		{name: "exact minute tomorrow", at: "01:30", next: now.Add(24 * time.Hour)},
		{name: "repeat alone", every: "24h", next: now.Add(24 * time.Hour), repeat: 24 * time.Hour},
		{name: "at and every", at: "02:00", every: "24h", next: now.Add(30 * time.Minute), repeat: 24 * time.Hour},
		{name: "bad clock", at: "25:00", invalid: true},
		{name: "bad repeat", every: "daily", invalid: true},
		{name: "zero repeat", every: "0s", invalid: true},
		{name: "negative repeat", every: "-1h", invalid: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			next, repeat, err := ParseTaskSchedule(c.at, c.every, now)
			if (err != nil) != c.invalid || !c.invalid && (!next.Equal(c.next) || repeat != c.repeat) {
				t.Fatalf("got %v, %v, %v; want %v, %v, invalid=%v", next, repeat, err, c.next, c.repeat, c.invalid)
			}
		})
	}
}
