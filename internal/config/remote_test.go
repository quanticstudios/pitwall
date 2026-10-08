package config

import (
	"strings"
	"testing"
)

func TestRemote(t *testing.T) {
	dir := t.TempDir()
	s, probs := LoadFile(write(t, dir, "config.toml", ""))
	if want := (RemoteSettings{Listen: "127.0.0.1:7777"}); s.Remote != want || len(probs) > 0 {
		t.Fatalf("defaults: %+v %v", s.Remote, msgs(probs))
	}
	// Enabling it keeps loopback.
	s, probs = LoadFile(write(t, dir, "config.toml", "[remote]\nenabled = true\n"))
	if !s.Remote.Enabled || s.Remote.Listen != DefaultRemoteListen || s.Remote.TLS || len(probs) > 0 {
		t.Fatalf("enabled: %+v %v", s.Remote, msgs(probs))
	}

	for _, tc := range []struct {
		toml string
		on   bool
		prob string
	}{
		{"listen = \"0.0.0.0:7777\"", false, "needs tls = true"},
		{"listen = \":7777\"", false, "needs tls = true"},
		{"listen = \"192.168.1.5:7777\"", false, "needs tls = true"},
		{"listen = \"0.0.0.0:7777\"\ntls = true", true, ""},
		{"listen = \"[::1]:7777\"", true, ""},
		{"listen = \"localhost:9000\"", true, ""},
		{"listen = \"127.0.0.1\"", false, "not host:port"},
		{"listen = \"127.0.0.1:99999\"", false, "not host:port"},
	} {
		s, probs := LoadFile(write(t, dir, "config.toml", "[remote]\nenabled = true\n"+tc.toml+"\n"))
		if s.Remote.Enabled != tc.on {
			t.Errorf("%s: enabled = %v, want %v", tc.toml, s.Remote.Enabled, tc.on)
		}
		got := msgs(probs)
		if tc.prob == "" && got != "" || tc.prob != "" && !strings.Contains(got, tc.prob) {
			t.Errorf("%s: problems %q, want %q", tc.toml, got, tc.prob)
		}
	}
}
