package proto

import (
	"time"

	"github.com/quanticstudios/pitwall/internal/vt"
)

// SearchAll replaces this connection's running search. An empty Query cancels
// it. ID is chosen by the client and echoed so late replies can be discarded.
type SearchAll struct {
	ID    uint64
	Query string
}

// SessionMatch is a matching excerpt, never a whole transcript. Match uses
// terminal line coordinates for scrollback and a JSONL record number for a
// transcript. Time is the transcript timestamp, or the scrollback capture time
// since terminal output has no timestamps.
type SessionMatch struct {
	Workspace, Pane string
	Match           vt.Match
	Snippet         string
	Time            time.Time
	Transcript      bool
}

// SearchAllResult streams bounded batches to the requesting connection only.
// Done terminates the stream; More reports that the result limit was reached.
type SearchAllResult struct {
	ID      uint64
	Matches []SessionMatch
	Done    bool
	More    bool
}
