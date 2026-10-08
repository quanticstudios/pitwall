package app

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quanticstudios/pitwall/internal/update"
)

// Version is this build's version, set by main. Only a release tag checks
// for updates.
var Version string

// Relaunch opens a window on session, showing tab workspace, in a new
// process; main sets it. Nil leaves "Restart to finish" to the user.
var Relaunch func(session, workspace string) error

// recheckAfter is how long since the last check before regaining focus
// checks again; tests shorten it.
var recheckAfter = 30 * time.Minute

// checkLatest asks GitHub for the latest release; tests swap it.
var checkLatest = func(ctx context.Context) (update.Release, bool, error) {
	return update.Check(ctx, http.DefaultClient, update.LatestURL, Version)
}

const (
	// updateEvery is how often a window checks GitHub for a release.
	updateEvery = time.Hour
	// updateTimeout bounds one install's downloads.
	updateTimeout = 10 * time.Minute
)

// The update button's labels after a click.
const (
	updating     = "Updating…"
	updated      = "Restart to finish"
	updateFailed = "Update failed"
)

// updater checks for a newer release and installs it on a click. The daemon
// is never touched: it keeps running the old binary, which serves the new
// windows while their proto.Version matches. Otherwise the new window asks
// before restarting it, as after any upgrade.
type updater struct {
	on atomic.Bool // [updates] check

	mu   sync.Mutex
	rel  update.Release // the newer release, Tag "" while there is none
	step string         // "", updating, updated or updateFailed
	poke chan struct{}  // focus regained; made by watch
}

// focused asks watch to check again if the last check is recheckAfter old.
func (up *updater) focused() {
	up.mu.Lock()
	defer up.mu.Unlock()
	select {
	case up.poke <- struct{}{}:
	default: // watch not started, or a poke already waits
	}
}

// watch checks at start, every updateEvery, and when the window regains
// focus recheckAfter after the last check, until stop closes.
func (up *updater) watch(stop <-chan struct{}, invalidate func()) {
	if !update.Supported || managed() {
		return
	}
	poke := make(chan struct{}, 1)
	up.mu.Lock()
	up.poke = poke
	up.mu.Unlock()
	var last time.Time
	for {
		if up.on.Load() {
			last = time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			rel, newer, err := checkLatest(ctx)
			cancel()
			switch {
			case err != nil:
				log.Printf("update check: %q", err)
			case newer:
				up.mu.Lock()
				if up.rel.Tag != rel.Tag && up.step == "" {
					log.Printf("update: %s is out, this is %s", rel.Tag, Version)
					up.rel = rel
					invalidate()
				}
				up.mu.Unlock()
			}
		}
		timer := time.NewTimer(updateEvery)
		for waiting := true; waiting; {
			select {
			case <-stop:
				timer.Stop()
				return
			case <-timer.C:
				waiting = false
			case <-poke:
				if time.Since(last) >= recheckAfter {
					timer.Stop()
					waiting = false
				}
			}
		}
	}
}

// label is the update button's text, "" to hide it.
func (up *updater) label() string {
	up.mu.Lock()
	defer up.mu.Unlock()
	if up.step == "" && up.rel.Tag != "" && up.on.Load() {
		return "Update"
	}
	return up.step
}

// click installs the release in the background, or after an install
// relaunches the window on session and reports true: close this one.
func (up *updater) click(session, workspace string, invalidate func()) (relaunched bool) {
	up.mu.Lock()
	defer up.mu.Unlock()
	switch {
	case up.step == updating || up.rel.Tag == "":
		return false
	case up.step == updated:
		if Relaunch == nil || session == "" || workspace == "" {
			return false
		}
		if err := Relaunch(session, workspace); err != nil {
			log.Printf("update: relaunch: %q", err)
			return false
		}
		log.Printf("update: relaunched on %s", up.rel.Tag)
		return true
	}
	up.step = updating
	rel := up.rel
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), updateTimeout)
		defer cancel()
		err := installRelease(ctx, rel)
		up.mu.Lock()
		if err != nil {
			log.Printf("update to %s: %q", rel.Tag, err)
			up.step = updateFailed
		} else {
			log.Printf("update: installed %s", rel.Tag)
			up.step = updated
		}
		up.mu.Unlock()
		invalidate()
	}()
	return false
}

// managed is whether a package manager owns the running binary; tests
// swap it.
var managed = func() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return update.Managed(exe)
}

// installRelease replaces the running binary with rel's; tests swap it.
var installRelease = func(ctx context.Context, rel update.Release) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return update.Install(ctx, http.DefaultClient, rel, exe)
}
