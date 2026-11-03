package main

import (
	"errors"
	"flag"
	"io"
	"time"

	"github.com/quanticstudios/pitwall/internal/proto"
)

func scrollbackCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("scrollback", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	sessionName := flags.String("s", "", "")
	lines := flags.Int("lines", 0, "")
	args, err := parseFlags(flags, args)
	if err != nil || len(args) != 1 || *lines < 0 {
		return errors.New("usage: pitwall scrollback [-s session] <pane> [--lines N]")
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
