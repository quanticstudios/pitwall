package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/proto"
)

func TestParsePopup(t *testing.T) {
	sh, err := filepath.Abs(mustLook(t, "sh"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := parsePopup([]string{"--width", "60%", "lead", "--height", "20", "--title", "pick", "--dir", ".", "--wait", "--", "sh", "-c", "fzf"})
	if err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	want := proto.OpenPopup{Cmd: []string{sh, "-c", "fzf"}, Dir: wd, Width: "60%", Height: "20", Title: "pick"}
	if p.tab != "lead" || !p.wait || p.open.Dir != want.Dir || p.open.Width != want.Width || p.open.Height != want.Height ||
		p.open.Title != want.Title || !slices.Equal(p.open.Cmd, want.Cmd) {
		t.Fatalf("got %+v", p)
	}
	if p, err := parsePopup(nil); err != nil || p.open.Cmd != nil || p.wait {
		t.Fatalf("no args: %+v %v", p, err)
	}
	for _, bad := range [][]string{{"--width", "0"}, {"--height", "120%"}, {"a", "b"}, {"--nope"}, {"--", "no-such-command-xyz"}} {
		if _, err := parsePopup(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func mustLook(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skip(name, "not on PATH")
	}
	return p
}

// popup --wait returns the command's exit code, and a second popup on the
// same tab is refused while the first is open.
func TestCLIPopupWait(t *testing.T) {
	_, run := talkDaemon(t)
	run(0, "#1\n", "new", "-n", "lead", "--", "sh")
	start := time.Now()
	run(3, "", "popup", "--wait", "lead", "--", "sh", "-c", "exit 3")
	run(0, "", "popup", "--wait", "lead", "--", "true")
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("waits took %v", took)
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "where")
	run(0, "", "popup", "--wait", "--dir", dir, "lead", "--", "sh", "-c", "pwd > where")
	if b, err := os.ReadFile(out); err != nil || strings.TrimSpace(string(b)) != dir {
		t.Fatalf("ran in %q, %v; want %s", b, err, dir)
	}
	run(0, "", "popup", "lead", "--", "sleep", "30")
	if stderr := run(1, "", "popup", "lead", "--", "true"); !strings.Contains(stderr, "popup open already") {
		t.Fatalf("second popup: %q", stderr)
	}
}
