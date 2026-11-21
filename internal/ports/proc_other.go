//go:build !linux && !darwin

package ports

import "errors"

// Procs is unsupported: Windows has no process groups or sessions to tell
// a pane's processes by.
func Procs() (Table, error) { return nil, errors.ErrUnsupported }

func Listening([]int) map[int][]int { return nil }

func Cwd(int) string { return "" }

func Running(int) bool { return false }
