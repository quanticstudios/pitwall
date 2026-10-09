package nowindow

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// Set starts cmd in a hidden console of its own. The release binary is a
// GUI program, so the daemon and the window have no console for a child to
// share, and Windows would open a new, visible one for each.
func Set(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
}
