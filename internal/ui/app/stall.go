package app

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/pprof"
	"slices"
	"sync/atomic"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
)

const (
	// stallAfter is how long one window event may take before the watchdog
	// dumps stacks. Compositors ping the window and flag it as not
	// responding after a few seconds; Gio answers pings only between events.
	stallAfter = 2 * time.Second
	stallKeep  = 5 // stall files kept in the state directory
)

// watchdog records when the window started handling an event, so a check
// from another goroutine can see an event that has run too long. Times are
// monotonic offsets from origin, so a wall clock change neither fakes a
// stall nor hides one.
type watchdog struct {
	origin time.Time
	start  atomic.Int64 // nanoseconds since origin plus one when the current event began, 0 between events
	dumped int64        // the start of the stall already dumped; check's goroutine only
}

func newWatchdog() *watchdog { return &watchdog{origin: time.Now()} }

func (wd *watchdog) begin() { wd.start.Store(int64(time.Since(wd.origin)) + 1) }
func (wd *watchdog) end()   { wd.start.Store(0) }

// watch checks once a second until stop closes.
func (wd *watchdog) watch(stop <-chan struct{}) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			wd.check(time.Since(wd.origin))
		}
	}
}

// check, given the time since origin, writes every goroutine's stack to a
// new stall-*.txt in the state directory, once per stall, and keeps the
// newest stallKeep files. It returns the file's path, or "" when nothing
// was written.
func (wd *watchdog) check(now time.Duration) string {
	s := wd.start.Load()
	if s == 0 || s == wd.dumped {
		return ""
	}
	ran := now - time.Duration(s-1)
	if ran < stallAfter {
		return ""
	}
	wd.dumped = s
	dir := config.StateDir()
	path, err := writeStacks(dir, ran)
	if err != nil {
		log.Printf("window event stalled for %v; writing stacks: %v", ran.Round(time.Second), err)
		return ""
	}
	log.Printf("window event stalled for %v; stacks in %s", ran.Round(time.Second), path)
	pruneStalls(dir, path)
	return path
}

// writeStacks writes the stacks to a new file in dir; windows sharing the
// state directory never overwrite each other's.
func writeStacks(dir string, d time.Duration) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, fmt.Sprintf("stall-%d-*.txt", time.Now().Unix()))
	if err != nil {
		return "", err
	}
	fmt.Fprintf(f, "pitwall %s: one window event has run for %v\n\n", time.Now().Format(time.RFC3339), d.Round(time.Millisecond))
	err = pprof.Lookup("goroutine").WriteTo(f, 2)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return f.Name(), err
}

// pruneStalls keeps the newest stallKeep stall files by modification time,
// always keeping the one just written.
func pruneStalls(dir, keep string) {
	paths, _ := filepath.Glob(filepath.Join(dir, "stall-*.txt"))
	if len(paths) <= stallKeep {
		return
	}
	type file struct {
		path string
		mod  time.Time
	}
	var files []file
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil {
			files = append(files, file{p, fi.ModTime()})
		}
	}
	slices.SortFunc(files, func(a, b file) int {
		switch {
		case a.path == keep:
			return -1
		case b.path == keep:
			return 1
		}
		return b.mod.Compare(a.mod) // newest first
	})
	for _, f := range files[min(stallKeep, len(files)):] {
		os.Remove(f.path)
	}
}
