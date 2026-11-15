//go:build nogui

package main

import (
	"errors"
	"fmt"
	"os"
)

// binName is this binary's name: pitwall-core, the daemon and every
// command but the window.
const binName = "pitwall-core"

// canOpenWindow tells the commands that open a window that they cannot.
const canOpenWindow = false

// errNoWindow is what pitwall-core says where the desktop pitwall would
// open a window.
var errNoWindow = errors.New("pitwall-core has no window; open one with the desktop pitwall")

// openURL fails, so web open prints the link instead; a var for tests.
var openURL = func(string) error { return errNoWindow }

// launchGUI fails; the commands that call it refuse before they get here.
// A var for tests.
var launchGUI = func(string, string) error { return errNoWindow }

// runWindow refuses the commands that open a window: none, -s and --host.
func runWindow(string, []string) {
	fmt.Fprintf(os.Stderr, "pitwall: %v\n%s", errNoWindow, usage)
	os.Exit(2)
}
