//go:build !unix

package daemon

// groupAlive is always true: Windows panes report no foreground group, so
// no record there has one.
var groupAlive = func(int) bool { return true }
