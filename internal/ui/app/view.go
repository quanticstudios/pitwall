package app

import (
	"maps"
	"slices"
)

// Viewer is optionally implemented by a Backend: Show tells the daemon
// which panes the window draws, so it sends frames of those alone
// (proto.View).
type Viewer interface {
	Show(panes []string)
}

// showPanes tells the backend the panes layoutPanes drew this frame, and
// the picture in picture pane, which drawPiP draws after it.
func (u *ui) showPanes(drawn map[string]bool) {
	if v, ok := u.b.(Viewer); ok {
		ids := slices.Sorted(maps.Keys(drawn))
		if p := u.nav.pip; p != "" && !drawn[p] {
			ids = append(ids, p)
		}
		v.Show(ids)
	}
}
