package main

import (
	"os"
	"os/exec"

	"github.com/quanticstudios/pitwall/internal/model"
)

// launchGUI opens a window on session in a process of its own, showing tab
// workspaceID when set.
var launchGUI = func(session, workspaceID string) error {
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, "-s", session)
	if workspaceID != "" {
		cmd.Env = append(os.Environ(), "PITWALL_ATTACH="+workspaceID)
	}
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// attachGUI opens a window on the tab's session unless one shows it: that
// window got the FocusSession already and raises itself.
func attachGUI(state model.State, session, workspaceID string) error {
	s := state.Session(session)
	if s == nil || s.Windows > 0 {
		return nil
	}
	return launchGUI(s.Name, workspaceID)
}
