package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

const scrollbackUsage = `usage: pitwall scrollback [-s session] <pane> [--lines N] [--format text|ansi|html]

Print a pane's scrollback and visible screen as plain text, ANSI, or
self-contained HTML.

Flags:
  -s session     session name or id
  --lines N      last N lines (0 = all)
  --format fmt   output format: text (default), ansi, or html
  -h, --help     show this help`

func scrollbackCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("scrollback", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	sessionName := flags.String("s", "", "")
	lines := flags.Int("lines", 0, "")
	format := flags.String("format", "", "")
	args, err := parseFlags(flags, args)
	if errors.Is(err, flag.ErrHelp) {
		_, err := fmt.Fprintln(out, scrollbackUsage)
		return err
	}
	if err != nil || len(args) != 1 || *lines < 0 {
		return errors.New(scrollbackUsage)
	}
	if *format != "" && !vt.ValidFormat(*format) {
		return fmt.Errorf("invalid --format %q: use text, ansi or html", *format)
	}
	if *format == "ansi" || *format == "html" {
		return readFormattedJSON(args[0], *sessionName, true, *lines, *format, out)
	}
	conn, err := dialCLI()
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.socket.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return err
	}
	m, err := syncMsg(conn)
	if err != nil {
		return err
	}
	if m.Level < proto.Since(proto.Scrollback{}) {
		return errOlder
	}
	session, err := currentSession(m.State, *sessionName)
	if err != nil {
		return err
	}
	_, p, err := resolvePane(m.State, session.ID, args[0])
	if err != nil {
		return err
	}
	if err := conn.Send(proto.Scrollback{Pane: p.ID, Lines: *lines}); err != nil {
		return err
	}
	for {
		msg, err := conn.Recv()
		if err != nil {
			return err
		}
		switch r := msg.(type) {
		case proto.Error:
			return errors.New(r.Message)
		case proto.ScrollbackResult:
			_, err := io.WriteString(out, r.Text)
			return err
		}
	}
}
