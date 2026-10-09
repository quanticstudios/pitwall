package daemon

import (
	"golang.org/x/sys/unix"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
)

// procSession is the session id of pid, or 0.
func procSession(pid int) int {
	sid, _ := unix.Getsid(pid)
	return sid
}

// procIdentify names the agent in the foreground group pg, and returns the
// comm of its leader. When the leader is no agent, the group's other members
// are checked, at most groupWalk of them; macOS lists a group directly. An
// interpreter is named by its script, from the start of its argv.
func procIdentify(pg int) (model.Provider, string) {
	comm := readComm(pg)
	if p := identifyProc(pg, comm); p != "" {
		return p, comm
	}
	procs, _ := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pg)
	for _, kp := range procs[:min(len(procs), groupWalk)] {
		pid := int(kp.Proc.P_pid)
		if pid == pg {
			continue
		}
		if p := identifyProc(pid, unix.ByteSliceToString(kp.Proc.P_comm[:])); p != "" {
			return p, comm
		}
	}
	return "", comm
}

// identifyProc names the agent process pid runs, by its comm and exe, or,
// for an interpreter, by the script its argv starts with.
func identifyProc(pid int, comm string) model.Provider {
	exe := readExe(pid)
	if p := agent.Identify(comm, exe); p != "" || !agent.Interpreter(comm, exe) {
		return p
	}
	// The buffer holds the environment too; only the first arguments are
	// taken from it, for agent.Script alone, and it is never logged or kept.
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return ""
	}
	return agent.Script(procArgs2(b, 16))
}

func readComm(pid int) string {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return ""
	}
	return unix.ByteSliceToString(kp.Proc.P_comm[:])
}
