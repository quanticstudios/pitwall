package main

import (
	"slices"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
)

func TestParseReport(t *testing.T) {
	r, err := parseReport([]string{"--name", "My agent", "--state", "blocked", "--message", "Choose a file", "--", "my-agent", "--resume", "a b"})
	if err != nil || r.Name != "My agent" || r.State != "blocked" || r.Message != "Choose a file" || !slices.Equal(r.ResumeCmd, []string{"my-agent", "--resume", "a b"}) {
		t.Fatalf("%+v, %v", r, err)
	}
	for _, tc := range []struct {
		args    []string
		nilCmd  bool
		release bool
	}{
		{[]string{"--name", "x"}, true, false},
		{[]string{"--name", "x", "--no-resume"}, false, false},
		{[]string{"--name", "x", "--release"}, true, true},
	} {
		r, err := parseReport(tc.args)
		if err != nil || (r.ResumeCmd == nil) != tc.nilCmd || r.Release != tc.release {
			t.Errorf("%v: %+v, %v", tc.args, r, err)
		}
	}
	for _, args := range [][]string{
		nil, {"--unknown"}, {"--name", "x", "--state", "busy"},
		{"--name", "x", "--no-resume", "--", "x"},
		{"--name", "x", "--release", "--state", "done"},
	} {
		if _, err := parseReport(args); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}

func TestReportUsesHook(t *testing.T) {
	sock, got := silentDaemon(t)
	t.Setenv("PITWALL_PANE", "p1")
	t.Setenv("PITWALL_SOCKET", sock)
	if err := runReportAgent([]string{"--name", "My agent", "--state", "working"}); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-got:
		r, err := agent.DecodeReport(ev.Payload)
		if err != nil || ev.Pane != "p1" || ev.Provider != "report" || r.Name != "My agent" || r.State != "working" {
			t.Fatalf("%+v, %+v, %v", ev, r, err)
		}
	case <-time.After(time.Second):
		t.Fatal("no report")
	}
	t.Setenv("PITWALL_PANE", "")
	if err := runReportAgent([]string{"--name", "x"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-got:
		t.Fatal("report outside a pane")
	default:
	}
}
