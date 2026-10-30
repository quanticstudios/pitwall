package pane

import "testing"

func adoptedExitCode(t *testing.T, pid, code int) func() int {
	return func() int { return code }
}
