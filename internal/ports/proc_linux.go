package ports

import (
	"os"
	"strconv"
	"strings"
)

// procRoot is where procfs is mounted; tests point it at a fake tree.
var procRoot = "/proc"

// fdWalkLimit bounds the fd links one Listening call reads.
const fdWalkLimit = 50000

// Procs reads every process's stat: parent, group, session and comm.
func Procs() (Table, error) {
	ents, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, err
	}
	t := Table{}
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		b, err := os.ReadFile(procRoot + "/" + e.Name() + "/stat")
		if err != nil {
			continue // it exited
		}
		if p, ok := parseStat(pid, string(b)); ok {
			t[pid] = p
		}
	}
	return t, nil
}

// parseStat reads /proc/<pid>/stat. comm, field 2, may hold spaces and
// parentheses, so the fields after it count from the last ')'. A zombie
// (state Z) is left out: it holds no socket and cannot be stopped.
func parseStat(pid int, s string) (Proc, bool) {
	open, end := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || end < open {
		return Proc{}, false
	}
	f := strings.Fields(s[end+1:]) // state ppid pgrp session ...
	if len(f) < 4 || f[0] == "Z" || f[0] == "X" {
		return Proc{}, false
	}
	ppid, _ := strconv.Atoi(f[1])
	pgid, _ := strconv.Atoi(f[2])
	sid, _ := strconv.Atoi(f[3])
	return Proc{PID: pid, PPID: ppid, PGID: pgid, SID: sid, Name: s[open+1 : end]}, true
}

// Listening is the wildcard and loopback TCP ports each of pids listens
// on, ascending, by pid; a pid with none is missing. It matches the
// sockets in /proc/net/tcp and tcp6 to the pids' fd links, at most
// fdWalkLimit of them; a process of another user, whose fds it cannot
// read, is skipped.
func Listening(pids []int) map[int][]int {
	inodes := map[string]int{}
	for _, f := range []string{"/net/tcp", "/net/tcp6"} {
		if b, err := os.ReadFile(procRoot + f); err == nil {
			ParseNetTCP(string(b), inodes)
		}
	}
	if len(inodes) == 0 {
		return nil
	}
	out := map[int][]int{}
	budget := fdWalkLimit
	for _, pid := range pids {
		dir := procRoot + "/" + strconv.Itoa(pid) + "/fd/"
		fds, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			if budget--; budget < 0 {
				return sorted(out)
			}
			l, _ := os.Readlink(dir + fd.Name())
			ino, ok := strings.CutPrefix(l, "socket:[")
			if !ok {
				continue
			}
			if port := inodes[strings.TrimSuffix(ino, "]")]; port > 0 {
				out[pid] = addPort(out[pid], port)
			}
		}
	}
	return sorted(out)
}

// Cwd is pid's working folder, "" when it cannot be read.
func Cwd(pid int) string {
	s, _ := os.Readlink(procRoot + "/" + strconv.Itoa(pid) + "/cwd")
	return strings.TrimSuffix(s, " (deleted)")
}

// Running reports whether pid runs: its stat reads, and it is no zombie
// waiting for its parent to reap it.
func Running(pid int) bool {
	b, err := os.ReadFile(procRoot + "/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	_, ok := parseStat(pid, string(b))
	return ok
}
