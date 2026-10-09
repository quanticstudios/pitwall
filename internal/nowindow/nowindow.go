//go:build !windows

// Package nowindow keeps the console programs pitwall runs in the
// background, such as git and gh, from opening a window on Windows.
package nowindow

import "os/exec"

// Set is for Windows; elsewhere a program never opens a window.
func Set(*exec.Cmd) {}
