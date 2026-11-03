package app

import (
	"maps"
	"slices"
)

// Viewer is optionally implemented by a Backend: Show tells the daemon
// which panes the window draws, so it sends frames of those alone, and
// which of them it draws at the live screen (proto.View).
type Viewer interface {
	Show(panes, live []string)
}

// showPanes tells the backend the panes layoutPanes drew this frame, and
// the picture in picture pane, which drawPiP draws after it at the live
// screen, whatever its tab scrolled back to.
func (u *ui) showPanes(drawn map[string]bool) {
	if v, ok := u.b.(Viewer); ok {
		ids, live := slices.Sorted(maps.Keys(drawn)), []string(nil)
		if p := u.nav.pip; p != "" && !drawn[p] {
			ids, live = append(ids, p), []string{p}
		}
		v.Show(ids, live)
	}
}
