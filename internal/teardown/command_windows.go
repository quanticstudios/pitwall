package teardown

import (
	"os/exec"
	"strconv"

	"github.com/quanticstudios/pitwall/internal/nowindow"
)

func ownGroup(cmd *exec.Cmd) {
	nowindow.Set(cmd)
	cmd.Cancel = func() error {
		kill := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid))
		nowindow.Set(kill)
		return kill.Run()
	}
}
