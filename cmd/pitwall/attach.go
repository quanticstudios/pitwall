package main

import (
	"fmt"

	"github.com/quanticstudios/pitwall/internal/model"
)

// attachGUI opens a window on the tab's session unless one shows it: that
// window got the FocusSession already and raises itself.
func attachGUI(state model.State, session, workspaceID string) error {
	s := state.Session(session)
	if s == nil || s.Windows > 0 {
		return nil
	}
	return launchGUI(s.Name, workspaceID)
}

// needsWindow is why pitwall-core refuses to show session, which it can
// only do by raising a window that shows it already, or nil. It runs
// before any request, so a refusal changes nothing.
func needsWindow(state model.State, session string) error {
	s := state.Session(session)
	if canOpenWindow || s == nil || s.Windows > 0 {
		return nil
	}
	return fmt.Errorf("no window shows session %s, and pitwall-core cannot open one; open it with the desktop pitwall", s.Name)
}
