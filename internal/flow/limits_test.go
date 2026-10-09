package flow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

// TestLimitsCodex: a rollout's latest rate_limits per limit id, resets
// given as resets_at or as resets_in_seconds from the event, a token_count
// without info still counted, and a fork's copied history left out.
func TestLimitsCodex(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	stamp := func(s int) string { return t0.Add(time.Duration(s) * time.Second).UTC().Format(time.RFC3339Nano) }
	reset := t0.Add(3 * time.Hour).Unix()
	writeFile(t, filepath.Join(dir, "rollout-a.jsonl"),
		`{"timestamp":"`+stamp(0)+`","type":"session_meta","payload":{"id":"a","source":"cli"}}`+"\n",
		`{"timestamp":"`+stamp(1)+`","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"primary":{"used_percent":10,"window_minutes":300,"resets_in_seconds":600},"secondary":{"used_percent":40,"window_minutes":10080,"resets_in_seconds":86400}}}}`+"\n",
		`{"timestamp":"`+stamp(5)+`","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"limit_id":"codex","limit_name":null,"primary":{"used_percent":12.5,"window_minutes":300,"resets_at":`+itoa(reset)+`},"secondary":{"used_percent":41,"window_minutes":10080,"resets_at":`+itoa(reset+86400)+`},"credits":null,"plan_type":"pro"}}}`+"\n",
		`{"timestamp":"`+stamp(6)+`","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"limit_id":"codex_spark","limit_name":"GPT-Spark","primary":{"used_percent":3,"window_minutes":300,"resets_at":`+itoa(reset)+`},"secondary":null}}}`+"\n",
	)
	// A fork written later carries a's old limits in its copied history.
	writeFile(t, filepath.Join(dir, "rollout-b.jsonl"),
		`{"timestamp":"`+stamp(30)+`","type":"session_meta","payload":{"id":"b","forked_from_id":"a"}}`+"\n",
		`{"timestamp":"`+stamp(30)+`","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"primary":{"used_percent":10,"window_minutes":300,"resets_in_seconds":600}}}}`+"\n",
	)
	var s Scanner
	s.Scan([]Source{{model.ProviderCodex, dir}}, time.Now().Add(-24*time.Hour))
	got := s.Limits(t.TempDir())
	want := []Limit{
		{Provider: model.ProviderCodex, Seen: t0.Add(5 * time.Second), Windows: []Window{
			{Minutes: 300, Used: 12.5, Resets: time.Unix(reset, 0)},
			{Minutes: 10080, Used: 41, Resets: time.Unix(reset+86400, 0)},
		}},
		{Provider: model.ProviderCodex, Name: "GPT-Spark", Seen: t0.Add(6 * time.Second), Windows: []Window{
			{Minutes: 300, Used: 3, Resets: time.Unix(reset, 0)},
		}},
	}
	if !equalLimits(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}

	// The older field: a reset in seconds from the event's time.
	writeFile(t, filepath.Join(dir, "rollout-a.jsonl"),
		`{"timestamp":"`+stamp(1)+`","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"primary":{"used_percent":10,"window_minutes":300,"resets_in_seconds":600}}}}`+"\n")
	s.Scan([]Source{{model.ProviderCodex, dir}}, time.Now().Add(-24*time.Hour))
	got = s.Limits(t.TempDir())
	if len(got) != 1 || !got[0].Windows[0].Resets.Equal(t0.Add(601*time.Second)) {
		t.Fatalf("resets_in_seconds: %+v", got)
	}
}

// TestLimitsClaude: the statusline's rate_limits as pitwall statusline
// saves them, and none from a statusline without them.
func TestLimitsClaude(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	in := `{"session_id":"s","model":{"display_name":"Opus"},"rate_limits":{"five_hour":{"used_percentage":23.5,"resets_at":1800003600},"seven_day":{"used_percentage":41.2,"resets_at":1800300000},"spend_limit":{"used_percentage":5}}}`
	l, ok := Statusline([]byte(in), now)
	want := Limit{Provider: model.ProviderClaude, Seen: now, Windows: []Window{
		{Minutes: 300, Used: 23.5, Resets: time.Unix(1800003600, 0)},
		{Minutes: 10080, Used: 41.2, Resets: time.Unix(1800300000, 0)},
	}}
	if !ok || !equalLimits([]Limit{l}, []Limit{want}) {
		t.Fatalf("got %+v %v", l, ok)
	}
	if _, ok := Statusline([]byte(`{"session_id":"s","model":{"display_name":"Opus"}}`), now); ok {
		t.Error("a statusline without rate_limits has limits")
	}
	state := t.TempDir()
	data, _ := json.Marshal(l)
	if err := os.WriteFile(filepath.Join(state, ClaudeLimitsFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
	var s Scanner
	if got := s.Limits(state); !equalLimits(got, []Limit{want}) {
		t.Fatalf("from the file: %+v", got)
	}
}

// TestWindowHits: the pace so far from the window's start projects the
// time it fills, unless that is after its reset or the window is young.
func TestWindowHits(t *testing.T) {
	resets := time.Date(2026, 10, 9, 18, 0, 0, 0, time.UTC) // a 5-hour window from 13:00
	at := func(h, m int) time.Time { return time.Date(2026, 10, 9, h, m, 0, 0, time.UTC) }
	for _, tc := range []struct {
		used float64
		seen time.Time
		want time.Time
	}{
		{50, at(15, 0), at(17, 0)},                        // 50% in 2h: 100% 2h later
		{40, at(15, 0), time.Time{}},                      // 100% at 18:00, the reset
		{80, at(14, 0), at(14, 15)},                       // 80% in 1h: 20% more in 15m
		{5, at(13, 10), time.Time{}},                      // 10 minutes in: too soon to tell
		{0, at(15, 0), time.Time{}},                       // no use
		{100, at(15, 0), time.Time{}},                     // already full
		{25, at(14, 0), at(17, 0)},                        // 25% an hour: full at 17:00
		{90, at(17, 0), at(17, 26).Add(40 * time.Second)}, // 4h for 90%: 10% more in 26m40s
	} {
		w := Window{Minutes: 300, Used: tc.used, Resets: resets}
		if got := w.Hits(tc.seen); !got.Equal(tc.want) {
			t.Errorf("%v%% at %s: got %s, want %s", tc.used, tc.seen.Format("15:04"), got.Format(time.TimeOnly), tc.want.Format(time.TimeOnly))
		}
	}
	if !(Window{Minutes: 300, Used: 50}).Hits(at(15, 0)).IsZero() {
		t.Error("a window with no reset time projects")
	}
}

// TestWindowNow: a window whose reset passed since it was seen is unused,
// whatever it last showed; one before its reset is as seen.
func TestWindowNow(t *testing.T) {
	resets := time.Date(2026, 10, 9, 18, 0, 0, 0, time.UTC)
	w := Window{Minutes: 300, Used: 96, Resets: resets}
	if got := w.Now(resets.Add(-time.Minute)); got != w {
		t.Errorf("before the reset: %+v", got)
	}
	if got := w.Now(resets); got.Used != 0 || !got.Resets.IsZero() || got.Minutes != 300 {
		t.Errorf("at the reset: %+v", got)
	}
	l := Limit{Windows: []Window{{Minutes: 300, Used: 96, Resets: resets}, {Minutes: 10080, Used: 50, Resets: resets.Add(72 * time.Hour)}}}
	if top, _ := l.Tightest(resets.Add(time.Hour)); top.Minutes != 10080 || top.Used != 50 {
		t.Errorf("tightest after the 5-hour reset: %+v", top)
	}
	if top, _ := l.Tightest(resets.Add(-time.Hour)); top.Minutes != 300 {
		t.Errorf("tightest before it: %+v", top)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

// equalLimits compares times as instants.
func equalLimits(a, b []Limit) bool {
	norm := func(ls []Limit) []Limit {
		out := []Limit{}
		for _, l := range ls {
			l.Seen = l.Seen.UTC()
			ws := []Window{}
			for _, w := range l.Windows {
				w.Resets = w.Resets.UTC()
				ws = append(ws, w)
			}
			l.Windows = ws
			out = append(out, l)
		}
		return out
	}
	return reflect.DeepEqual(norm(a), norm(b))
}
