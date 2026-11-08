package main

import (
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func TestCLIKillWorkingAgent(t *testing.T) {
	for _, args := range [][]string{{"kill", "1"}, {"kill", "-f", "1"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			st := cliState()
			st.Activities = []model.Activity{{WorkspaceID: "a", PaneID: "pa", Provider: model.ProviderClaude, State: model.StateWorking}}
			fakeCLI(t, cliExchange{state: st}, cliExchange{request: proto.KillSession{WorkspaceID: "a"}, state: st})
			if code, out, stderr := cliOutput(args...); code != 0 || out != "" || stderr != "" {
				t.Fatalf("%d: %s %s", code, out, stderr)
			}
		})
	}
}
