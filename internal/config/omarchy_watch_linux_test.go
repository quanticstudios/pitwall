package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestWatchOmarchy: a theme switch, done the way Omarchy does it, calls
// changed once its burst of events settles, and LoadOmarchy then reads the
// new theme; a file Omarchy keeps beside the theme does not.
func TestWatchOmarchy(t *testing.T) {
	cur := omarchyAt(t, "tokyo-night")
	stop := make(chan struct{})
	defer close(stop)
	calls := make(chan struct{}, 8)
	go WatchOmarchy(stop, func() { calls <- struct{}{} })
	time.Sleep(50 * time.Millisecond) // the watch is armed

	os.WriteFile(filepath.Join(cur, "background"), nil, 0o644)
	select {
	case <-calls:
		t.Fatal("changed for an unrelated file")
	case <-time.After(3 * omarchyDebounce):
	}

	applyOmarchy(t, cur, "flexoki-light")
	select {
	case <-calls:
	case <-time.After(2 * time.Second):
		t.Fatal("no change after a theme switch")
	}
	select {
	case <-calls:
		t.Fatal("one switch called changed twice")
	case <-time.After(3 * omarchyDebounce):
	}
	if th, name, err := LoadOmarchy(); err != nil || name != "Flexoki Light" || th.Terminal.Background != "#fffcf0" {
		t.Fatalf("after the switch: %q %s %v", name, th.Terminal.Background, err)
	}
}

// TestWatchOmarchyAbsent: without Omarchy the watcher returns at once.
func TestWatchOmarchyAbsent(t *testing.T) {
	t.Setenv(OmarchyDirEnv, filepath.Join(t.TempDir(), "none"))
	done := make(chan struct{})
	go func() { WatchOmarchy(make(chan struct{}), func() {}); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("WatchOmarchy waits on a machine without Omarchy")
	}
}
