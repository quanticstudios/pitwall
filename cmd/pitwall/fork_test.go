package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func TestForkCommand(t *testing.T) {
	for _, address := range []string{"", "#1", "@lp", "#1.1"} {
		t.Run(address, func(t *testing.T) {
			before := hostState("ls", "lw", "lp")
			before.Workspaces[0].Tabs[0].Layout = &layout.Node{Pane: "lp"}
			after := before
			after.Workspaces = append(after.Workspaces, model.Workspace{ID: "fork", SessionID: "ls", Name: "side"})
			fakeCLI(t, cliExchange{state: before, level: proto.Level}, cliExchange{request: proto.ForkPane{Pane: "lp", Name: "side", Worktree: "approach"}, state: after})
			t.Setenv("PITWALL_PANE", "lp")
			args := []string{"--worktree", "approach", "-n", "side"}
			if address != "" {
				args = append([]string{address}, args...)
			}
			var out bytes.Buffer
			if err := forkCommand(args, &out); err != nil || out.String() != "#2\n" {
				t.Fatalf("fork = %q, %v", out.String(), err)
			}
		})
	}
}

func TestForkArguments(t *testing.T) {
	for _, args := range [][]string{{"one", "two"}, {"--worktree"}, {"--unknown"}} {
		if err := forkCommand(args, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), forkUsage) {
			t.Errorf("fork %q = %v", args, err)
		}
	}
}

func TestForkOldDaemon(t *testing.T) {
	fakeCLI(t, cliExchange{state: hostState("s", "w", "p"), level: proto.ForkLevel - 1})
	if err := forkCommand(nil, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "newer daemon") {
		t.Fatalf("fork = %v", err)
	}
}
