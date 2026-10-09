//go:build !windows

package daemon

import "context"

// followCwd is for Windows; elsewhere lookAt reads the folder of the
// foreground shell.
func (d *Daemon) followCwd(context.Context, look) {}
