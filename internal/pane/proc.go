package pane

// Foreground is the PTY's foreground process group, or 0 when it cannot be
// read (the pane exited).
func (p *Pane) Foreground() int { return p.foreground() }
