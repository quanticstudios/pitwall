//go:build unix

package app

import (
	"os/exec"
	"runtime"
)

// openCommand opens url in the user's default handler.
func openCommand(url string) *exec.Cmd {
	return openCommandForOS(runtime.GOOS, url)
}

func openCommandForOS(goos, url string) *exec.Cmd {
	if goos == "darwin" {
		return exec.Command("open", "--", url)
	}
	// why: xdg-open 1.2.1 rejects "--" as "unexpected option '--'" in its
	// argument loop; validateOpenURL ensures url starts with an allowed scheme.
	return exec.Command("xdg-open", url)
}
