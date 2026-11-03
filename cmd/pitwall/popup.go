package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"slices"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

const popupUsage = "usage: pitwall popup [-s session] [--width 80%] [--height 70%] [--title text] [--dir path] [--wait] [tab] [-- cmd args...]"

// popupClosed is popup --wait's exit code when the popup closed before
// its command exited, as a Ctrl+C in a picker gives.
const popupClosed = 130

type popupArgs struct {
	session, tab string
	wait         bool
	open         proto.OpenPopup
}

// parsePopup reads popup's flags. Paths and the command are made absolute
// here: the daemon runs them in another folder.
func parsePopup(args []string) (popupArgs, error) {
	var cmd []string
	if i := slices.Index(args, "--"); i >= 0 {
		args, cmd = args[:i], args[i+1:]
		if len(cmd) > 0 {
			if err := absCmd(cmd); err != nil {
				return popupArgs{}, err
			}
		}
	}
	flags := flag.NewFlagSet("popup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var p popupArgs
	flags.StringVar(&p.session, "s", "", "")
	flags.StringVar(&p.open.Width, "width", "", "")
	flags.StringVar(&p.open.Height, "height", "", "")
	flags.StringVar(&p.open.Title, "title", "", "")
	flags.StringVar(&p.open.Dir, "dir", "", "")
	flags.BoolVar(&p.wait, "wait", false, "")
	rest, err := parseFlags(flags, args)
	if err != nil || len(rest) > 1 {
		return popupArgs{}, errors.New(popupUsage)
	}
	if len(rest) == 1 {
		p.tab = rest[0]
	}
	for _, size := range []string{p.open.Width, p.open.Height} {
		if size == "" {
			continue
		}
		if _, _, err := model.ParsePopupSize(size); err != nil {
			return popupArgs{}, err
		}
	}
	if p.open.Dir != "" {
		if p.open.Dir, err = filepath.Abs(p.open.Dir); err != nil {
			return popupArgs{}, err
		}
	}
	p.open.Cmd = cmd
	return p, nil
}

// popupCommand opens a popup over a tab. With --wait it returns once the
// command exits, with its exit code.
func popupCommand(args []string, errOut io.Writer) int {
	code, err := popup(args)
	if err != nil {
		fmt.Fprintln(errOut, "pitwall:", err)
	}
	return code
}

func popup(args []string) (int, error) {
	p, err := parsePopup(args)
	if err != nil {
		return 1, err
	}
	// The watch connection is open before the popup is, so it gets the
	// PaneExited of a command that ends at once.
	var watch *watcher
	if p.wait {
		if watch, err = dialWatch(); err != nil {
			return 1, err
		}
		defer watch.Close()
	}
	conn, err := dialCLI()
	if err != nil {
		return 1, err
	}
	defer conn.Close()
	m, err := syncMsg(conn)
	if err != nil {
		return 1, err
	}
	if m.Level < proto.PopupLevel {
		return 1, errors.New("the daemon is older than this pitwall; restart it to use popup")
	}
	session, err := currentSession(m.State, p.session)
	if err != nil {
		return 1, err
	}
	w, err := resolveTab(m.State, session.ID, p.tab)
	if err != nil {
		return 1, err
	}
	p.open.WorkspaceID = w.ID
	p.open.Pane = popupPaneID()
	after, err := syncCLI(conn, p.open)
	if err != nil {
		return 1, err
	}
	if !p.wait {
		return 0, nil
	}
	return waitPopup(watch, after, p.open.Pane)
}

// waitPopup waits for pane, a popup that state (the daemon's answer to
// OpenPopup) came after, and returns its command's exit code.
func waitPopup(watch *watcher, state model.State, pane string) (int, error) {
	opened := state.Version
	for {
		if code, ok := watch.exits[pane]; ok {
			return exitCode(code), nil
		}
		if state.Version >= opened && !slices.ContainsFunc(state.Panes, func(p model.Pane) bool { return p.ID == pane }) {
			if code, ok := watch.exitOf(state, pane); ok {
				return exitCode(code), nil
			}
			return popupClosed, nil
		}
		var err error
		if state, err = watch.next(); err != nil {
			return 1, err
		}
	}
}

// exitCode is a command's exit code as popup --wait's: popupClosed for a
// command a signal ended.
func exitCode(code int) int {
	if code < 0 {
		return popupClosed
	}
	return code
}

// popupPaneID is a new pane id, newID's shape, so popup --wait knows which
// PaneExited is its popup's before the daemon answers.
func popupPaneID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
