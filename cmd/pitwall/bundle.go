package main

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// bundleEnv gives a window started from pitwall.app what a terminal gives
// one: launchd starts an app in / with PATH=/usr/bin:/bin:/usr/sbin:/sbin,
// where claude and codex are not found. A pitwall run any other way keeps
// its environment.
func bundleEnv() {
	exe, err := os.Executable()
	if runtime.GOOS != "darwin" || err != nil || !strings.Contains(exe, ".app/Contents/MacOS/") {
		return
	}
	if home, err := os.UserHomeDir(); err == nil {
		os.Chdir(home)
	}
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/zsh"
		os.Setenv("SHELL", sh)
	}
	if path := loginPath(sh); path != "" {
		os.Setenv("PATH", path)
	}
}

// loginPath is PATH as an interactive login sh sets it, read once, or ""
// when sh fails or takes over 5s. Interactive, because zsh users often
// set PATH in .zshrc, which a login shell alone skips.
func loginPath(sh string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const mark = "_PITWALL_PATH_"
	// why: printenv runs the same in fish, which joins a quoted "$PATH"
	// with spaces; the marks skip what rc files print.
	cmd := exec.CommandContext(ctx, sh, "-ilc", "echo "+mark+"; /usr/bin/printenv PATH; echo "+mark)
	cmd.WaitDelay = time.Second // an rc file's background job may hold stdout
	out, _ := cmd.Output()      // rc files often exit non-zero; the marks tell
	_, rest, _ := strings.Cut(string(out), mark+"\n")
	path, _, ok := strings.Cut(rest, mark+"\n")
	if !ok {
		return ""
	}
	return strings.TrimSpace(path)
}
