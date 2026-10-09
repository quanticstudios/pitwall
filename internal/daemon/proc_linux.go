package daemon

import (
	"io"
	"os"
	"strconv"
	"strings"

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
