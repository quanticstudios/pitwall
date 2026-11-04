package app

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// openPath opens path from pane's folder, in a popup when configured.
func (u *ui) openPath(st *model.State, pane, path string) error {
	pn := findPane(st, pane)
	if pn == nil || pn.Cwd == "" {
		return errors.New("the pane's folder is unknown")
	}
	if len(u.cfg.OpenPath) == 0 {
		if curHost() != "" || !filepath.IsAbs(path) {
			return errors.New("the path is not on this machine")
		}
		if _, err := os.Stat(path); err != nil {
			return err
		}
		return hintOpen(fileURL(path))
	}
	if u.link().Level < proto.PopupPathLevel {
		return errors.New("path popups need daemon Level 59 or newer")
	}
	if !u.send(proto.OpenPopup{WorkspaceID: pn.WorkspaceID, Cmd: u.cfg.OpenPath, Dir: pn.Cwd, Path: path}) {
		return errors.New("could not send the path popup")
	}
	return nil
}
