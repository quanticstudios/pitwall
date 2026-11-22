package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/remote"
)

func TestRunRemote(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	run := func(args ...string) (int, string, string) {
		var out, errs strings.Builder
		code := runRemote(args, &out, &errs)
		return code, out.String(), errs.String()
	}
	if code, _, errs := run("pair"); code != 1 || !strings.Contains(errs, "off") {
		t.Fatalf("pair while off: %d %q", code, errs)
	}
	os.MkdirAll(config.Dir(), 0o755)
	os.WriteFile(config.Path(), []byte("[remote]\nenabled = true\nurl = \"https://laptop.tail1.ts.net\"\n"), 0o644)
	code, out, errs := run("pair", "my", "pixel")
	if code != 0 || !strings.Contains(out, "https://laptop.tail1.ts.net/#pair=") || !strings.Contains(out, "█") ||
		!strings.Contains(out, `"my pixel"`) || strings.Contains(out, "tailscale serve") {
		t.Fatalf("pair: %d %q\n%s", code, errs, out)
	}
	if _, err := os.Stat(filepath.Join(remote.Dir(), "pair.json")); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := run("devices"); code != 0 || !strings.Contains(out, "No paired devices") {
		t.Fatalf("devices: %d %q", code, out)
	}
	if code, _, errs := run("revoke", "my pixel"); code != 1 || !strings.Contains(errs, "no paired device") {
		t.Fatalf("revoke unknown: %d %q", code, errs)
	}
	// Loopback without url: the hint to put it on the tailnet.
	os.WriteFile(config.Path(), []byte("[remote]\nenabled = true\n"), 0o644)
	if code, out, _ := run("pair"); code != 0 || !strings.Contains(out, "http://127.0.0.1:7777/#pair=") || !strings.Contains(out, "tailscale serve --bg 7777") {
		t.Fatalf("loopback pair: %d\n%s", code, out)
	}
	if code, _, _ := run("frob"); code != 2 {
		t.Fatalf("unknown command: %d", code)
	}
}

func TestRunRemoteTailscale(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	oldRunning, oldWait, oldPoll, oldLogout := remoteDaemonRunning, remoteTailscaleWait, remoteTailscalePoll, remoteLogoutDir
	t.Cleanup(func() {
		remoteDaemonRunning, remoteTailscaleWait, remoteTailscalePoll, remoteLogoutDir = oldRunning, oldWait, oldPoll, oldLogout
	})
	remoteTailscaleWait = 200 * time.Millisecond
	remoteTailscalePoll = 10 * time.Millisecond

	run := func(args ...string) (int, string, string) {
		var out, errs strings.Builder
		code := runRemote(args, &out, &errs)
		return code, out.String(), errs.String()
	}

	// Status while off and daemon not running.
	remoteDaemonRunning = func() bool { return false }
	code, out, errs := run("tailscale", "status", "--json")
	if code != 0 || errs != "" {
		t.Fatalf("status json: %d %q %q", code, out, errs)
	}
	var st tailscaleStatusJSON
	if err := json.Unmarshal([]byte(out), &st); err != nil || st.Enabled || st.State != remote.TailscaleOff || st.DaemonRunning {
		t.Fatalf("status json payload: %+v err=%v", st, err)
	}

	// Turning on without daemon writes config.toml and explains next step.
	code, out, errs = run("tailscale", "on", "--hostname", "pitwall-demo")
	if code != 0 || !strings.Contains(out, `device "pitwall-demo"`) {
		t.Fatalf("tailscale on (no daemon): %d %q %q", code, out, errs)
	}
	cfg, probs := config.Load()
	if len(probs) > 0 || !cfg.Remote.Tailscale.Enabled || cfg.Remote.Tailscale.Hostname != "pitwall-demo" {
		t.Fatalf("loaded config: %+v probs=%v", cfg.Remote.Tailscale, probs)
	}

	// With daemon running and needs_login status, `on` prints the login URL.
	remoteDaemonRunning = func() bool { return true }
	if err := remote.WriteTailscaleStatus(remote.Dir(), remote.TailscaleStatus{
		Enabled:  true,
		State:    remote.TailscaleNeedsLogin,
		Hostname: "pitwall-demo",
		AuthURL:  "https://login.tailscale.com/a/test123",
	}); err != nil {
		t.Fatal(err)
	}
	code, out, errs = run("tailscale", "on")
	if code != 0 || !strings.Contains(out, "https://login.tailscale.com/a/test123") {
		t.Fatalf("tailscale on (needs_login): %d %q %q", code, out, errs)
	}

	// With daemon running and running status, `status` prints URL and owner.
	if err := remote.WriteTailscaleStatus(remote.Dir(), remote.TailscaleStatus{
		Enabled:  true,
		State:    remote.TailscaleRunning,
		Hostname: "pitwall-demo",
		DNSName:  "pitwall-demo.tail1.ts.net",
		URL:      "https://pitwall-demo.tail1.ts.net/",
		SelfUser: "alice@example.com",
		IPs:      []string{"100.64.0.10"},
	}); err != nil {
		t.Fatal(err)
	}
	code, out, errs = run("tailscale", "status")
	if code != 0 || !strings.Contains(out, "https://pitwall-demo.tail1.ts.net/") || !strings.Contains(out, "alice@example.com (owner)") {
		t.Fatalf("tailscale status (running): %d %q %q", code, out, errs)
	}

	// Turning off without daemon calls offline logout and --delete-state removes tsnet dir.
	remoteDaemonRunning = func() bool { return false }
	if err := os.MkdirAll(remote.TailscaleDir(remote.Dir()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote.TailscaleDir(remote.Dir()), "tailscaled.state"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	loggedOut := false
	remoteLogoutDir = func(_ context.Context, dir, host string) error {
		loggedOut = dir == remote.TailscaleDir(remote.Dir()) && host == "pitwall-demo"
		return nil
	}
	code, out, errs = run("tailscale", "off", "--delete-state")
	if code != 0 || !loggedOut || remote.TailscaleStateExists(remote.Dir()) || !strings.Contains(out, "deleted") {
		t.Fatalf("tailscale off --delete-state: %d loggedOut=%v exists=%v out=%q errs=%q", code, loggedOut, remote.TailscaleStateExists(remote.Dir()), out, errs)
	}
	cfg, _ = config.Load()
	if cfg.Remote.Tailscale.Enabled {
		t.Fatal("tailscale still enabled in config after off")
	}

	// Bad subcommand exits 2.
	if code, _, _ := run("tailscale", "bogus"); code != 2 {
		t.Fatalf("bad tailscale subcommand: %d", code)
	}
}
