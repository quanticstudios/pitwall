package pane

import (
	"syscall"

	"golang.org/x/sys/unix"
)

type kqueueExit struct {
	fd, pid int
}

func watchExit(pid int) (exitWatcher, error) {
	syscall.ForkLock.Lock()
	fd, err := unix.Kqueue()
	if err == nil {
		unix.CloseOnExec(fd)
	}
	syscall.ForkLock.Unlock()
	if err != nil {
		return nil, err
	}
	w := &kqueueExit{fd: fd, pid: pid}
	change := unix.Kevent_t{
		Ident: uint64(pid), Filter: unix.EVFILT_PROC,
		Flags:  unix.EV_ADD | unix.EV_ONESHOT,
		Fflags: unix.NOTE_EXIT | unix.NOTE_EXITSTATUS,
	}
	for {
		_, err = unix.Kevent(fd, []unix.Kevent_t{change}, nil, nil)
		if err != unix.EINTR {
			break
		}
	}
	if err != nil {
		w.Close()
		return nil, err
	}
	return w, nil
}

func (w *kqueueExit) Close() error { return unix.Close(w.fd) }

func (w *kqueueExit) Wait() (int, error) {
	defer w.Close()
	var events [1]unix.Kevent_t
	for {
		n, err := unix.Kevent(w.fd, nil, events[:], nil)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return -1, err
		}
		if n == 0 {
			continue
		}
		e := events[0]
		if e.Flags&unix.EV_ERROR != 0 {
			return -1, unix.Errno(e.Data)
		}
		if e.Fflags&unix.NOTE_EXIT == 0 {
			continue
		}
		if code, err := waitChild(w.pid); err == nil {
			return code, nil
		}
		if e.Fflags&unix.NOTE_EXITSTATUS != 0 {
			return unix.WaitStatus(e.Data).ExitStatus(), nil
		}
		return -1, nil
	}
}
