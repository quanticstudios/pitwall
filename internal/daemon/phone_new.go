package daemon

import (
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"slices"
	"time"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/store"
	"github.com/quanticstudios/pitwall/internal/web"
)

// phoneNewOptions is what the phone's New form offers in session: the
// agents on this daemon's PATH and its presets, and the session's folders.
func (d *Daemon) phoneNewOptions(session string) remote.NewOptions {
	home, _ := os.UserHomeDir()
	d.mu.Lock()
	folders := remote.Folders(d.st, session, home)
	d.mu.Unlock()
	look := func(cmd string) error { _, err := exec.LookPath(cmd); return err }
	return remote.NewOptions{Agents: remote.Agents(ctlHub{d}.Settings().Presets, look), Folders: folders}
}

// phoneStart opens the tab t describes through the control commands
// newTab and worktreeNew, so the phone's tab gets their checks and guard.
func (d *Daemon) phoneStart(t remote.NewTab) (tab, pane string, err error) {
	ctx := context.Background()
	if !t.Worktree {
		return web.StartTab(ctx, ctlHub{d}, t.Dir, t.Session, t.Preset, t.Cmd)
	}
	tab, pane, note, err := web.StartWorktree(ctx, ctlHub{d}, t.Dir, t.Branch, t.Session, t.Preset, t.Cmd)
	if note != "" {
		log.Printf("remote: worktree tab %s: %s", tab, note)
	}
	if err == nil && pane == "" {
		pane, err = d.phoneShell(tab)
	}
	return tab, pane, err
}

// phoneShell opens a shell in worktree tab, which a worktree without a
// command or setup opens with no pane: a window adds one when it shows the
// tab, and the phone lists and lands in a tab's pane.
func (d *Daemon) phoneShell(tab string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	w := d.workspace(tab)
	if w == nil {
		return "", errors.New("the worktree's tab has closed")
	}
	if len(w.Tabs) > 0 { // a window showed it first
		if ids := layout.Panes(w.Tabs[0].Layout); len(ids) > 0 {
			return ids[0], nil
		}
		return "", nil
	}
	if err := d.addTab(w, w.Path, nil, nil); err != nil {
		return "", err
	}
	d.changed()
	return d.st.Panes[len(d.st.Panes)-1].ID, nil
}

// phoneCloseTab closes a tab the phone lists, as the window's close does.
func (d *Daemon) phoneCloseTab(tab string) error {
	d.mu.Lock()
	w := d.st.Workspace(tab)
	ok := w != nil && !w.Detached && !w.Monitor
	d.mu.Unlock()
	if !ok {
		return errors.New("that tab has closed")
	}
	return d.handle(context.Background(), proto.CloseTab{WorkspaceID: tab})
}

// phoneRestart starts exited pane's command again in its folder, with the
// command a daemon restart brings it back with: its agent's session
// resumed when one is known, else the command it started with. The pane
// keeps its screen above the new run.
func (d *Daemon) phoneRestart(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id })
	switch {
	case i < 0:
		return errors.New("that pane has closed")
	case !d.st.Panes[i].Exited:
		return errors.New("that pane is still running")
	case d.closing || d.frozen:
		return errors.New("the daemon is stopping")
	}
	sp, old := &d.st.Panes[i], d.panes[id]
	resumes := store.Resumes(*sp)
	if err := d.startVT(id, d.o.RestoreCmd(*sp), sp.Cwd, d.portsOf(sp.WorkspaceID), sp.Env, d.carryVT(*sp, old)); err != nil {
		return err // the pane stays as it was, exited
	}
	if old != nil {
		closeAll([]Pane{old})
	}
	sp.Exited, sp.ExitCode, sp.ExitUnknown = false, 0, false
	if resumes {
		d.resumed[id] = time.Now()
	}
	log.Printf("pane %s: restarted from the phone, resume %t", id, resumes)
	d.changed()
	return nil
}
