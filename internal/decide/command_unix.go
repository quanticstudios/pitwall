//go:build unix

package decide

import (
	"os/exec"
	"syscall"
)

// ownGroup starts cmd in a process group of its own and makes its cancel
// kill that whole group, so a child the command started cannot outlive the
// timeout.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
