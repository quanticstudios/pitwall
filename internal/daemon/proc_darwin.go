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
// are checked, at most groupWalk of them; macOS lists a group directly.
func procIdentify(pg int) (model.Provider, string) {
	comm := readComm(pg)
	if p := agent.Identify(comm, readExe(pg)); p != "" {
		return p, comm
	}
	procs, _ := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pg)
	for _, kp := range procs[:min(len(procs), groupWalk)] {
		pid := int(kp.Proc.P_pid)
		if pid == pg {
			continue
		}
		if p := agent.Identify(unix.ByteSliceToString(kp.Proc.P_comm[:]), readExe(pid)); p != "" {
			return p, comm
		}
	}
	return "", comm
}

func readComm(pid int) string {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return ""
	}
	return unix.ByteSliceToString(kp.Proc.P_comm[:])
}
