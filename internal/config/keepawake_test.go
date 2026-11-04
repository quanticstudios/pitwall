package config

import (
	"strings"
	"testing"
	"time"
)

func TestKeepAwake(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  time.Duration
		bad   bool
	}{
		{"", 15 * time.Minute, false},
		{`"0s"`, 0, false},
		{`"1h30m"`, 90 * time.Minute, false},
		{`"500ms"`, 500 * time.Millisecond, false},
		{`"-1m"`, 15 * time.Minute, true},
		{`"15"`, 15 * time.Minute, true},
		{`"forever"`, 15 * time.Minute, true},
		{`15`, 15 * time.Minute, true},
	} {
		body := "[agents]\nkeep_awake = true\n"
		if tc.value != "" {
			body += "keep_awake_grace = " + tc.value + "\n"
		}
		s, problems := LoadFile(write(t, t.TempDir(), "config.toml", body))
		if !s.KeepAwake || s.KeepAwakeGrace != tc.want || (len(problems) > 0) != tc.bad {
			t.Errorf("%q: on %v, grace %s, problems %v", tc.value, s.KeepAwake, s.KeepAwakeGrace, problems)
		}
	}
	s, _ := LoadFile(write(t, t.TempDir(), "config.toml", ""))
	if s.KeepAwake || s.KeepAwakeGrace != DefaultKeepAwakeGrace {
		t.Fatalf("defaults: on %v, grace %s", s.KeepAwake, s.KeepAwakeGrace)
	}
	for _, key := range []string{"keep_awake", "keep_awake_grace"} {
		if !strings.Contains(Default(), key+" = ") || !strings.Contains(string(Schema()), `"`+key+`"`) {
			t.Errorf("%s missing from defaults or schema", key)
		}
	}
}
