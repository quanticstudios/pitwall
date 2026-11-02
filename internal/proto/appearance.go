package proto

import (
	"encoding/gob"
	"time"
)

// SetProjectAppearance sets a project's lucide icon name and aide color id,
// from the sidebar's project popover.
type SetProjectAppearance struct {
	ProjectID string
	Icon      string // lucide icon name, e.g. "code"
	Color     string // aide color id, e.g. "sky"
}

// SetTabOptions pins or unpins tab WorkspaceID and sets its color id, ""
// for none, from the sidebar's tab menu.
type SetTabOptions struct {
	WorkspaceID string
	Pinned      bool
	Color       string
}

// SetTabInbox settles or unsettles tab WorkspaceID and snoozes it until
// SnoozedUntil, or wakes it with a zero SnoozedUntil, from the sidebar's
// inbox. Unsettling restarts the tab's idle time for settle_after_days.
type SetTabInbox struct {
	WorkspaceID  string
	Settled      bool
	SnoozedUntil time.Time
}

func init() {
	Messages = append(Messages, SetProjectAppearance{}, SetTabOptions{}, SetTabInbox{})
	// conn.go's init registers Messages too; registering here as well keeps
	// this independent of file init order. Same type, same name: no panic.
	gob.Register(SetProjectAppearance{})
	gob.Register(SetTabOptions{})
	gob.Register(SetTabInbox{})
}
