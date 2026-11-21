// Package ports finds the processes a pane or a chat started and the TCP
// ports they listen on, for a chat's Servers panel. It reads each
// process's parent, group, session and name, and its sockets; never its
// arguments or its environment, which can carry secrets.
package ports

import (
	"slices"
	"strconv"
	"strings"
)

// Proc is one process: its parent, process group, session and name (the
// kernel's comm, at most 15 bytes on Linux).
type Proc struct {
	PID, PPID, PGID, SID int
	Name                 string
}

// Table is every process this user can see, by pid.
type Table map[int]Proc

// Children is t's processes by parent.
func (t Table) Children() map[int][]int {
	out := map[int][]int{}
	for pid, p := range t {
		out[p.PPID] = append(out[p.PPID], pid)
	}
	for _, kids := range out {
		slices.Sort(kids)
	}
	return out
}

// walkLimit bounds how many processes Under collects from one root.
const walkLimit = 4096

// Under is root and every process below it in t, at most walkLimit of
// them. A pid reused into a loop is visited once.
func Under(kids map[int][]int, root int) []int { return under(kids, root, nil) }

// localV4 reports whether hex, /proc/net/tcp's little-endian IPv4 address,
// is the wildcard or 127.0.0.0/8: where a dev server on this machine
// listens. One on a single LAN address is left out, as T3 Code does.
func localV4(hex string) bool {
	if len(hex) != 8 {
		return false
	}
	return hex == "00000000" || strings.HasSuffix(strings.ToUpper(hex), "7F")
}

// localAddr reports whether hex, a /proc/net/tcp or tcp6 local address, is
// the wildcard or loopback, an IPv4-mapped one included.
func localAddr(hex string) bool {
	hex = strings.ToUpper(hex)
	switch len(hex) {
	case 8:
		return localV4(hex)
	case 32:
		if hex == strings.Repeat("0", 32) || hex == strings.Repeat("0", 24)+"01000000" {
			return true
		}
		return strings.HasPrefix(hex, strings.Repeat("0", 16)+"FFFF0000") && localV4(hex[24:])
	}
	return false
}

// tcpListen is the TCP state LISTEN in /proc/net/tcp.
const tcpListen = "0A"

// ParseNetTCP reads /proc/net/tcp or tcp6 text: the socket inode of each
// listener on the wildcard or loopback, with its port.
func ParseNetTCP(text string, into map[string]int) {
	lines := strings.Split(text, "\n")
	for _, line := range lines[min(1, len(lines)):] {
		f := strings.Fields(line)
		if len(f) < 10 || f[3] != tcpListen {
			continue
		}
		addr, portHex, ok := strings.Cut(f[1], ":")
		if !ok || !localAddr(addr) {
			continue
		}
		port, err := strconv.ParseUint(portHex, 16, 16)
		if err != nil || port == 0 || f[9] == "0" {
			continue
		}
		into[f[9]] = int(port)
	}
}

// ParseLsof reads `lsof -F pn` output: each process's listening ports on
// the wildcard or loopback, ascending, by pid.
func ParseLsof(text string) map[int][]int {
	out := map[int][]int{}
	pid := 0
	for line := range strings.SplitSeq(text, "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(line[1:])
		case 'n':
			if port := lsofPort(line[1:]); port > 0 && pid > 0 && !slices.Contains(out[pid], port) {
				out[pid] = append(out[pid], port)
			}
		}
	}
	for _, ps := range out {
		slices.Sort(ps)
	}
	return out
}

// addPort is ps with port, once.
func addPort(ps []int, port int) []int {
	if slices.Contains(ps, port) {
		return ps
	}
	return append(ps, port)
}

// sorted sorts each pid's ports in m and returns m.
func sorted(m map[int][]int) map[int][]int {
	for _, ps := range m {
		slices.Sort(ps)
	}
	return m
}

// lsofPort is the port of an lsof name such as "*:3000", "127.0.0.1:5173"
// or "[::1]:8080", 0 for one on another address.
func lsofPort(name string) int {
	name, _, _ = strings.Cut(name, " ")
	i := strings.LastIndexByte(name, ':')
	if i < 0 {
		return 0
	}
	host := strings.Trim(name[:i], "[]")
	switch {
	case host == "*", host == "localhost", host == "::1", host == "::", host == "0.0.0.0", strings.HasPrefix(host, "127."):
	default:
		return 0
	}
	port, err := strconv.Atoi(name[i+1:])
	if err != nil || port <= 0 || port > 65535 {
		return 0
	}
	return port
}
