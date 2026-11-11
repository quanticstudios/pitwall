package app

import (
	"image"
	"path/filepath"

	gl "gioui.org/layout"
	"gioui.org/op"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/mdview"
)

// openReader opens link, Ctrl+clicked in pane, in the reader when it
// names a markdown file, and reports whether it did; a notice under the
// pane at r says why one did not open. A file on a Host is not on this
// machine, and the daemon has no message to read one.
func (u *ui) openReader(gtx gl.Context, st *model.State, pane, link string, r image.Rectangle) bool {
	cwd := ""
	if pn := findPane(st, pane); pn != nil {
		cwd = pn.Cwd
	}
	path, frag, ok := mdview.LinkPath(link, cwd)
	if !ok {
		return false
	}
	switch {
	case curHost() != "":
		u.showError(gtx, filepath.Base(path)+" is on "+curHost()+"; the reader opens files on this machine only", r)
	case !filepath.IsAbs(path):
		u.showError(gtx, "The pane's folder is unknown", r)
	default:
		if err := u.reader.Open(path, frag); err != nil {
			u.showError(gtx, "Cannot open "+filepath.Base(path)+": "+err.Error(), r)
			return true
		}
		u.readerWS = u.nav.workspace
		u.settings.Hide()
		u.review.hide()
		u.boardOn, u.mon.on = false, false
		gtx.Execute(op.InvalidateCmd{})
	}
	return true
}

// layoutReader draws the reader, or the panes again once another tab is
// shown.
func (u *ui) layoutReader(gtx gl.Context, st *model.State) {
	if u.nav.workspace != u.readerWS {
		u.reader.Close()
		u.layoutPanes(gtx, st)
		return
	}
	u.reader.Layout(gtx, u.th)
	if !u.reader.On() {
		gtx.Execute(op.InvalidateCmd{}) // the panes, next frame
	}
}
