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

// showPanes tells the backend the panes layoutPanes drew this frame.
func (u *ui) showPanes(drawn map[string]bool) {
	if v, ok := u.b.(Viewer); ok {
		v.Show(slices.Sorted(maps.Keys(drawn)))
	}
}
