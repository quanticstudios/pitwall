// Package app is the window: sidebar on the left, the active workspace's
// split panes on the right, Alt navigation and the Alt-hold switcher.
package app

import (
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// Backend is what the window needs from the daemon. The real one wraps a
// proto.Conn; tests and demos use a fake.
type Backend interface {
	State() model.State
	Frame(pane string) (vt.Grid, vt.Modes, bool)
	Send(msg any) error // a proto message
	Changed() <-chan struct{}
}

// Run opens the window and blocks until it closes.
func Run(b Backend) error { panic("unimplemented") }
