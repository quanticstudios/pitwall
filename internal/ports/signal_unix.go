//go:build linux || darwin

package ports

import (
	"errors"
	"syscall"
)

// Signal sends SIGTERM, or SIGKILL when force, to pid, or to the process
// group pid leads when group. A process already gone is no error.
func Signal(pid int, group, force bool) error {
	sig := syscall.SIGTERM
	if force {
		sig = syscall.SIGKILL
	}
	target := pid
	if group {
		target = -pid
	}
	if err := syscall.Kill(target, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

// Supported reports whether Procs and Signal work on this system.
const Supported = true
