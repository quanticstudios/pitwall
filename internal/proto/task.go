package proto

import (
	"encoding/gob"

	"github.com/quanticstudios/pitwall/internal/model"
)

// NewTask starts Task in a tab of its own, or with Queue adds it last to
// its session's queue (model.State.Tasks). The daemon gives it its ID,
// its SessionID when "" (FromPane's session, else the most recently used
// one) and its GroupID when "" (the group whose folder holds Dir).
type NewTask struct {
	Task     model.Task
	Queue    bool
	FromPane string
}

// DropTask takes task ID out of its queue, and with Start starts it now.
type DropTask struct {
	ID    string
	Start bool
}

func init() {
	Messages = append(Messages, NewTask{}, DropTask{})
	gob.Register(NewTask{}) // as in appearance.go
	gob.Register(DropTask{})
}
