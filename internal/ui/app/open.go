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
	if err := OpenURL(url); err != nil {
		log.Printf("open link: %q", err)
	}
}

// OpenURL opens url in the user's default browser, as an argument of its
// own, never as shell text.
func OpenURL(url string) error {
	cmd := openCommand(url)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
