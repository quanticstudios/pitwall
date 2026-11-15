// Package childproc bounds background commands and their descendants.
package childproc

import (
	"os/exec"
	"time"
)

// Bound makes cancellation stop cmd's process tree and bounds pipe drains.
// cmd must come from exec.CommandContext. The returned cleanup stops any
// descendants left after cmd exits and must run after Run or Output returns.
func Bound(cmd *exec.Cmd) func() {
	ownGroup(cmd)
	cmd.WaitDelay = time.Second
	return func() {
		if cmd.Process != nil {
			_ = cmd.Cancel()
		}
	}
}
