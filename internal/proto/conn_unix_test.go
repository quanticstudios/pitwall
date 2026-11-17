//go:build unix

package proto

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

func TestSocketPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	if got, err := SocketPath(); err != nil || got != filepath.Join(dir, "pitwall", "pitwall.sock") {
		t.Fatalf("SocketPath() = %q, %v", got, err)
	}
	info, err := os.Lstat(filepath.Join(dir, "pitwall"))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("socket directory permissions: %v, %v", info, err)
	}
}

func TestSocketPathRejectsUnsafeDirectory(t *testing.T) {
	for _, kind := range []string{"group read", "group write", "group execute", "other read", "other write", "other execute", "symlink", "file", "foreign owner"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("XDG_RUNTIME_DIR", root)
			dir := filepath.Join(root, "pitwall")
			switch kind {
			case "symlink":
				if err := os.Symlink(t.TempDir(), dir); err != nil {
					t.Fatal(err)
				}
			case "file":
				if err := os.WriteFile(dir, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if kind == "foreign owner" {
					if err := os.Chown(dir, os.Getuid()+1, -1); err != nil {
						t.Skipf("cannot create a foreign-owned directory: %v", err)
					}
				} else {
					bits := map[string]os.FileMode{"group read": 0o040, "group write": 0o020, "group execute": 0o010, "other read": 0o004, "other write": 0o002, "other execute": 0o001}
					if err := os.Chmod(dir, 0o700|bits[kind]); err != nil {
						t.Fatal(err)
					}
				}
			}
			if path, err := SocketPath(); err == nil || path != "" {
				t.Fatalf("SocketPath() = %q, %v; want an error and no path", path, err)
			}
		})
	}
}

func TestSocketPathWithoutXDGRuntimeDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	uid := strconv.Itoa(os.Getuid())
	for _, c := range []struct {
		name    string
		run     os.FileMode // the mode of <runUser>/<uid>, 0 for none
		tmpSock string      // "live", "stale", "file", or no socket
		runSock string
		want    string // "run" or "tmp"
	}{
		{"no runtime folder", 0, "", "", "tmp"},
		{"the user's runtime folder", 0o700, "", "", "run"},
		{"a runtime folder others can enter", 0o755, "", "", "tmp"},
		{"a daemon from before in tmp", 0o700, "live", "", "tmp"},
		{"a daemon in each", 0o700, "live", "live", "run"},
		{"a stale run socket beside a live tmp daemon", 0o700, "live", "stale", "tmp"},
		{"a stale tmp socket", 0o700, "stale", "", "run"},
		{"a file in place of the tmp socket", 0o700, "file", "", "run"},
		{"stale sockets in both", 0o700, "stale", "stale", "run"},
	} {
		t.Run(c.name, func(t *testing.T) {
			// Unix socket paths must fit even with a long test name.
			root, err := os.MkdirTemp("", "pw-runtime-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(root) })
			runUser, tmpRoot = filepath.Join(root, "run"), filepath.Join(root, "tmp")
			t.Cleanup(func() { runUser, tmpRoot = "/run/user", "/tmp" })
			run := filepath.Join(runUser, uid)
			tmp := filepath.Join(tmpRoot, "pitwall-"+uid)
			if c.run != 0 {
				if err := os.MkdirAll(run, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(run, c.run); err != nil {
					t.Fatal(err)
				}
			}
			touch := func(dir, kind string) {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(dir, "pitwall.sock")
				if kind == "file" {
					if err := os.WriteFile(path, nil, 0o600); err != nil {
						t.Fatal(err)
					}
					return
				}
				ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
				if err != nil {
					t.Fatal(err)
				}
				if kind == "stale" {
					ln.SetUnlinkOnClose(false)
					ln.Close()
					return
				}
				done := make(chan struct{})
				go func() {
					defer close(done)
					for {
						conn, err := ln.Accept()
						if err != nil {
							return
						}
						conn.Close()
					}
				}()
				t.Cleanup(func() { ln.Close(); <-done })
			}
			if c.tmpSock != "" {
				touch(tmp, c.tmpSock)
			}
			if c.runSock != "" {
				touch(filepath.Join(run, "pitwall"), c.runSock)
			}
			want := filepath.Join(tmp, "pitwall.sock")
			if c.want == "run" && runtime.GOOS == "linux" {
				want = filepath.Join(run, "pitwall", "pitwall.sock")
			}
			if got, err := SocketPath(); err != nil || got != want {
				t.Fatalf("SocketPath() = %q, %v; want %q", got, err, want)
			}
		})
	}
}

func TestSocketPathRuntimeOwnership(t *testing.T) {
	for _, kind := range []string{"symlink", "foreign owner"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("XDG_RUNTIME_DIR", "")
			root := t.TempDir()
			runUser, tmpRoot = filepath.Join(root, "run"), filepath.Join(root, "tmp")
			t.Cleanup(func() { runUser, tmpRoot = "/run/user", "/tmp" })
			if err := os.MkdirAll(runUser, 0o700); err != nil {
				t.Fatal(err)
			}
			run := filepath.Join(runUser, strconv.Itoa(os.Getuid()))
			if kind == "symlink" {
				if err := os.Symlink(t.TempDir(), run); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(run, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chown(run, os.Getuid()+1, -1); err != nil {
					t.Skipf("cannot create a foreign-owned runtime directory: %v", err)
				}
			}
			want := filepath.Join(tmpRoot, "pitwall-"+strconv.Itoa(os.Getuid()), "pitwall.sock")
			if got, err := SocketPath(); err != nil || got != want {
				t.Fatalf("SocketPath() = %q, %v; want %q", got, err, want)
			}
		})
	}
}

// An explicit XDG directory always wins, including over an old tmp socket.
func TestSocketPathExplicitRuntimeUnchanged(t *testing.T) {
	root := t.TempDir()
	runUser, tmpRoot = filepath.Join(root, "run"), filepath.Join(root, "tmp")
	t.Cleanup(func() { runUser, tmpRoot = "/run/user", "/tmp" })
	uid := strconv.Itoa(os.Getuid())
	for _, dir := range []string{filepath.Join(runUser, uid, "pitwall"), filepath.Join(tmpRoot, "pitwall-"+uid)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "pitwall.sock"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	explicit := filepath.Join(root, "explicit")
	t.Setenv("XDG_RUNTIME_DIR", explicit)
	if got, err := SocketPath(); err != nil || got != filepath.Join(explicit, "pitwall", "pitwall.sock") {
		t.Fatalf("SocketPath() = %q, %v", got, err)
	}
}
