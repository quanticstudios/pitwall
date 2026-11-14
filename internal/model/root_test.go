package model

import "testing"

// A worker on another machine hangs under its parent here though its group
// is its own machine's; a tab here with a parent in another group does not.
func TestRootRemoteParent(t *testing.T) {
	s := State{Workspaces: []Workspace{
		{ID: "p", SessionID: "s", ProjectID: "here"},
		{ID: "w", SessionID: "s", ProjectID: "there", Parent: "p", RemoteParent: true},
		{ID: "l", SessionID: "s", ProjectID: "other", Parent: "p"},
	}}
	if got := s.Root(s.Workspaces[1]); got != "p" {
		t.Errorf("remote worker's root %q, want p", got)
	}
	if got := s.Root(s.Workspaces[2]); got != "" {
		t.Errorf("a tab of another group hangs under %q", got)
	}
}
