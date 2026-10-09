// Command pwdemo-app runs the pitwall window against a fake backend.
package main

import (
	"log"
	"os"
	"time"

	gioapp "gioui.org/app"

	"github.com/quanticstudios/pitwall/internal/ui/app"
)

func main() {
	b := app.NewFakeBackend()
	// A canned install: the demo never touches an agent's config.
	app.InstallHooks = func(dry, statusline bool) (string, error) {
		time.Sleep(300 * time.Millisecond)
		if dry {
			line := ""
			if statusline {
				line = "/home/you/.claude/settings.json: wrapped statusLine: ~/.claude/statusline.sh\n"
			}
			return line + "/home/you/.claude/settings.json: added SessionStart: 'pitwall' hook claude\n" +
				"/home/you/.claude/settings.json: added Stop: 'pitwall' hook claude\n" +
				"/home/you/.codex/hooks.json: unchanged\n" +
				"/home/you/.gemini/settings.json: added BeforeAgent: 'pitwall' hook gemini\n" +
				"/home/you/.config/opencode/plugins/pitwall.js: wrote OpenCode plugin\n", nil
		}
		return "Backup: /home/you/.claude/settings.json.pitwall-backup-1791470000\n", nil
	}
	go b.Run(3*time.Second, nil)
	go func() {
		if err := app.Run(b); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}()
	gioapp.Main()
}
