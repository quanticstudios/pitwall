// Package store saves daemon state to disk and plans how to bring panes back
// after a restart.
package store

import "github.com/quanticstudios/pitwall/internal/model"

// Path is $XDG_STATE_HOME/pitwall/state.json.
func Path() string { panic("unimplemented") }

// Save writes atomically (temp file + rename).
func Save(path string, s model.State) error { panic("unimplemented") }

// Load returns an empty state and nil when the file does not exist.
func Load(path string) (model.State, error) { panic("unimplemented") }

// RestoreCmd is the argv that brings a pane back: a resumed agent session
// when one is known, otherwise the original command.
func RestoreCmd(p model.Pane) []string { panic("unimplemented") }
