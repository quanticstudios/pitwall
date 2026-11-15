package main

import (
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

// hostState is a daemon's state with one session, tab and pane.
func hostState(session, tab, pane string) model.State {
	return model.State{
		Version:    1,
		Sessions:   []model.Session{{ID: session, Name: "main"}},
		Workspaces: []model.Workspace{{ID: tab, SessionID: session, Tabs: []model.Tab{{ID: tab + "t"}}, ActiveTab: tab + "t"}},
		Panes:      []model.Pane{{ID: pane, WorkspaceID: tab}},
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}
