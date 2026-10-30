package daemon

import (
	"strings"

	"github.com/quanticstudios/pitwall/internal/model"
)

// profileEnv uses the tab's saved directories for a new split or replacement
// agent. The caller holds d.mu. Other preset variables belong to that pane.
func (d *Daemon) profileEnv(w *model.Workspace) []string {
	if w == nil || w.Profile == "" {
		return nil
	}
	for _, p := range d.st.Panes {
		if p.WorkspaceID != w.ID {
			continue
		}
		var out []string
		for _, e := range p.Env {
			k, _, _ := strings.Cut(e, "=")
			if k == "CLAUDE_CONFIG_DIR" || k == "CODEX_HOME" || k == "PITWALL_PROFILE" {
				out = append(out, e)
			}
		}
		return out
	}
	return nil
}
