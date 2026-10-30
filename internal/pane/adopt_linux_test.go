package pane

import (
	"testing"

	"golang.org/x/sys/unix"
)

// Keep a second pidfd so the assertion can distinguish kernels with exit
// metadata from those that expose only readiness, without guessing a version.
func adoptedExitCode(t *testing.T, pid, code int) func() int {
	t.Helper()
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(fd) })
	return func() int {
		info := unix.PidfdInfo{Mask: unix.PIDFD_INFO_EXIT}
		if unix.IoctlPidfdInfo(fd, &info) == nil && info.Mask&unix.PIDFD_INFO_EXIT != 0 {
			return code
		}
		t.Log("pidfd exit metadata unavailable; status is unknown")
		return -1
	}
}
