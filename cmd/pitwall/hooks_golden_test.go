//go:build unix

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/quanticstudios/pitwall/internal/agent"
)

// Hooks pitwall v0.2.13 installed for a binary stay as they are for the
// binary at that path now, in either build: no installed hook needs an
// edit after an update, and the only change is an event added since. testdata/hooks-v0.2.13 holds what v0.2.13's hooks
// install wrote for testBin.
func TestHooksFromV0213Unchanged(t *testing.T) {
	home := hooksHome(t)
	files := map[string]string{
		"claude-settings.json": filepath.Join(home, ".claude", "settings.json"),
		"codex-hooks.json":     filepath.Join(home, ".codex", "hooks.json"),
		"pi-pitwall.ts":        filepath.Join(home, ".pi", "agent", "extensions", "pitwall.ts"),
	}
	for name, path := range files {
		data, err := os.ReadFile(filepath.Join("testdata", "hooks-v0.2.13", name))
		if err != nil {
			t.Fatal(err)
		}
		writeHooksTestFile(t, path, string(data))
	}
	plans, err := planHooks([]string{"install"})
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, p := range plans {
		switch p.path {
		case files["claude-settings.json"], files["codex-hooks.json"]:
			var want []string
			if p.path == files["codex-hooks.json"] {
				want = []string{"added SessionStart: '/opt/pitwall/bin/pitwall' hook codex"} // v0.3.2 added it beside the rest
			}
			if p.skip != "" || !slices.Equal(p.changes, want) {
				t.Errorf("%s: skip %q, changes %q", p.path, p.skip, p.changes)
			}
		case files["pi-pitwall.ts"]:
			// why: a newer template may replace the extension; it must
			// still run the same binary.
			if p.skip != "" {
				t.Errorf("%s: skip %q", p.path, p.skip)
			}
			if !bytes.Contains(agent.PiExtension(testBin), []byte(`const bin = "/opt/pitwall/bin/pitwall";`)) {
				t.Errorf("%s: the extension install would write runs another binary", p.path)
			}
		default:
			continue
		}
		seen++
	}
	if seen != len(files) {
		t.Fatalf("planned %d of the %d files: %+v", seen, len(files), plans)
	}
}
