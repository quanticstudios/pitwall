package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestFinishUpdate: a handoff that works ends the update. One that fails,
// for any reason, keeps the old daemon and fails naming update --restart;
// with --restart it falls back to the restart and says why, and a failed
// restart is an error.
func TestFinishUpdate(t *testing.T) {
	for _, c := range []struct {
		name              string
		force             bool
		handOff, restart  error
		restarted, failed bool
		says              string
	}{
		{name: "handoff", says: "took over; every program in a pane kept running"},
		{name: "refused", handOff: errors.New("the old daemon refused: frozen"), failed: true, says: "could not take over (the old daemon refused: frozen). The old daemon keeps serving"},
		{name: "other version", handOff: errors.New("the old daemon cannot hand off: daemon speaks protocol version 15"), failed: true, says: binName + " update --restart restarts it"},
		{name: "handoff version", handOff: errors.New("handoff request handoffRequest{Version:2}, want version 1"), failed: true, says: "keep running on the old build"},
		{name: "fallback", force: true, handOff: errors.New("handoff: refused"), restarted: true, says: "could not take over (handoff: refused), so pitwall restarts the daemon"},
		{name: "both fail", force: true, handOff: errors.New("refused"), restart: errors.New("no lock"), restarted: true, failed: true, says: "could not take over (refused)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var out strings.Builder
			restarted := false
			err := finishUpdate(&out, c.force, func() error { return c.handOff }, func() error { restarted = true; return c.restart })
			if restarted != c.restarted || (err != nil) != c.failed {
				t.Fatalf("restarted %v, err %v", restarted, err)
			}
			if said := out.String() + fmt.Sprint(err); !strings.Contains(said, c.says) {
				t.Fatalf("said %q, want %q in it", said, c.says)
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

// A bare word other than --restart is a usage error before any network.
func TestUpdateUsage(t *testing.T) {
	if err := runUpdate([]string{"--force"}, new(strings.Builder)); err == nil || !strings.Contains(err.Error(), "update [--restart]") {
		t.Fatalf("got %v, want the usage", err)
	}
}
