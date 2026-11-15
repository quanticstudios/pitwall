// Package childproc bounds background commands and their descendants.
package childproc

import (
	"os/exec"
	"time"
)

// Bound makes cancelling cmd stop its descendants too, where the platform
// allows, and stops a descendant that keeps a pipe open from holding Wait
// past one second. cmd must come from exec.CommandContext.
func Bound(cmd *exec.Cmd) {
	ownGroup(cmd)
	cmd.WaitDelay = time.Second
}
