package daemon

import (
	"slices"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// parentOf is the parent and process group of a process, 0s when it cannot
// be read; tests swap it for a fake process tree.
var parentOf = procParent

// nestedHook reports whether a hook comes from an agent that the pane's
// agent started, such as `claude -p` run from its Bash tool, given the pid of
// the hook process. Such a hook is dropped, so the child never takes over
// the pane's status or resume session. The test is the process tree alone:
// the agent that sent the hook is outside the terminal's foreground group
// but descends from it, and that group is the one the pane's accepted hooks
// came from. A hook that cannot be placed so (no pid, as on Windows, a
// process gone, a shared Codex app-server) is accepted. A rejected agent is
// accepted once the pane's agent exits, since the foreground group changes
// and the child is no longer below it.
func (d *Daemon) nestedHook(m proto.AgentEvent, pid int) bool {
	if pid <= 0 || m.Provider == "report" || model.ReportedName(m.Provider) != "" {
		return false // a report comes from the agent's own tool runs
	}
	d.mu.Lock()
	fg := d.live.fg[m.Pane]
	f, ok := d.panes[m.Pane].(foregrounder)
	if pi := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == m.Pane }); pi < 0 || model.ReportedName(d.st.Panes[pi].Provider) != "" {
		ok = false // a self-reported agent has its own authority rules
	}
	d.mu.Unlock()
	if !ok || fg <= 0 || f.Foreground() != fg {
		return false
	}
	return nestedIn(pid, fg)
}

// nestedIn reports whether the hook process pid was sent by an agent outside
// foreground group fg that descends from a process in fg. An agent may start
// its hook in a group of its own, led by the hook or by a shell between the
// two (Claude Code starts every hook and tool command with setsid): the
// sender is then the leader's parent. Otherwise the sender is the hook's
// parent.
func nestedIn(pid, fg int) bool {
	ppid, pg := parentOf(pid)
	if ppid <= 1 || pg == fg {
		return false
	}
	sender := ppid
	if pg == ppid {
		sender, _ = parentOf(ppid)
	}
	up, pg := parentOf(sender)
	if pg <= 0 || pg == fg {
		return false
	}
	for range 64 { // a bound against a pid reused into a loop
		if up <= 1 {
			return false
		}
		next, pg := parentOf(up)
		if pg == fg {
			return true
		}
		if pg <= 0 {
			return false
		}
		up = next
	}
	return false
}
