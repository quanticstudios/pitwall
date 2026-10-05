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
// from another goroutine can see an event that has run too long.
type watchdog struct {
	start  atomic.Int64 // UnixNano when the current event began, 0 between events
	dumped int64        // the start of the stall already dumped; check's goroutine only
}

func (wd *watchdog) begin() { wd.start.Store(time.Now().UnixNano()) }
func (wd *watchdog) end()   { wd.start.Store(0) }

// watch checks once a second until stop closes.
func (wd *watchdog) watch(stop <-chan struct{}) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-t.C:
			wd.check(now)
		}
	}
}

// check writes every goroutine's stack to stall-<unix>.txt in the state
// directory, once per stall, and keeps the newest stallKeep files. It
// returns the file's path, or "" when nothing was written.
func (wd *watchdog) check(now time.Time) string {
	s := wd.start.Load()
	if s == 0 || s == wd.dumped || now.Sub(time.Unix(0, s)) < stallAfter {
		return ""
	}
	wd.dumped = s
	dir := config.StateDir()
	path := filepath.Join(dir, fmt.Sprintf("stall-%d.txt", now.Unix()))
	err := os.MkdirAll(dir, 0o700)
	if err == nil {
		err = writeStacks(path, now.Sub(time.Unix(0, s)))
	}
	if err != nil {
		log.Printf("pitwall: window event stalled for %v; writing stacks: %v", now.Sub(time.Unix(0, s)).Round(time.Second), err)
		return ""
	}
	log.Printf("pitwall: window event stalled for %v; stacks in %s", now.Sub(time.Unix(0, s)).Round(time.Second), path)
	if old, _ := filepath.Glob(filepath.Join(dir, "stall-*.txt")); len(old) > stallKeep {
		slices.Sort(old) // same-width unix seconds sort by time
		for _, p := range old[:len(old)-stallKeep] {
			os.Remove(p)
		}
	}
	return path
}

func writeStacks(path string, d time.Duration) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	fmt.Fprintf(f, "pitwall %s: one window event has run for %v\n\n", time.Now().Format(time.RFC3339), d.Round(time.Millisecond))
	err = pprof.Lookup("goroutine").WriteTo(f, 2)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
