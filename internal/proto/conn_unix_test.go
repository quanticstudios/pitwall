//go:build unix

package proto

import (
	"os"
	"path/filepath"
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
