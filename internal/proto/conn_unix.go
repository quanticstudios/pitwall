//go:build unix

package proto

import (
	"fmt"
	"os"
	"syscall"
)

func runtimeDir() string { return fmt.Sprintf("/tmp/pitwall-%d", os.Getuid()) }

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
