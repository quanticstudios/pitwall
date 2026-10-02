// Package sidebar draws the project and workspace tree with agent status,
// the way aide's SidebarTree does.
package sidebar

import (
	"gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Event is one of SelectWorkspace, NewWorkspace, AddProject, RenameWorkspace,
// ArchiveWorkspace, DeleteWorkspace.
type Event any

type SelectWorkspace struct{ WorkspaceID, PaneID string }
type NewWorkspace struct{ ProjectID string }
type AddProject struct{}
type RenameWorkspace struct{ WorkspaceID, Name string }
type ArchiveWorkspace struct{ WorkspaceID string }
type DeleteWorkspace struct{ WorkspaceID string }

type Sidebar struct{}

// Layout draws st and returns events from this frame's input.
func (s *Sidebar) Layout(gtx layout.Context, th *theme.Theme, st *model.State, activeWorkspace string) (layout.Dimensions, []Event) {
	panic("unimplemented")
}
