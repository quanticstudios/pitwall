package proto

import "github.com/quanticstudios/pitwall/internal/vt"

// Commands requests a snapshot of a pane's retained OSC 133 command blocks.
type Commands struct {
	Pane string
	ID   uint64
}

// CommandResult answers only the requesting client. ID rejects stale replies.
// More means older blocks exceed the reply size limit.
type CommandResult struct {
	Pane             string
	ID               uint64
	Blocks           []vt.CommandBlock
	Integrated, More bool
}
