package config

import (
	"strings"
	"testing"
	"time"
)

// park_idle_after is off unless set to a positive duration; a bad one is
// reported and stays off.
func TestParkIdleAfter(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  time.Duration
		bad   bool
	}{
		{"", 0, false},
		{`"0s"`, 0, false},
		{`"1h"`, time.Hour, false},
		{`"20s"`, 20 * time.Second, false},
		{`"-1m"`, 0, true},
		{`"soon"`, 0, true},
		{`60`, 0, true},
	} {
		body := "[agents]\n"
		if tc.value != "" {
			body += "park_idle_after = " + tc.value + "\n"
		}
		s, problems := LoadFile(write(t, t.TempDir(), "config.toml", body))
		if s.ParkIdleAfter != tc.want || (len(problems) > 0) != tc.bad {
			t.Errorf("%q: %s, problems %v", tc.value, s.ParkIdleAfter, problems)
		}
	}
	if !strings.Contains(Default(), "park_idle_after = ") || !strings.Contains(string(Schema()), `"park_idle_after"`) {
		t.Error("park_idle_after missing from defaults or schema")
	}
}
