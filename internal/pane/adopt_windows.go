package pane

import (
	"errors"
	"os"
)

// Adopt is unsupported on Windows; daemon updates use the restart path.
// As on Unix, it consumes master even on failure.
func Adopt(c Config, master *os.File, pid int) (*Pane, error) {
	if master != nil {
		master.Close()
	}
	return nil, errors.New("pane handoff is unsupported on Windows")
}
