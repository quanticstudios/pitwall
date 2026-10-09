package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/update"
)

// TestUpdateButton walks the button from "Update" through a failed and a
// good install to the relaunch.
func TestUpdateButton(t *testing.T) {
	var fail bool
	install := installRelease
	installRelease = func(context.Context, update.Release) error {
		if fail {
			return errors.New("checksum")
		}
		return nil
	}
	var to [2]string
	Relaunch = func(session, ws string) error { to = [2]string{session, ws}; return nil }
	t.Cleanup(func() { installRelease, Relaunch = install, nil })

	var up updater
	if up.label() != "" {
		t.Fatal("a button before any release")
	}
	up.rel = update.Release{Tag: "v0.1.0-alpha.19"}
	if up.label() != "" {
		t.Fatal("a button with [updates] check off")
	}
	up.on.Store(true)
	done := make(chan struct{}, 1)
	invalidate := func() { done <- struct{}{} }
	for _, c := range []struct {
		fail bool
		want string
	}{{true, updateFailed}, {false, updated}} {
		fail = c.fail
		if up.click("main", "w1", invalidate) {
			t.Fatal("relaunched before installing")
		}
		<-done
		if got := up.label(); got != c.want {
			t.Fatalf("label = %q, want %q", got, c.want)
		}
	}
	if !up.click("main", "w1", invalidate) || to != [2]string{"main", "w1"} {
		t.Fatalf("restart did not relaunch on main/w1: %v", to)
	}
}

// TestUpdateRecheck: regaining focus checks again only once recheckAfter
// has passed since the last check; it does not wait for the hourly timer.
func TestUpdateRecheck(t *testing.T) {
	checks := make(chan struct{}, 8)
	check, after := checkLatest, recheckAfter
	checkLatest = func(context.Context) (update.Release, bool, error) {
		checks <- struct{}{}
		return update.Release{}, false, nil
	}
	t.Cleanup(func() { checkLatest, recheckAfter = check, after })
	got := func(want bool, what string) {
		t.Helper()
		select {
		case <-checks:
			if !want {
				t.Fatalf("%s: checked", what)
			}
		case <-time.After(200 * time.Millisecond):
			if want {
				t.Fatalf("%s: no check", what)
			}
		}
	}
	// run starts a watch with recheckAfter d and returns its stop.
	run := func(d time.Duration) (*updater, func()) {
		recheckAfter = d
		up := &updater{}
		up.on.Store(true)
		stop, done := make(chan struct{}), make(chan struct{})
		go func() { up.watch(stop, func() {}); close(done) }()
		got(true, "start")
		return up, func() { close(stop); <-done }
	}

	up, stop := run(time.Hour)
	up.focused()
	got(false, "focus right after a check")
	stop()

	up, stop = run(0)
	up.focused()
	got(true, "focus after recheckAfter")
	stop()
}
