package daemon

import "github.com/quanticstudios/pitwall/internal/model"

// clearReported releases a self-reported agent, including its resume command.
// Callers hold d.mu.
func (d *Daemon) clearReported(p *model.Pane) {
	p.Provider, p.SessionID, p.AgentMode, p.Prompt, p.Transcript = "", "", "", "", ""
	p.ResumeCmd = nil
	delete(d.live.fg, p.ID)
	delete(d.live.hookAt, p.ID)
	d.dropActivity(p.ID)
	d.changed()
}
