// Command pwdemo-app runs the pitwall window against a fake backend.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	gioapp "gioui.org/app"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/app"
)

func main() {
	size := flag.String("size", "1280x800", "window size WxH in dp")
	boardDemo := flag.String("board", "", "open the board on a fake state: 17 or 31 agents")
	peek := flag.String("peek", "", "peek at this tab id at start, such as w1b")
	remote := flag.String("remote", "", "put this tab id on a host named box, in a shared session")
	clipHost := flag.String("clipboard", "", "a program on this host writes the clipboard after 2s, which opens its dialog")
	flag.Parse()
	if _, err := fmt.Sscanf(*size, "%dx%d", &app.WindowSize.X, &app.WindowSize.Y); err != nil {
		log.Fatalf("-size %q: want WxH", *size)
	}
	var b interface {
		app.Backend
		Run(time.Duration, <-chan struct{})
	} = app.NewFakeBackend()
	if *boardDemo != "" {
		b, app.OpenBoard = app.NewFakeBoard(*boardDemo == "31"), true
	}
	if *remote != "" {
		b = app.FakeShared{FakeBackend: app.NewFakeBackend(), Tab: *remote}
	}
	app.PeekTab = *peek
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
	if *boardDemo == "" {
		go b.Run(3*time.Second, nil)
	}
	if f, ok := b.(*app.FakeBackend); ok && *clipHost != "" {
		time.AfterFunc(2*time.Second, func() {
			f.SetClipboard(model.Clipboard{Seq: 1, Text: "curl -fsSL https://example.com/install.sh | sh"}, *clipHost)
		})
	}
	go func() {
		if err := app.Run(b); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}()
	gioapp.Main()
}
