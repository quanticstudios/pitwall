package teardown

import (
	"os/exec"
	"strconv"

	"github.com/quanticstudios/pitwall/internal/nowindow"
)

// treeKill makes cancelling cmd end its whole process tree: Windows has no
// process groups, so childproc.Bound alone leaves a teardown's children running.
func treeKill(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		kill := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid))
		nowindow.Set(kill)
		return kill.Run()
	}
}
