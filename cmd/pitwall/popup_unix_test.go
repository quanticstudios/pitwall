//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
