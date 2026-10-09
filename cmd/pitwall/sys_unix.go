//go:build unix

package main

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// ttyPath is the calling process's terminal, for pitwall notify.
const ttyPath = "/dev/tty"

// tryLock takes an exclusive flock on f without waiting; false means another
// process holds it.
func tryLock(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return false, nil
	}
	return err == nil, err
}

// waitLock takes an exclusive flock on f, waiting for it.
func waitLock(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX) }

// detach starts cmd in a session of its own, so it outlives this process.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

// terminate asks pid to exit; a process already gone is no error.
func terminate(pid int) error {
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

// hideConsole is for Windows, where a GUI started from a shortcut opens a
// console window.
func hideConsole() {}

// attachConsole is for Windows, where the release binary is a GUI program
// that has no console of its own.
func attachConsole(string) {}

// startFailed reports a GUI start failure on the desktop.
func startFailed(err error) {
	if runtime.GOOS == "darwin" {
		exec.Command("osascript", "-e", "on run argv", "-e", "display alert \"pitwall could not start\" message (item 1 of argv) as critical", "-e", "end run", "--", err.Error()).Run()
		return
	}
	exec.Command("notify-send", "--app-name=pitwall", "--urgency=critical", "pitwall could not start", err.Error()).Run()
}

// lockHolder is the pid holding an flock on f, from /proc/locks, which
// lists each lock's owner and the device:inode it covers.
func lockHolder(f *os.File) (int, error) {
	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		return 0, err
	}
	data, err := os.ReadFile("/proc/locks")
	if err != nil {
		return 0, err
	}
	suffix := ":" + strconv.FormatUint(st.Ino, 10)
	for _, line := range strings.Split(string(data), "\n") {
		// "1: FLOCK  ADVISORY  WRITE 4056327 00:3a:1234 0 EOF"
		f := strings.Fields(line)
		if len(f) >= 6 && f[1] == "FLOCK" && strings.HasSuffix(f[5], suffix) && devMatches(f[5], uint64(st.Dev)) {
			return strconv.Atoi(f[4])
		}
	}
	return 0, errors.New("no process holds the daemon lock")
}

// devMatches compares /proc/locks' "MAJ:MIN:INODE" (hex major and minor) with
// a stat device number.
func devMatches(field string, dev uint64) bool {
	parts := strings.Split(field, ":")
	if len(parts) != 3 {
		return false
	}
	maj, err1 := strconv.ParseUint(parts[0], 16, 32)
	min, err2 := strconv.ParseUint(parts[1], 16, 32)
	return err1 == nil && err2 == nil && maj == uint64(devMajor(dev)) && min == uint64(devMinor(dev))
}

func devMajor(dev uint64) uint32 { return uint32((dev>>32)&0xfffff000) | uint32((dev>>8)&0x00000fff) }
func devMinor(dev uint64) uint32 { return uint32((dev>>12)&0xffffff00) | uint32(dev&0x000000ff) }
