//go:build unix

package main

import (
	"encoding/json"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
)

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

// TestProgramReport turns pitwall status <state>'s arguments into an OSC
// 7501 body and refuses what the daemon would discard.
func TestProgramReport(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"working", "--app", "cargo", "--progress", "40", "--message", "Compiling"}, "state=working:progress=40:app=cargo:msg=Q29tcGlsaW5n"},
		{[]string{"blocked", "--message", "Approve?"}, "state=blocked:msg=QXBwcm92ZT8="},
		{[]string{"done"}, "state=done"},
		{[]string{"error", "--app", "make"}, "state=error:app=make"},
		{[]string{"idle"}, "state=idle"},
	} {
		if got, err := programReport(c.args); err != nil || got != c.want {
			t.Errorf("%q: %q, %v, want %q", c.args, got, err, c.want)
		}
	}
	for _, args := range [][]string{
		{"working", "--progress", "101"},
		{"done", "--progress", "50"},
		{"working", "--app", "my app"},
		{"working", "--app", strings.Repeat("a", 33)},
		{"working", "--message", "two\nlines"},
		{"working", "extra"},
		{"working", "--color", "red"},
	} {
		if _, err := programReport(args); err == nil {
			t.Errorf("%q was taken", args)
		}
	}
}

// TestProgramUsesHook sends the report as report-agent's hook, with the
// caller's process group and no name, which an older daemon refuses.
func TestProgramUsesHook(t *testing.T) {
	sock, got := silentDaemon(t)
	t.Setenv("PITWALL_PANE", "p1")
	t.Setenv("PITWALL_SOCKET", sock)
	if err := statusCommand([]string{"working", "--app", "cargo"}); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-got:
		var p struct {
			Program string
			Group   int
		}
		if err := json.Unmarshal(ev.Payload, &p); err != nil || ev.Pane != "p1" || ev.Provider != "report" ||
			p.Program != "state=working:app=cargo" || p.Group != syscall.Getpgrp() {
			t.Fatalf("%+v, %+v, %v", ev, p, err)
		}
		if _, err := agent.DecodeReport(ev.Payload); err == nil {
			t.Fatal("an agent report decoder took it")
		}
	case <-time.After(time.Second):
		t.Fatal("no report")
	}
}
