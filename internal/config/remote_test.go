package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestRemote(t *testing.T) {
	dir := t.TempDir()
	s, probs := LoadFile(write(t, dir, "config.toml", ""))
	want := RemoteSettings{
		Listen:    "127.0.0.1:7777",
		Tailscale: RemoteTailscaleSettings{Hostname: DefaultTailscaleHostname("")},
	}
	if !reflect.DeepEqual(s.Remote, want) || len(probs) > 0 {
		t.Fatalf("defaults: %+v want %+v probs=%v", s.Remote, want, msgs(probs))
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

func TestRemoteTailscale(t *testing.T) {
	dir := t.TempDir()
	// Valid opt-in with normalized hostname, users, and tags.
	sampleKey := "tskey-" + "auth-" + "example"
	s, probs := LoadFile(write(t, dir, "config.toml", `[remote.tailscale]
enabled = true
hostname = "Pitwall-Work"
auth_key = "`+sampleKey+`"
allow_users = ["Alice@Example.com", "bob@example.com", "alice@example.com"]
allow_tags = ["Tag:Phones", "tag:phones", "tag:ops"]
`))
	if len(probs) > 0 {
		t.Fatalf("unexpected problems: %v", msgs(probs))
	}
	ts := s.Remote.Tailscale
	if !ts.Enabled || ts.Hostname != "pitwall-work" || ts.AuthKey != sampleKey {
		t.Fatalf("tailscale = %+v", ts)
	}
	if !reflect.DeepEqual(ts.AllowUsers, []string{"alice@example.com", "bob@example.com"}) {
		t.Errorf("allow_users = %v", ts.AllowUsers)
	}
	if !reflect.DeepEqual(ts.AllowTags, []string{"tag:phones", "tag:ops"}) {
		t.Errorf("allow_tags = %v", ts.AllowTags)
	}

	// Bad hostname disables built-in Tailscale and reports a problem.
	s, probs = LoadFile(write(t, dir, "config.toml", "[remote.tailscale]\nenabled = true\nhostname = \"bad host.name\"\n"))
	if s.Remote.Tailscale.Enabled {
		t.Fatalf("bad hostname stayed enabled")
	}
	if got := msgs(probs); !strings.Contains(got, "not a valid DNS label") {
		t.Errorf("bad hostname probs = %q", got)
	}

	// Bad tag and empty user are ignored with problems; valid entries survive.
	s, probs = LoadFile(write(t, dir, "config.toml", `[remote.tailscale]
enabled = true
allow_users = ["", "carol@example.com"]
allow_tags = ["phones", "tag:valid"]
`))
	if !s.Remote.Tailscale.Enabled {
		t.Fatalf("valid tailscale disabled by bad tag/user: %v", msgs(probs))
	}
	if !reflect.DeepEqual(s.Remote.Tailscale.AllowUsers, []string{"carol@example.com"}) {
		t.Errorf("allow_users = %v", s.Remote.Tailscale.AllowUsers)
	}
	if !reflect.DeepEqual(s.Remote.Tailscale.AllowTags, []string{"tag:valid"}) {
		t.Errorf("allow_tags = %v", s.Remote.Tailscale.AllowTags)
	}
	if got := msgs(probs); !strings.Contains(got, "empty user login") || !strings.Contains(got, "not a valid Tailscale tag") {
		t.Errorf("probs = %q", got)
	}

	// DefaultTailscaleHostname sanitization.
	for in, want := range map[string]string{
		"MyLaptop.local":        "pitwall-mylaptop",
		"dev_box 01":            "pitwall-dev-box-01",
		"---":                   "pitwall",
		strings.Repeat("a", 80): "pitwall-" + strings.Repeat("a", 55),
	} {
		if got := DefaultTailscaleHostname(in); got != want {
			t.Errorf("DefaultTailscaleHostname(%q) = %q, want %q", in, got, want)
		}
	}

	// Default() and Schema() include [remote.tailscale].
	if d := Default(); !strings.Contains(d, "[remote.tailscale]") || !strings.Contains(d, "# allow_users = []") || !strings.Contains(d, "# allow_tags = []") {
		t.Errorf("Default() missing [remote.tailscale]")
	}
	if sc := string(Schema()); !strings.Contains(sc, `"tailscale"`) || !strings.Contains(sc, `"allow_tags"`) {
		t.Errorf("Schema() missing tailscale fields")
	}
}
