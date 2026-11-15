package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
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
		{name: "windows", handOff: errNoHandoff, failed: true, says: "could not take over (Windows has no handoff). The old daemon keeps serving"},
		{name: "both fail", force: true, handOff: errors.New("refused"), restart: errors.New("no lock"), restarted: true, failed: true, says: "could not take over (refused)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var out strings.Builder
			restarted := false
			err := finishUpdate(&out, c.force, binName+" update --restart", func() error { return c.handOff }, func() error { restarted = true; return c.restart })
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

// Anything but --restart and --version with a tag is a usage error before
// any network.
func TestUpdateUsage(t *testing.T) {
	for _, args := range [][]string{{"--force"}, {"--version"}, {"--version", ""}, {"--restart", "--restart"}, {"--version", "v1.0.0", "--version", "v1.0.1"}, {"v1.0.0"}} {
		if err := runUpdate(args, new(strings.Builder)); err == nil || !strings.Contains(err.Error(), "update [--restart] [--version vX.Y.Z]") {
			t.Errorf("%q: got %v, want the usage", args, err)
		}
	}
	for _, c := range []struct {
		args    []string
		restart bool
		tag     string
	}{{nil, false, ""}, {[]string{"--restart"}, true, ""}, {[]string{"--version", "v0.2.1"}, false, "v0.2.1"}, {[]string{"--version", "v0.2.1", "--restart"}, true, "v0.2.1"}} {
		if restart, tag, err := updateArgs(c.args); err != nil || restart != c.restart || tag != c.tag {
			t.Errorf("updateArgs(%q) = %v, %q, %v", c.args, restart, tag, err)
		}
	}
}

// TestUpdateVersion: update --version installs nothing when the tag is not
// a release tag, when a package manager owns the binary, or when the binary
// already runs it, and a tag that was never released fails before any
// write. The drill installs one that was.
func TestUpdateVersion(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	t.Setenv("PITWALL_UPDATE_URL", srv.URL+"/releases/latest")
	dir := t.TempDir()
	exe := filepath.Join(dir, binName)
	managed := filepath.Join(dir, "Cellar", "pitwall", "bin", binName)
	if runtime.GOOS == "windows" {
		managed = filepath.Join(dir, "scoop", "apps", "pitwall", binName)
	}
	for _, p := range []string{exe, managed} {
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte("old"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name, exe, tag string
		requests       bool
		says           string // in the output, or the error with failed
		failed         bool
	}{
		{name: "not a tag", exe: exe, tag: "latest", failed: true, says: `"latest" is not a release tag`},
		{name: "managed", exe: managed, tag: "v0.0.1", says: "A package manager installed this pitwall"},
		{name: "same", exe: exe, tag: "v0.0.2", says: binName + " v0.0.2 is installed already."},
		{name: "never released", exe: exe, tag: "v0.0.9", requests: true, failed: true, says: "install v0.0.9: checksums.txt: GET " + srv.URL + "/releases/download/v0.0.9/checksums.txt: 404"},
	} {
		t.Run(c.name, func(t *testing.T) {
			hits.Store(0)
			var out strings.Builder
			got, err := installUpdate(&out, c.exe, "v0.0.2", c.tag)
			if got != "v0.0.2" || (err != nil) != c.failed || (hits.Load() > 0) != c.requests {
				t.Fatalf("installUpdate = %q, %v after %d requests", got, err, hits.Load())
			}
			if said := out.String() + fmt.Sprint(err); !strings.Contains(said, c.says) {
				t.Fatalf("said %q, want %q in it", said, c.says)
			}
			if b, _ := os.ReadFile(c.exe); string(b) != "old" {
				t.Fatalf("binary = %q", b)
			}
		})
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Fatalf("left behind: %v", entries)
	}
}
