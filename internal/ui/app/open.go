package app

import (
	"log"
	"strings"
)

// openLink opens a link the user Ctrl+clicked in a pane with the OS opener.
// The URL goes in as an argument of its own, never as shell text. A file
// link on a Host names a file there, which this machine cannot open.
func openLink(url string) {
	if Host != "" && strings.HasPrefix(url, "file:") {
		log.Printf("open link: %q is on %s", url, Host)
		return
	}
	cmd := openCommand(url)
	if err := cmd.Start(); err != nil {
		log.Printf("open link: %q", err)
		return
	}
	go func() { _ = cmd.Wait() }()
}
