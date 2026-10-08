package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
