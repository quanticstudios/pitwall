package ports

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Procs lists every process with sysctl kern.proc.all; the session comes
// from getsid. A zombie is left out.
func Procs() (Table, error) {
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	t := Table{}
	for _, kp := range kps {
		pid := int(kp.Proc.P_pid)
		if pid <= 0 || kp.Proc.P_stat == 5 { // SZOMB
			continue
		}
		sid, _ := unix.Getsid(pid)
		t[pid] = Proc{PID: pid, PPID: int(kp.Eproc.Ppid), PGID: int(kp.Eproc.Pgid), SID: sid, Name: unix.ByteSliceToString(kp.Proc.P_comm[:])}
	}
	return t, nil
}

// lsofWait bounds one lsof run.
const lsofWait = 5 * time.Second

// Listening asks lsof for the TCP listeners of pids: `-F pn` prints each
// one's pid and address only, never its arguments.
func Listening(pids []int) map[int][]int {
	if len(pids) == 0 {
		return nil
	}
	ids := make([]string, len(pids))
	for i, p := range pids {
		ids[i] = strconv.Itoa(p)
	}
	ctx, cancel := context.WithTimeout(context.Background(), lsofWait)
	defer cancel()
	// lsof exits 1 when no process matched, with nothing on stdout.
	b, _ := exec.CommandContext(ctx, "lsof", "-nP", "-a", "-iTCP", "-sTCP:LISTEN", "-p", strings.Join(ids, ","), "-F", "pn").Output()
	return ParseLsof(string(b))
}

// Cwd is unknown here: macOS has no procfs, and an lsof per process would
// cost more than the guess it feeds is worth.
func Cwd(int) string { return "" }

// Running reports whether pid runs and is no zombie.
func Running(pid int) bool {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	return err == nil && kp.Proc.P_pid == int32(pid) && kp.Proc.P_stat != 5 // SZOMB
}
