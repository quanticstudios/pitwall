package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestFinishUpdate: a handoff that works ends the update; one that fails
// falls back to the restart and says why; a failed restart is an error.
func TestFinishUpdate(t *testing.T) {
	for _, c := range []struct {
		name              string
		handOff, restart  error
		restarted, failed bool
		says              string
	}{
		{name: "handoff", says: "took over; every program in a pane kept running"},
		{name: "fallback", handOff: errors.New("handoff: refused"), restarted: true, says: "could not take over (handoff: refused), so pitwall restarts the daemon"},
		{name: "both fail", handOff: errors.New("refused"), restart: errors.New("no lock"), restarted: true, failed: true, says: "could not take over (refused)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var out strings.Builder
			restarted := false
			err := finishUpdate(&out, func() error { return c.handOff }, func() error { restarted = true; return c.restart })
			if restarted != c.restarted || (err != nil) != c.failed {
				t.Fatalf("restarted %v, err %v", restarted, err)
			}
			if !strings.Contains(out.String(), c.says) {
				t.Fatalf("said %q, want %q in it", out.String(), c.says)
			}
		})
	}
}

// TestPrintSibling: update names the other flavor in its folder, and the
// command that updates it, unless it already runs the new version or is
// this binary through a link.
func TestPrintSibling(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixtures are shell scripts")
	}
	dir := t.TempDir()
	script := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	core := script("pitwall-core", "echo 'pitwall v2 (core)'\n")
	line := func() string {
		var b strings.Builder
		printSibling(&b, core, "v2")
		return b.String()
	}
	if got := line(); got != "" {
		t.Errorf("no sibling: %q", got)
	}
	desktop := script("pitwall", "echo 'pitwall v1'\n")
	if got, want := line(), desktop+" next to it is v1 and updates on its own: "+desktop+" update\n"; got != want {
		t.Errorf("older sibling:\n got %q\nwant %q", got, want)
	}
	script("pitwall", "echo 'pitwall v2'\n")
	if got := line(); got != "" {
		t.Errorf("same version: %q", got)
	}
	script("pitwall", "exit 1\n")
	if got := line(); !strings.Contains(got, "a build that cannot run (exit status 1)") {
		t.Errorf("broken sibling: %q", got)
	}
	os.Remove(desktop)
	if err := os.Symlink("pitwall-core", desktop); err != nil {
		t.Fatal(err)
	}
	if got := line(); got != "" {
		t.Errorf("link: %q", got)
	}
}
