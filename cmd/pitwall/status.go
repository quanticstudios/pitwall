package main

import (
	"errors"
	"flag"
	"io"
	"slices"
	"strings"

	"github.com/quanticstudios/pitwall/internal/proto"
)

const statusUsage = "usage: pitwall status [-s session] [pane] [-- text], or pitwall status <pane> <text>"

// statusCommand is pitwall status: it sets the line under a tab's row, the
// tab of pane or of the calling pane, and clears it without text or with
// --clear.
func statusCommand(args []string) error {
	var text []string
	if i := slices.Index(args, "--"); i >= 0 {
		args, text = args[:i], args[i+1:]
	}
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	sessionName := flags.String("s", "", "")
	drop := flags.Bool("clear", false, "")
	pos, err := parseFlags(flags, args)
	if err != nil || len(pos) > 2 || len(pos) == 2 && text != nil {
		return errors.New(statusUsage)
	}
	if len(pos) == 2 {
		pos, text = pos[:1], pos[1:]
	}
	line := strings.Join(text, " ")
	if *drop {
		line = ""
	}
	conn, err := dialCLI()
	if err != nil {
		return err
	}
	defer conn.Close()
	m, err := syncMsg(conn)
	if err != nil {
		return err
	}
	if m.Level < proto.Since(proto.SetStatus{}) {
		return errors.New("the daemon is older than this pitwall; restart it to use status")
	}
	session, err := currentSession(m.State, *sessionName)
	if err != nil {
		return err
	}
	w, err := resolveTab(m.State, session.ID, "")
	if len(pos) == 1 {
		w, _, err = resolvePane(m.State, session.ID, pos[0])
	}
	if err != nil {
		return err
	}
	_, err = syncCLI(conn, proto.SetStatus{WorkspaceID: w.ID, Text: line})
	return err
}
