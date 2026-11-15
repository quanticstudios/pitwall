//go:build !unix

package childproc

import "os/exec"

// shortcut: cancelling stops only cmd itself, through its handle, and a
// descendant outlives it. taskkill /T by pid could reach another process
// once Windows reuses that pid; use a job object if leaks matter there.
func ownGroup(*exec.Cmd) {}
