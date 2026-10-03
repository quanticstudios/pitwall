//go:build unix

package app

import (
	"os/exec"
	"runtime"
)

// openCommand opens url in the user's default handler.
func openCommand(url string) *exec.Cmd {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", url)
	}
	return exec.Command("xdg-open", url)
}
