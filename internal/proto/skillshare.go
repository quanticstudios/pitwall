package proto

import "github.com/quanticstudios/pitwall/internal/skillshare"

// SkillshareLevel added Skillshare, SkillshareResult and
// model.State.Skillshare: the skillshare popup's work runs on the machine
// of the tab it is for.
const SkillshareLevel = 74

// Skillshare asks the daemon to do Call on its machine
// (skillshare.Client.Serve). Pane is the focused pane, by which a window
// of several machines routes it; "" for a machine picked by name. ID is the
// client's and comes back on every SkillshareResult.
type Skillshare struct {
	Pane string
	ID   uint64
	// Cancel stops this connection's call with ID and releases its clone.
	Cancel bool
	Call   skillshare.Call
}

// SkillshareResult answers a Skillshare to its client alone: a Step while
// the call runs, then one with Done and its Reply.
type SkillshareResult struct {
	ID    uint64
	Step  string
	Done  bool
	Reply skillshare.Reply
}
