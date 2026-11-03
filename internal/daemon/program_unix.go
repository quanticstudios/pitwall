//go:build unix

package daemon

import "golang.org/x/sys/unix"

// groupAlive reports whether process group pg has a process left. A group
// pitwall may not signal still has one.
var groupAlive = func(pg int) bool {
	err := unix.Kill(-pg, 0)
	return err == nil || err == unix.EPERM
}
