//go:build unix

package decide

import (
	"os/exec"
	"syscall"
)

// ownGroup starts cmd in a process group of its own and makes its cancel
// kill that whole group, children in it included. A child that leaves the
// group (setsid, daemonizing) is not killed; detaching it makes it the
// provider's to stop.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
