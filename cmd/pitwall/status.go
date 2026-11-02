package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/progstatus"
	"github.com/quanticstudios/pitwall/internal/proto"
)

const statusUsage = "usage: pitwall status [-s session] [pane] [-- text], or pitwall status <pane> <text>"

const programUsage = "usage: pitwall status working|blocked|done|error|idle [--app name] [--progress N] [--message text]"

// programStates are the states pitwall status reports for the calling
// program, its first argument.
var programStates = []string{"working", "blocked", "done", "error", "idle"}

// statusCommand is pitwall status: it sets the line under a tab's row, the
// tab of pane or of the calling pane, and clears it without text or with
// --clear.
func statusCommand(args []string) error {
	if len(args) > 0 && slices.Contains(programStates, args[0]) {
		return programCommand(args)
	}
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

// programReport is pitwall status <state>'s report as the body of an OSC
// 7501 sequence, checked as the daemon checks one.
func programReport(args []string) (string, error) {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	app := flags.String("app", "", "")
	progress := flags.Int("progress", progstatus.NoProgress, "")
	msg := flags.String("message", "", "")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() > 0 {
		return "", errors.New(programUsage)
	}
	r := progstatus.Report{State: progstatus.State(args[0]), App: *app, Progress: *progress, Msg: *msg}
	switch {
	case r.App != "" && !progstatus.ValidApp(r.App):
		return "", fmt.Errorf("--app must be 1 to %d bytes of letters, digits and _ . + -", progstatus.MaxApp)
	case r.Progress != progstatus.NoProgress && (r.Progress < 0 || r.Progress > 100):
		return "", errors.New("--progress must be 0 to 100")
	case r.Progress != progstatus.NoProgress && r.State != progstatus.Working && r.State != progstatus.Blocked:
		return "", errors.New("--progress needs working or blocked")
	case len(r.Msg) > progstatus.MaxMsgDecoded || !utf8.ValidString(r.Msg) || strings.ContainsFunc(r.Msg, progstatus.IsControl):
		return "", fmt.Errorf("--message must be at most %d bytes of UTF-8 without control characters", progstatus.MaxMsgDecoded)
	}
	return progstatus.Encode(r), nil
}

// programCommand is pitwall status <state>: it reports the calling
// program's state to the pane it runs in, as report-agent reports an
// agent's. Outside a pane, or without a daemon, it does nothing.
func programCommand(args []string) error {
	body, err := programReport(args)
	if err != nil {
		return err
	}
	pane := os.Getenv("PITWALL_PANE")
	if pane == "" {
		return nil
	}
	path := os.Getenv("PITWALL_SOCKET")
	if path == "" {
		if path, err = proto.SocketPath(); err != nil {
			return nil
		}
	}
	// No name, so a daemon without program status refuses it.
	payload, _ := json.Marshal(map[string]any{"name": "", "program": body, "group": processGroup()})
	sendHook(path, pane, model.Provider("report"), payload)
	return nil
}
