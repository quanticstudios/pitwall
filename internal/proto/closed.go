package proto

import "encoding/gob"

// ReopenTab opens closed tab ID (model.State.Closed) again: in its session,
// group and folder, its agent resumed, under the record's ID. A session that
// ended since comes back by name, and a group by name too. AtRoot opens it in
// model.ClosedTab.Root instead, for a tab whose folder is gone. The record
// goes once the tab is open.
type ReopenTab struct {
	ID     string
	AtRoot bool
}

// ForgetTab drops closed tab ID from model.State.Closed.
type ForgetTab struct {
	ID string
}

func init() {
	Messages = append(Messages, ReopenTab{}, ForgetTab{})
	gob.Register(ReopenTab{})
	gob.Register(ForgetTab{})
}
