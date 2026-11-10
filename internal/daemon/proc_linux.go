package daemon

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
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

// procBusy reports whether the agent leading group pg runs a process other
// than its stdio servers (see stdioOnly), such as a tool's shell, a dev
// server or a watcher. When pg is an interpreter wrapping the agent (node
// running Codex's script), that agent's processes are looked at, and any
// other process of the wrapper counts as busy; an agent deeper in the
// tree is just another process. Unreadable counts as busy.
func procBusy(pg int) bool {
	comm := readComm(pg)
	if comm == "" {
		return true
	}
	lead, kids := pg, children(pg)
	if agent.Interpreter(comm, readExe(pg)) {
		if i := slices.IndexFunc(kids, func(c int) bool { return identifyProc(c, readComm(c)) != "" }); i >= 0 {
			if len(kids) > 1 {
				return true
			}
			lead, kids = kids[i], children(kids[i])
		}
	}
	return !stdioOnly(lead, kids)
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
