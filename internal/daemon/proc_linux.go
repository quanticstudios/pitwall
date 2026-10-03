package daemon

import (
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
// processes and three levels deep.
func procIdentify(pg int) (model.Provider, string) {
	comm := readComm(pg)
	if p := agent.Identify(comm, readExe(pg)); p != "" {
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
			if p := agent.Identify(readComm(c), readExe(c)); p != "" {
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

func readComm(pid int) string {
	b, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
	return strings.TrimSpace(string(b))
}

// Adapted from tuios (MIT): internal/session/agent_detect_linux.go
func readExe(pid int) string {
	s, _ := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	return strings.TrimSuffix(s, " (deleted)")
}

// statField is field n of /proc/<pid>/stat as proc(5) numbers them, or 0.
// comm, field 2, may hold spaces and parentheses, so fields are counted from
// after the last ')'.
//
// Adapted from tuios (MIT): internal/session/agent_detect_linux.go
func statField(pid, n int) int {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
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
	dir := "/proc/" + strconv.Itoa(pid) + "/task/"
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
