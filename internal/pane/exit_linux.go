package pane

import (
	"time"

	"golang.org/x/sys/unix"
)

type pidfdExit struct {
	fd, pid int
}

func watchExit(pid int) (exitWatcher, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return nil, err
	}
	return &pidfdExit{fd: fd, pid: pid}, nil
}

func (w *pidfdExit) Close() error { return unix.Close(w.fd) }

func (w *pidfdExit) Wait() (int, error) {
	defer w.Close()
	fds := []unix.PollFd{{Fd: int32(w.fd), Events: unix.POLLIN}}
	for {
		_, err := unix.Poll(fds, -1)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return -1, err
		}
		if fds[0].Revents&(unix.POLLERR|unix.POLLNVAL) != 0 {
			return -1, unix.EBADF
		}
		if fds[0].Revents&(unix.POLLIN|unix.POLLHUP) != 0 {
			break
		}
	}
	// WNOWAIT leaves status for waitChild without decoding siginfo_t's
	// architecture-dependent union. Normal panes still use exec.Cmd.Wait.
	for {
		err := unix.Waitid(unix.P_PIDFD, w.fd, nil, unix.WEXITED|unix.WNOWAIT, nil)
		if err == unix.EINTR {
			continue
		}
		if err == nil {
			return waitChild(w.pid)
		}
		break
	}
	// Recent kernels retain exit status on pidfds even for non-children.
	// Older kernels provide only the exit event; EOF cannot supply a code.
	info := unix.PidfdInfo{Mask: unix.PIDFD_INFO_EXIT}
	if unix.IoctlPidfdInfo(w.fd, &info) != nil {
		return -1, nil
	}
	if info.Mask&unix.PIDFD_INFO_EXIT == 0 {
		// Metadata is published at reaping, after the initial POLLIN. HUP
		// follows reaping; omit POLLIN to avoid spinning on a zombie. Bound
		// this wait because another parent may postpone reaping forever.
		fds[0].Events = 0
		deadline := time.Now().Add(drainTimeout)
		for remaining := time.Until(deadline); remaining > 0; remaining = time.Until(deadline) {
			_, err := unix.Poll(fds, int(remaining/time.Millisecond)+1)
			if err != unix.EINTR {
				break
			}
		}
		info = unix.PidfdInfo{Mask: unix.PIDFD_INFO_EXIT}
		if unix.IoctlPidfdInfo(w.fd, &info) != nil {
			return -1, nil
		}
	}
	if info.Mask&unix.PIDFD_INFO_EXIT != 0 {
		return unix.WaitStatus(info.Exit_code).ExitStatus(), nil
	}
	return -1, nil
}
