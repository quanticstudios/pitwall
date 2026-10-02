// Package daemon owns all state: projects, workspaces, panes, agent activity
// and branch stats. Clients connect over proto and get StateMsg and Frame
// pushes.
package daemon

import (
	"context"
	"net"
)

type Daemon struct{}

// New loads state from store.Path() and relaunches saved panes with
// store.RestoreCmd.
func New() (*Daemon, error) { panic("unimplemented") }

// Serve accepts clients until ctx is done, then saves state and closes panes.
func (d *Daemon) Serve(ctx context.Context, ln net.Listener) error { panic("unimplemented") }
