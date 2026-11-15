//go:build unix

package childproc

import (
	"os/exec"
	"syscall"
)

func ownGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		// why: once Wait has reaped the leader, its pid, the group's id, can
		// belong to another process group. Signal knows; a raw kill does not.
		if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
			return err
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
