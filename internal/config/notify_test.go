package config

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestNotifications(t *testing.T) {
	s, probs := LoadFile(filepath.Join(t.TempDir(), "config.toml"))
	n := s.Notifications
	if len(probs) > 0 || n.Sound != "" || !n.Approval || !n.Input || !n.Done || !n.Failed || n.QuietHours != "" {
		t.Fatalf("defaults %+v %v", n, probs)
	}
	s, probs = LoadFile(write(t, t.TempDir(), "config.toml",
		"[notifications]\nsound = \"bell\"\ndone = false\nmuted_agents = [\"codex\"]\nquiet_hours = \"22:00-08:00\"\n"))
	n = s.Notifications
	if len(probs) > 0 || n.Sound != "bell" || n.Done || !n.Failed || !slices.Equal(n.MutedAgents, []string{"codex"}) || n.QuietFrom != 22*60 || n.QuietTo != 8*60 {
		t.Fatalf("set %+v %v", n, probs)
	}
	at := func(h, m int) time.Time { return time.Date(2026, 1, 1, h, m, 0, 0, time.Local) }
	for _, tc := range []struct {
		h, m  int
		quiet bool
	}{{21, 59, false}, {22, 0, true}, {23, 30, true}, {0, 0, true}, {7, 59, true}, {8, 0, false}, {12, 0, false}} {
		if got := n.Quiet(at(tc.h, tc.m)); got != tc.quiet {
			t.Errorf("22:00-08:00 at %02d:%02d quiet = %v", tc.h, tc.m, got)
		}
	}
	day := NotifySettings{QuietFrom: 9 * 60, QuietTo: 17 * 60}
	if !day.Quiet(at(12, 0)) || day.Quiet(at(17, 0)) || day.Quiet(at(8, 59)) {
		t.Error("09:00-17:00 is wrong")
	}
	if (NotifySettings{}).Quiet(at(0, 0)) {
		t.Error("no quiet hours are quiet")
	}
	for _, bad := range []string{"22-08", "25:00-08:00", "22:00-08:60", "22:00 - 08:00", "22:00"} {
		s, probs = LoadFile(write(t, t.TempDir(), "config.toml", "[notifications]\nquiet_hours = \""+bad+"\"\n"))
		if len(probs) != 1 || !strings.Contains(probs[0].Msg, "quiet_hours") || s.Notifications.QuietHours != "" || s.Notifications.Quiet(at(23, 0)) {
			t.Errorf("%q: %v %+v", bad, probs, s.Notifications)
		}
	}
}
