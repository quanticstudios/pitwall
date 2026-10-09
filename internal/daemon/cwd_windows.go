package daemon

import (
	"context"
	"slices"

	"github.com/quanticstudios/pitwall/internal/model"
)

// followCwd moves a shell pane's folder to the one its prompt last
// reported (pane.Pane.Cwd), as lookAt does where the shell is the
// foreground: Windows has no foreground group to poll, so the shell's
// OSC 7 is all there is.
func (d *Daemon) followCwd(ctx context.Context, l look) {
	if l.shell == "" {
		return
	}
	cwd := l.p.Cwd()
	if cwd == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closing || d.panes[l.id] != l.p {
		return
	}
	if i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == l.id }); i >= 0 && d.st.Panes[i].Cwd != cwd {
		d.st.Panes[i].Cwd = cwd
		d.changed()
		go d.refreshStats(ctx, d.st.Panes[i].WorkspaceID) // a cd can change the repo
	}
}
