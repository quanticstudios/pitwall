package daemon

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
)

// procSession is the session id of pid, or 0.
func procSession(pid int) int { return statField(pid, 6) }

// procIdentify names the agent in the foreground group pg, and returns the
// comm of its leader. A wrapper runs its agent as a child in the same group:
// node for an npm install of Codex, a launcher script that does not exec. So
// when the leader is no agent, its group is walked, at most groupWalk
// processes and three levels deep. An interpreter (node for an npm install
// of Gemini CLI) is named by its script, from the start of its argv.
func procIdentify(pg int) (model.Provider, string) {
	comm := readComm(pg)
	if p := identifyProc(pg, comm); p != "" {
		return p, comm
	}
	n := 0
	var walk func(pid, depth int) model.Provider
	walk = func(pid, depth int) model.Provider {
		for _, c := range children(pid) {
			if n++; n > groupWalk {
				return ""
			}
			if statField(c, 5) != pg {
				continue // a background job or a daemon the leader started
			}
			if p := identifyProc(c, readComm(c)); p != "" {
				return p
			}
			if depth < 3 {
				if p := walk(c, depth+1); p != "" {
					return p
				}
			}
		}
		return ""
	}
	return walk(pg, 1), comm
}

// procRoot is where procfs is mounted; tests point it at a fake tree.
var procRoot = "/proc"

// identifyProc names the agent process pid runs, by its comm and exe, or,
// for an interpreter, by the script its argv starts with.
func identifyProc(pid int, comm string) model.Provider {
	exe := readExe(pid)
	if p := agent.Identify(comm, exe); p != "" || !agent.Interpreter(comm, exe) {
		return p
	}
	return agent.Script(readArgs(pid))
}

// argsRead bounds the bytes of an argv readArgs reads: a script's path
// comes first, and the rest is never looked at.
const argsRead = 4096

// readArgs is the start of pid's argv, at most argsRead bytes of it, cut
// short at the limit. Callers pass it to agent.Script only, for a process
// of a pane's own foreground group, and never log or keep it: arguments
// can carry secrets.
func readArgs(pid int) []string {
	f, err := os.Open(procRoot + "/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return nil
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, argsRead))
	args := strings.Split(string(b), "\x00")
	if len(args) > 0 && args[len(args)-1] == "" {
		args = args[:len(args)-1]
	}
	return args
}

func readComm(pid int) string {
	b, _ := os.ReadFile(procRoot + "/" + strconv.Itoa(pid) + "/comm")
	return strings.TrimSpace(string(b))
}

// Adapted from tuios (MIT): internal/session/agent_detect_linux.go
func readExe(pid int) string {
	s, _ := os.Readlink(procRoot + "/" + strconv.Itoa(pid) + "/exe")
	return strings.TrimSuffix(s, " (deleted)")
}

// statField is field n of /proc/<pid>/stat as proc(5) numbers them, or 0.
// comm, field 2, may hold spaces and parentheses, so fields are counted from
// after the last ')'.
//
// Adapted from tuios (MIT): internal/session/agent_detect_linux.go
func statField(pid, n int) int {
	b, err := os.ReadFile(procRoot + "/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	s := string(b)
	f := strings.Fields(s[strings.LastIndexByte(s, ')')+1:])
	if n < 3 || len(f) <= n-3 {
		return 0
	}
	v, _ := strconv.Atoi(f[n-3])
	return v
}

// children lists pid's children across its threads: a child is listed under
// the thread that forked it. At most 64 threads are read.
//
// Adapted from tuios (MIT): internal/session/agent_detect_linux.go
func children(pid int) []int {
	dir := procRoot + "/" + strconv.Itoa(pid) + "/task/"
	tids, _ := os.ReadDir(dir)
	var out []int
	for _, t := range tids[:min(len(tids), 64)] {
		b, _ := os.ReadFile(dir + t.Name() + "/children")
		for _, f := range strings.Fields(string(b)) {
			if c, err := strconv.Atoi(f); err == nil {
				out = append(out, c)
			}
		}
	}
	return out
}

// procBusy reports whether pane's agent, leading group pg, has work of its
// own running: a child other than its stdio servers (see stdioOnly), or
// any other process started in the pane (see paneStrays), such as a
// shell's `cmd &` that left the agent's tree. Unreadable counts as busy.
func procBusy(pg int, pane string) bool {
	keep, ok := agentProcs(pg, pane)
	return !ok || paneStrays(pane, keep)
}

// agentProcs is what may run in pane while its agent, leading group pg,
// parks: the pane's processes above the agent, the agent, and its stdio
// servers with everything under them. ok is false when the agent has
// another child or a read fails. When pg is a bare interpreter launching
// the agent (node running Codex's script), its one child is the agent.
func agentProcs(pg int, pane string) (keep map[int]bool, ok bool) {
	comm := readComm(pg)
	if comm == "" {
		return nil, false
	}
	keep = map[int]bool{pg: true}
	for pid, n := pg, 0; n < 8; n++ {
		pp, _ := procParent(pid)
		if pp <= 1 || !carriesPane(pp, pane) {
			break
		}
		keep[pp], pid = true, pp
	}
	lead, kids := pg, children(pg)
	if launches(pg, comm, kids) {
		lead, kids = kids[0], children(kids[0])
		keep[lead] = true
	}
	if !stdioOnly(lead, kids) {
		return nil, false
	}
	budget := 256
	var under func(pid, depth int)
	under = func(pid, depth int) {
		if budget--; budget < 0 || depth > 6 {
			return // ponytail: what is left out still carries the pane, so it counts as busy
		}
		keep[pid] = true
		for _, c := range children(pid) {
			under(c, depth+1)
		}
	}
	for _, k := range kids {
		under(k, 0)
	}
	return keep, true
}

// launches reports whether pg, named comm, is a bare interpreter running
// the agent its script names as its one child, which shares its terminal:
// the launcher a package manager installs. An agent pg names itself, or a
// child on other stdio, is no launcher's.
func launches(pg int, comm string, kids []int) bool {
	exe := readExe(pg)
	if len(kids) != 1 || agent.Identify(comm, exe) != "" || !agent.Interpreter(comm, exe) {
		return false
	}
	prov := identifyProc(kids[0], readComm(kids[0]))
	tty := fdLink(pg, 0)
	return prov != "" && prov == identifyProc(pg, comm) && strings.HasPrefix(tty, "/dev/") && fdLink(kids[0], 0) == tty
}

// paneVar is the variable every process started in a pane inherits.
const paneVar = "PITWALL_PANE="

// paneStrays reports whether a process outside keep carries this pane's
// paneVar. Only that variable is looked for: nothing else of an
// environment is kept or logged, and a process it cannot read, as another
// user's, is skipped.
func paneStrays(pane string, keep map[int]bool) bool {
	ents, err := os.ReadDir(procRoot)
	if err != nil {
		return true
	}
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err == nil && !keep[pid] && carriesPane(pid, pane) {
			return true
		}
	}
	return false
}

// carriesPane reports whether pid's environment sets paneVar to pane.
func carriesPane(pid int, pane string) bool {
	b, err := os.ReadFile(procRoot + "/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return false
	}
	want := []byte(paneVar + pane + "\x00")
	return bytes.HasPrefix(b, want) || bytes.Contains(b, append([]byte{0}, want...))
}

// stdioOnly reports whether every process in kids is a stdio server of
// lead, as an MCP server is: its stdin and stdout each are a pipe lead
// holds or a unix socket whose peer lead holds (Node gives a child a
// socketpair). A background tool writes to a file or /dev/null, and a
// server a hook started to pipes of its own. An fd it cannot read counts
// against.
func stdioOnly(lead int, kids []int) bool {
	if len(kids) == 0 {
		return true
	}
	held := heldEnds(lead)
	if held == nil {
		return false
	}
	nl, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, unix.NETLINK_SOCK_DIAG)
	if err != nil {
		return false
	}
	defer unix.Close(nl)
	unix.SetsockoptTimeval(nl, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 1})
	toLead := func(link string) bool {
		if strings.HasPrefix(link, "pipe:") {
			return held[link]
		}
		ino, ok := strings.CutPrefix(link, "socket:[")
		n, err := strconv.ParseUint(strings.TrimSuffix(ino, "]"), 10, 32)
		return ok && err == nil && held[fmt.Sprintf("socket:[%d]", unixPeer(nl, uint32(n)))]
	}
	for _, c := range kids {
		if !toLead(fdLink(c, 0)) || !toLead(fdLink(c, 1)) {
			return false
		}
	}
	return true
}

// heldEnds is the pipes and sockets pid has open, as their fd links read
// ("pipe:[123]", "socket:[456]"), or nil when its fds cannot be read.
func heldEnds(pid int) map[string]bool {
	dir := procRoot + "/" + strconv.Itoa(pid) + "/fd/"
	fds, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, fd := range fds {
		if l, _ := os.Readlink(dir + fd.Name()); strings.HasPrefix(l, "pipe:") || strings.HasPrefix(l, "socket:") {
			out[l] = true
		}
	}
	return out
}

// unixPeer is the inode of the unix socket connected to the one with inode
// ino, asked of the kernel's sock_diag over netlink socket nl; 0 when
// unknown.
func unixPeer(nl int, ino uint32) uint32 {
	const (
		reqLen        = unix.NLMSG_HDRLEN + 24 // struct unix_diag_req
		udiagShowPeer = 0x4
		unixDiagPeer  = 2
		msgLen        = 16 // struct unix_diag_msg
	)
	req := make([]byte, reqLen)
	ne := binary.NativeEndian
	ne.PutUint32(req[0:], reqLen)
	ne.PutUint16(req[4:], unix.SOCK_DIAG_BY_FAMILY)
	ne.PutUint16(req[6:], unix.NLM_F_REQUEST)
	req[16] = unix.AF_UNIX
	ne.PutUint32(req[20:], ^uint32(0)) // every state
	ne.PutUint32(req[24:], ino)
	ne.PutUint32(req[28:], udiagShowPeer)
	ne.PutUint32(req[32:], ^uint32(0)) // no cookie
	ne.PutUint32(req[36:], ^uint32(0))
	if unix.Sendto(nl, req, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}) != nil {
		return 0
	}
	buf := make([]byte, 4096)
	n, _, err := unix.Recvfrom(nl, buf, 0)
	if err != nil || n < unix.NLMSG_HDRLEN+msgLen || ne.Uint16(buf[4:]) != unix.SOCK_DIAG_BY_FAMILY ||
		ne.Uint32(buf[unix.NLMSG_HDRLEN+4:]) != ino {
		return 0
	}
	end := min(n, int(ne.Uint32(buf[0:])))
	for off := unix.NLMSG_HDRLEN + msgLen; off+8 <= end; {
		l, typ := int(ne.Uint16(buf[off:])), ne.Uint16(buf[off+2:])
		if l < 4 {
			return 0
		}
		if typ == unixDiagPeer {
			return ne.Uint32(buf[off+4:])
		}
		off += (l + 3) &^ 3
	}
	return 0
}

// fdLink is where pid's fd n points, "" when unreadable.
func fdLink(pid, n int) string {
	l, _ := os.Readlink(procRoot + "/" + strconv.Itoa(pid) + "/fd/" + strconv.Itoa(n))
	return l
}

// procParent is pid's parent and process group, or 0s.
func procParent(pid int) (ppid, pgid int) { return statField(pid, 4), statField(pid, 5) }

// peerPID is the pid of the process on the other end of a unix socket, or 0.
func peerPID(nc net.Conn) int {
	pid := 0
	if sc, ok := nc.(syscall.Conn); ok {
		if raw, err := sc.SyscallConn(); err == nil {
			raw.Control(func(fd uintptr) {
				if cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED); err == nil {
					pid = int(cred.Pid)
				}
			})
		}
	}
	return pid
}
