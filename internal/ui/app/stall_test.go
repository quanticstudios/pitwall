package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestWatchdog checks a long event writes one stack file per stall, none
// for a short one, and that only the newest stallKeep files stay.
func TestWatchdog(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var wd watchdog
	now := time.Now()
	wd.start.Store(now.Add(-time.Second).UnixNano())
	if p := wd.check(now); p != "" {
		t.Fatalf("a 1s event wrote %s", p)
	}
	for i := range stallKeep + 2 {
		at := now.Add(time.Duration(i) * time.Second)
		wd.start.Store(at.Add(-3 * time.Second).UnixNano())
		p := wd.check(at)
		if p == "" {
			t.Fatalf("stall %d wrote no file", i)
		}
		data, err := os.ReadFile(p)
		if err != nil || !strings.Contains(string(data), "TestWatchdog") {
			t.Fatalf("stall file has no stack of this test: %v\n%s", err, data)
		}
		if p := wd.check(at.Add(time.Second)); p != "" {
			t.Fatalf("the same stall wrote a second file %s", p)
		}
	}
	files, _ := filepath.Glob(filepath.Join(os.Getenv("XDG_STATE_HOME"), "pitwall", "stall-*.txt"))
	if len(files) != stallKeep {
		t.Fatalf("kept %d stall files, want %d", len(files), stallKeep)
	}
	wd.end()
	if p := wd.check(now.Add(time.Hour)); p != "" {
		t.Fatalf("an idle window wrote %s", p)
	}
}
