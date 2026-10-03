package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// lockRange is the byte LockFileEx locks: far past the pid the daemon writes
// at the start of its lock file, because a Windows lock also blocks reads.
var lockRange = windows.Overlapped{OffsetHigh: 1}

func lockFile(f *os.File, flags uint32) error {
	ol := lockRange
	return windows.LockFileEx(windows.Handle(f.Fd()), flags|windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &ol)
}

// tryLock takes an exclusive lock on f without waiting; false means another
// process holds it.
func tryLock(f *os.File) (bool, error) {
	err := lockFile(f, windows.LOCKFILE_FAIL_IMMEDIATELY)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return err == nil, err
}

// waitLock takes an exclusive lock on f, waiting for it.
func waitLock(f *os.File) error { return lockFile(f, 0) }

// detach starts cmd in a hidden console of its own, outside this console's
// Ctrl+C. The console is still there for the git and shell processes the
// daemon starts, so none of them opens a window.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP}
}

// terminate stops pid. Windows has no SIGTERM for a process without a shared
// console, so the daemon gets no final save; it saves shortly after every
// change anyway.
func terminate(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return nil // already gone
	}
	defer p.Release()
	if err := p.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

// hideConsole drops the console window Windows opens for a GUI started from
// a shortcut or Explorer, where pitwall is the console's only process. A
// console shared with a terminal stays.
func hideConsole() {
	k := windows.NewLazySystemDLL("kernel32.dll")
	var pids [2]uint32
	if n, _, _ := k.NewProc("GetConsoleProcessList").Call(uintptr(unsafe.Pointer(&pids[0])), 2); n == 1 {
		k.NewProc("FreeConsole").Call()
	}
}

// startFailed reports a GUI start failure in a message box.
func startFailed(err error) {
	title, _ := windows.UTF16PtrFromString("pitwall could not start")
	text, _ := windows.UTF16PtrFromString(err.Error())
	windows.MessageBox(0, text, title, windows.MB_ICONERROR|windows.MB_OK)
}

// lockHolder finds no holder: only Linux lists lock owners, and only Linux
// had daemons that wrote no pid.
func lockHolder(*os.File) (int, error) {
	return 0, errors.New("no process holds the daemon lock")
}
