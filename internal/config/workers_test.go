package config

import (
	"strings"
	"testing"
	"time"
)

func TestCloseDoneWorkers(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  time.Duration
		bad   bool
	}{
		{"", 10 * time.Minute, false},
		{`"off"`, 0, false},
		{`"30m"`, 30 * time.Minute, false},
		{`"1s"`, time.Second, false},
		{`"0s"`, 10 * time.Minute, true},
		{`"-1m"`, 10 * time.Minute, true},
		{`"soon"`, 10 * time.Minute, true},
		{`60`, 10 * time.Minute, true},
	} {
		body := "[agents]\n"
		if tc.value != "" {
			body += "close_done_workers = " + tc.value + "\n"
		}
		s, problems := LoadFile(write(t, t.TempDir(), "config.toml", body))
		if s.CloseDoneWorkers != tc.want || (len(problems) > 0) != tc.bad {
			t.Errorf("%s: %s, problems %v", tc.value, s.CloseDoneWorkers, problems)
		}
	}
	if !strings.Contains(Default(), "close_done_workers = ") || !strings.Contains(string(Schema()), `"close_done_workers"`) {
		t.Fatal("missing from defaults or schema")
	}
}
