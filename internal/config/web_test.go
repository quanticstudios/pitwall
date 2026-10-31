package config

import (
	"strings"
	"testing"
)

func TestWeb(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		toml string
		want WebSettings
		prob string
	}{
		{"", WebSettings{}, ""},
		{"[web]\nenabled = true\n", WebSettings{Enabled: true}, ""},
		{"[web]\nenabled = true\nport = 8123\n", WebSettings{Enabled: true, Port: 8123}, ""},
		{"[web]\nenabled = true\nport = 70000\n", WebSettings{}, "web listener stays off"},
		{"[web]\nenabled = true\nport = 1.5\n", WebSettings{}, "not a whole number"},
	} {
		s, probs := LoadFile(write(t, dir, "config.toml", tc.toml))
		if s.Web != tc.want {
			t.Errorf("%q: %+v, want %+v", tc.toml, s.Web, tc.want)
		}
		got := msgs(probs)
		if tc.prob == "" && got != "" || tc.prob != "" && !strings.Contains(got, tc.prob) {
			t.Errorf("%q: problems %q, want %q", tc.toml, got, tc.prob)
		}
	}
}
