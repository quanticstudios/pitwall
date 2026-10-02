package proto

import "encoding/gob"

// SetProjectAppearance sets a project's lucide icon name and aide color id,
// from the sidebar's project popover.
type SetProjectAppearance struct {
	ProjectID string
	Icon      string // lucide icon name, e.g. "code"
	Color     string // aide color id, e.g. "sky"
}

func init() {
	Messages = append(Messages, SetProjectAppearance{})
	// conn.go's init registers Messages too; registering here as well keeps
	// this independent of file init order. Same type, same name: no panic.
	gob.Register(SetProjectAppearance{})
}
