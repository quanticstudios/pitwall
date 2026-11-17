//go:build unix

package proto

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"
)

// runUser is where systemd-logind makes each user's runtime folder, and
// tmpRoot holds the fallback directory. Tests point them elsewhere.
var runUser, tmpRoot = "/run/user", "/tmp"

// runtimeDir is the socket directory when XDG_RUNTIME_DIR is unset. On
// Linux that is <runUser>/<uid>/pitwall when the user owns that runtime
// folder: a login without PAM, as over Tailscale SSH, sets no
// XDG_RUNTIME_DIR, yet the daemon a desktop login started listens there.
// A daemon already listening in /tmp/pitwall-<uid> keeps that directory,
// so remote-start joins it instead of starting a second one.
func runtimeDir() string {
	tmp := filepath.Join(tmpRoot, fmt.Sprintf("pitwall-%d", os.Getuid()))
	run := filepath.Join(runUser, strconv.Itoa(os.Getuid()))
	info, err := os.Lstat(run)
	if runtime.GOOS != "linux" || err != nil || !info.IsDir() || private(run, info) != nil {
		return tmp
	}
	dir := filepath.Join(run, "pitwall")
	if !runtimeSocketLive(dir) && runtimeSocketLive(tmp) {
		return tmp
	}
	return dir
}

// A socket file can outlive its daemon. Prefer a running daemon over a
// stale socket, and validate its directory before making the probe.
func runtimeSocketLive(dir string) bool {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || private(dir, info) != nil {
		return false
	}
	conn, err := net.DialTimeout("unix", filepath.Join(dir, "pitwall.sock"), 200*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// private rejects a socket directory another user owns or can enter.
func private(dir string, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("socket directory %s must belong to uid %d", dir, os.Getuid())
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("socket directory %s must have no group or other permissions", dir)
	}
	return nil
}
