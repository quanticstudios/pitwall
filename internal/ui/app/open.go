package app

import "log"

// openLink opens a link the user Ctrl+clicked in a pane with the OS opener.
// The URL goes in as an argument of its own, never as shell text.
func openLink(url string) {
	cmd := openCommand(url)
	if err := cmd.Start(); err != nil {
		log.Printf("open link: %q", err)
		return
	}
	go func() { _ = cmd.Wait() }()
}
