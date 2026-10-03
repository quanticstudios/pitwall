package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	run := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		code := runConfig(args, &out, &errb)
		return code, out.String() + errb.String()
	}
	path := filepath.Join(dir, "pitwall", "config.toml")
	if code, out := run("path"); code != 0 || strings.TrimSpace(out) != path {
		t.Fatalf("path: %d %q", code, out)
	}
	if code, out := run("default"); code != 0 || !strings.HasPrefix(out, "#:schema "+filepath.Join(dir, "pitwall", "schema.json")+"\n") {
		t.Fatalf("default: %d %q", code, out[:min(80, len(out))])
	}
	if code, out := run("check"); code != 0 || !strings.HasSuffix(out, ": ok\n") {
		t.Fatalf("check without a file: %d %q", code, out)
	}
	if code, _ := run("init"); code != 0 {
		t.Fatal("init failed")
	}
	for _, f := range []string{"config.toml", "schema.json", "theme.schema.json"} {
		if _, err := os.Stat(filepath.Join(dir, "pitwall", f)); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(path, []byte("[keys]\nnext_sesion = \"Alt+J\"\n"), 0o644)
	if code, _ := run("init"); code != 1 {
		t.Fatal("init overwrote an existing config")
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "next_sesion") {
		t.Fatal("init changed the existing config")
	}
	if code, out := run("check"); code != 1 || !strings.HasPrefix(out, "config.toml:2: keys.next_sesion: unknown key") {
		t.Fatalf("check: %d %q", code, out)
	}
	os.WriteFile(path, []byte("[keys]\nnext_session = \"Alt+J\"\n"), 0o644)
	if code, out := run("check"); code != 0 || !strings.HasPrefix(out, "config.toml:2: keys.next_session: renamed to next_tab") {
		t.Fatalf("check with an old action name: %d %q", code, out)
	}
	if code, out := run("schema"); code != 0 || !strings.Contains(out, `"next_tab"`) {
		t.Fatalf("schema: %d", code)
	}
	if code, _ := run("nope"); code != 2 {
		t.Fatal("unknown subcommand accepted")
	}
}
