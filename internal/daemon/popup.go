package daemon

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// popupID is the shape of a pane id a caller picks: newID's.
var popupID = regexp.MustCompile(`^[0-9a-f]{16}$`)

// openPopup starts m's command in a pane over its tab's splits. The pane
// is in no layout; exited closes it when the command ends, as any pane
// without Held, and removePane clears the tab's Popup.
func (d *Daemon) openPopup(m proto.OpenPopup) error {
	for _, size := range []string{m.Width, m.Height} {
		if size != "" {
			if _, _, err := model.ParsePopupSize(size); err != nil {
				return err
			}
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	w := d.workspace(m.WorkspaceID)
	switch {
	case w == nil:
		return fmt.Errorf("no workspace %s", m.WorkspaceID)
	case w.Chat != "":
		return errors.New("a chat tab has no panes")
	case len(w.Tabs) == 0:
		return errors.New("this tab has no panes to show a popup over")
	case w.Popup != nil:
		return errors.New("this tab has a popup open already; close it first")
	}
	id := m.Pane
	if id == "" {
		id = newID()
	} else if !popupID.MatchString(id) || d.panes[id] != nil || slices.ContainsFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id }) {
		return fmt.Errorf("pane id %q is not a free id of 16 hex digits", id)
	}
	cwd := cmp.Or(m.Dir, d.st.LivePath(*w))
	if fi, err := os.Stat(cwd); err != nil || !fi.IsDir() || !filepath.IsAbs(cwd) {
		return fmt.Errorf("%s is not a directory", cwd)
	}
	if m.Path != "" {
		if len(m.Cmd) == 0 || m.Cmd[0] == "" {
			return errors.New("a path popup needs a command")
		}
		p := m.Path
		if rest, ok := strings.CutPrefix(p, "~/"); ok {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			p = filepath.Join(home, rest)
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(cwd, p)
		}
		m.Cmd = append(slices.Clone(m.Cmd), p)
	}
	env := d.profileEnv(w)
	if err := d.start(id, m.Cmd, cwd, w.Ports, env); err != nil {
		return err
	}
	d.st.Panes = append(d.st.Panes, model.Pane{ID: id, WorkspaceID: w.ID, Cmd: m.Cmd, Cwd: cwd, Env: env})
	w.Popup = &model.Popup{Pane: id, Width: m.Width, Height: m.Height, Title: m.Title}
	d.changed()
	return nil
}

// withoutPopups is s as the store keeps it: a restart drops popups, whose
// commands ran for one look, so their panes are left out.
// Workspace.Popup itself is never saved.
func withoutPopups(s model.State) model.State {
	if !slices.ContainsFunc(s.Workspaces, func(w model.Workspace) bool { return w.Popup != nil }) {
		return s
	}
	s.Panes = slices.DeleteFunc(slices.Clone(s.Panes), func(p model.Pane) bool { return s.IsPopup(p.ID) })
	return s
}
