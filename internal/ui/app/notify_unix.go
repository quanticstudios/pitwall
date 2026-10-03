//go:build unix

package app

import (
	"context"
	"os/exec"
	"runtime"
	"time"
)

const notifyTimeout = 2 * time.Second

// desktopCommand shows one desktop notification. A later one with the same
// group replaces it where the notification server supports that.
func desktopCommand(ctx context.Context, urgent bool, group, title, body string) *exec.Cmd {
	if runtime.GOOS == "darwin" {
		// The text goes in as argv, so it needs no AppleScript quoting.
		return exec.CommandContext(ctx, "osascript", "-e", "on run argv",
			"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
			"-e", "end run", "--", title, body)
	}
	urgency := "normal"
	if urgent {
		urgency = "critical"
	}
	return exec.CommandContext(ctx, "notify-send", "--app-name=pitwall", "--urgency="+urgency,
		"--hint=string:x-canonical-private-synchronous:pitwall-"+group, "--", title, body)
}
