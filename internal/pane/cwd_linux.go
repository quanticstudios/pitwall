package pane

import (
	"os"
	"strconv"
)

// procCwd is pid's working directory, or "".
func procCwd(pid int) string {
	cwd, _ := os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd")
	return cwd
}
