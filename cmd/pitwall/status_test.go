//go:build unix

package main

import "testing"

// TestStatusCommand sets a tab's status line by tab, by pane address and
// from inside the pane, and clears it, as ls --json shows.
func TestStatusCommand(t *testing.T) {
	_, run := talkDaemon(t)
	run(0, "#1\n", "new", "-n", "lead", "--", "sh")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"lead", "--", "CI", "3/7"}, `"status":"CI 3/7"`},
		{[]string{"1.1", "waiting on review"}, `"status":"waiting on review"`},
		{[]string{"lead"}, `"status":""`},
		{[]string{"@inside", "--", "own"}, `"status":"own"`},
		{[]string{"--clear", "lead"}, `"status":""`},
	} {
		if c.args[0] == "@inside" {
			t.Setenv("PITWALL_PANE", paneID(t, "1.1"))
			c.args = c.args[1:]
		}
		run(0, "", append([]string{"status"}, c.args...)...)
		run(0, c.want, "ls", "--json")
	}
	if stderr := run(1, "", "status", "lead", "a", "b"); stderr == "" {
		t.Fatal("three arguments were taken")
	}
}
