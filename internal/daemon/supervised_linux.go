package daemon

import (
	"os"
	"strconv"
	"strings"
)

// supervised names the systemd unit whose service this daemon is, and
// whether a user manager runs it; "" when none does. A var for tests.
var supervised = func() (unit string, user bool) {
	cgroup, _ := os.ReadFile("/proc/self/cgroup")
	comm, _ := os.ReadFile("/proc/" + strconv.Itoa(os.Getppid()) + "/comm")
	return supervisedUnit(string(cgroup), strings.TrimSpace(string(comm)))
}

// supervisedUnit is the unit in cgroup, the text of /proc/self/cgroup,
// when the unified hierarchy's path ends in a .service and parent, the
// parent process's comm, is a systemd manager. Once that daemon exits,
// systemd stops the unit and kills every process left in its cgroup, which
// after a handoff is every pane. A daemon in a session or app scope is not
// a unit's service.
func supervisedUnit(cgroup, parent string) (unit string, user bool) {
	if parent != "systemd" {
		return "", false
	}
	for line := range strings.Lines(cgroup) {
		path, ok := strings.CutPrefix(strings.TrimSpace(line), "0::")
		if !ok {
			continue
		}
		unit = path[strings.LastIndexByte(path, '/')+1:]
		if !strings.HasSuffix(unit, ".service") {
			return "", false
		}
		return unit, strings.Contains(path, "/user@")
	}
	return "", false
}
