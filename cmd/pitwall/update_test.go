package main

import (
	"errors"
	"strings"
	"testing"
)

// TestFinishUpdate: a handoff that works ends the update; one that fails
// falls back to the restart and says why; a failed restart is an error.
func TestFinishUpdate(t *testing.T) {
	for _, c := range []struct {
		name              string
		handOff, restart  error
		restarted, failed bool
		says              string
	}{
		{name: "handoff", says: "took over; every program in a pane kept running"},
		{name: "fallback", handOff: errors.New("handoff: refused"), restarted: true, says: "could not take over (handoff: refused), so pitwall restarts the daemon"},
		{name: "both fail", handOff: errors.New("refused"), restart: errors.New("no lock"), restarted: true, failed: true, says: "could not take over (refused)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var out strings.Builder
			restarted := false
			err := finishUpdate(&out, func() error { return c.handOff }, func() error { restarted = true; return c.restart })
			if restarted != c.restarted || (err != nil) != c.failed {
				t.Fatalf("restarted %v, err %v", restarted, err)
			}
			if !strings.Contains(out.String(), c.says) {
				t.Fatalf("said %q, want %q in it", out.String(), c.says)
			}
		})
	}
}
