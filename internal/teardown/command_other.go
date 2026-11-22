//go:build !windows

package teardown

import "os/exec"

// treeKill is a no-op here: childproc.Bound already kills cmd's process group.
func treeKill(*exec.Cmd) {}
