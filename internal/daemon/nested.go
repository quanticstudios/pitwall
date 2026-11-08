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
// came from. Before the first accepted hook, as after any daemon start, that
// group is the current one while the pane has an agent. A hook that cannot
// be placed so (no pid, as on Windows, a process gone, a shared Codex
// app-server) is accepted. A rejected agent is accepted once the pane's
// agent exits, since the foreground group changes and the child is no
// longer below it.
func (d *Daemon) nestedHook(m proto.AgentEvent, pid int) bool {
	if pid <= 0 || m.Provider == "report" || model.ReportedName(m.Provider) != "" {
		return false // a report comes from the agent's own tool runs
	}
	d.mu.Lock()
	fg := d.live.fg[m.Pane]
	f, ok := d.panes[m.Pane].(foregrounder)
	agent := false
	if pi := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == m.Pane }); pi < 0 || model.ReportedName(d.st.Panes[pi].Provider) != "" {
		ok = false // a self-reported agent has its own authority rules
	} else {
		agent = d.st.Panes[pi].Provider != ""
	}
	d.mu.Unlock()
	if !ok {
		return false
	}
	cur := f.Foreground()
	if fg <= 0 && agent {
		fg = cur // no hook since the daemon started: a takeover, restart or resume
	}
	if fg <= 0 || cur != fg {
		return false
	}
	return nestedIn(pid, fg)
}

// nestedIn reports whether the hook process pid was sent by an agent outside
// foreground group fg that descends from a process in fg. The sender is the
// hook's parent, or that parent's parent when the parent is a shell leading
// the hook's group: Claude Code starts every hook and tool command with
// setsid, sometimes through a shell. An agent leading a group, as pi run by
// `exec` in a tool shell, is the sender itself.
func nestedIn(pid, fg int) bool {
	ppid, pg := parentOf(pid)
	if ppid <= 1 || pg == fg {
		return false
	}
	sender := ppid
	if c := commOf(ppid); pg == ppid && c != "" && (c == shellComm || slices.Contains(shells, c)) {
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
