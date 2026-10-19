package main

import (
	"errors"
	"os"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// runFocus moves the window's focus from the calling pane to the pane next
// to it, for vim at the edge of its windows (see docs/keys.md).
func runFocus(args []string) error {
	dirs := map[string]layout.Direction{"left": layout.Left, "right": layout.Right, "up": layout.Up, "down": layout.Down}
	var d layout.Direction
	ok := len(args) == 1
	if ok {
		d, ok = dirs[args[0]]
	}
	if !ok {
		return errors.New("usage: pitwall focus left|right|up|down")
	}
	pane := os.Getenv("PITWALL_PANE")
	if pane == "" {
		return errors.New("focus must run inside a pitwall pane (PITWALL_PANE is not set)")
	}
	conn, err := dialCLI()
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = syncCLI(conn, proto.FocusDir{Pane: pane, Dir: d})
	return err
}
