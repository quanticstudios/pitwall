package main

import (
	"errors"
	"log"
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ttyPath is the calling process's terminal, for pitwall notify.
const ttyPath = "CONOUT$"

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

var kernel32 = windows.NewLazySystemDLL("kernel32.dll")

// hideConsole drops the console window Windows opens for a console build
// (go build without -H windowsgui) started from a shortcut or Explorer,
// where pitwall is the console's only process. A console shared with a
// terminal stays.
func hideConsole() {
	var pids [2]uint32
	if n, _, _ := kernel32.NewProc("GetConsoleProcessList").Call(uintptr(unsafe.Pointer(&pids[0])), 2); n == 1 {
		kernel32.NewProc("FreeConsole").Call()
	}
}

// attachConsole gives a command the console of the terminal that ran it.
// The release binary is a GUI program (-H windowsgui), so Windows opens no
// console for it: the window starts without one flashing up, and a hook an
// agent runs without a console stays silent. Output redirected to a pipe
// or a file keeps it. The window and the daemon attach to nothing, so
// closing the terminal does not end them.
func attachConsole(cmd string) {
	switch cmd {
	case "", "-s", "--host", "daemon":
		return
	}
	redirected := func(std uint32) bool {
		h, err := windows.GetStdHandle(std)
		if err != nil || h == 0 {
			return false
		}
		t, _ := windows.GetFileType(h)
		return t == windows.FILE_TYPE_DISK || t == windows.FILE_TYPE_PIPE
	}
	in, out, errOut := redirected(windows.STD_INPUT_HANDLE), redirected(windows.STD_OUTPUT_HANDLE), redirected(windows.STD_ERROR_HANDLE)
	// Fails in a console build, which has its console already, and when
	// the parent has none.
	if r, _, _ := kernel32.NewProc("AttachConsole").Call(uintptr(attachParent)); r == 0 {
		return
	}
	reopen := func(f **os.File, std uint32, name string) {
		h, err := windows.CreateFile(windows.StringToUTF16Ptr(name), windows.GENERIC_READ|windows.GENERIC_WRITE,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
		if err != nil {
			return
		}
		windows.SetStdHandle(std, h)
		*f = os.NewFile(uintptr(h), name)
	}
	if !in {
		reopen(&os.Stdin, windows.STD_INPUT_HANDLE, "CONIN$")
	}
	if !out {
		reopen(&os.Stdout, windows.STD_OUTPUT_HANDLE, "CONOUT$")
	}
	if !errOut {
		reopen(&os.Stderr, windows.STD_ERROR_HANDLE, "CONOUT$")
		log.SetOutput(os.Stderr)
	}
}

// attachParent is ATTACH_PARENT_PROCESS, (DWORD)-1.
const attachParent = ^uint32(0)

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
