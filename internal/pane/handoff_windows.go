package pane

import (
	"errors"
	"os"
)

// Prepare is unsupported on Windows, as Adopt is. It consumes master.
func Prepare(c Config, master *os.File, pid int) (*Pane, error) {
	return Adopt(c, master, pid)
}

// The handoff methods exist so the daemon builds; Prepare never returns a
// pane and Pause leaves the pane running.
func (p *Pane) Release() {}
func (p *Pane) Pause()   {}
func (p *Pane) Resume()  {}
func (p *Pane) Master() (*os.File, int, error) {
	return nil, 0, errors.New("pane handoff is unsupported on Windows")
}
