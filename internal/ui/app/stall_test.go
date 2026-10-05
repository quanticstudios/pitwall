package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestWatchdog checks a long event writes one stack file per stall, none
// for a short one, that stalls in the same second get files of their own,
// and that the newest stallKeep files by modification time stay.
func TestWatchdog(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := filepath.Join(os.Getenv("XDG_STATE_HOME"), "pitwall")
	wd := newWatchdog()
	stallAt := func(at time.Duration) { wd.start.Store(int64(at-3*time.Second) + 1) }

	wd.start.Store(int64(time.Hour-time.Second) + 1)
	if p := wd.check(time.Hour); p != "" {
		t.Fatalf("a 1s event wrote %s", p)
	}
	// An old file from another window, older than everything written here.
	stale := filepath.Join(dir, "stall-1-stale.txt")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(stale, nil, 0o600)
	os.Chtimes(stale, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour))

	var written []string
	for i := range stallKeep + 2 {
		at := time.Hour + time.Duration(i)*time.Millisecond // all in the same wall-clock second
		stallAt(at)
		p := wd.check(at)
		if p == "" {
			t.Fatalf("stall %d wrote no file", i)
		}
		if slices.Contains(written, p) {
			t.Fatalf("stall %d reused %s", i, p)
		}
		written = append(written, p)
		data, err := os.ReadFile(p)
		if err != nil || !strings.Contains(string(data), "TestWatchdog") {
			t.Fatalf("stall file has no stack of this test: %v\n%s", err, data)
		}
		if p := wd.check(at + time.Second); p != "" {
			t.Fatalf("the same stall wrote a second file %s", p)
		}
		// Distinct modification times, so pruning has an order to keep.
		mod := time.Now().Add(time.Duration(i-30) * time.Minute)
		os.Chtimes(p, mod, mod)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "stall-*.txt"))
	slices.Sort(files)
	want := slices.Clone(written[len(written)-stallKeep:])
	slices.Sort(want)
	if !slices.Equal(files, want) {
		t.Fatalf("kept %v, want the newest %v", files, want)
	}
	wd.end()
	if p := wd.check(2 * time.Hour); p != "" {
		t.Fatalf("an idle window wrote %s", p)
	}
}
