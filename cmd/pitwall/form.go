package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/quanticstudios/pitwall/internal/form"
	"github.com/quanticstudios/pitwall/internal/proto"
)

const formUsage = "usage: pitwall form [--id id] [file], or pitwall form --id id --update [file], or pitwall form --id id --close"

// formCommand is pitwall form: it shows the spec in file, or on stdin,
// over the calling pane's tab and prints the user's answers as JSON. It
// exits 0 with the answers, 1 without (the user cancelled, or a script
// closed the form), and 2 on an error. --update replaces an open form's
// spec, and --close ends it.
func formCommand(args []string, stdin io.Reader, out, errOut io.Writer) int {
	err := runForm(args, stdin, out)
	if err == errNoAnswer {
		return 1
	}
	if err != nil {
		fmt.Fprintln(errOut, "pitwall form:", err)
		return 2
	}
	return 0
}

var errNoAnswer = errors.New("no answer")

func runForm(args []string, stdin io.Reader, out io.Writer) error {
	flags := flag.NewFlagSet("form", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	id := flags.String("id", "", "")
	update := flags.Bool("update", false, "")
	closeIt := flags.Bool("close", false, "")
	pos, err := parseFlags(flags, args)
	if err != nil || len(pos) > 1 || *update && *closeIt || (*update || *closeIt) && *id == "" || *closeIt && len(pos) > 0 {
		return errors.New(formUsage)
	}
	var spec []byte
	if !*closeIt {
		if spec, err = readSpec(pos, stdin); err != nil {
			return err
		}
		if _, err := form.Parse(spec); err != nil {
			return err
		}
	}
	pane := os.Getenv("PITWALL_PANE")
	if pane == "" && !*update && !*closeIt {
		return errors.New("pitwall form must run inside a pitwall pane (PITWALL_PANE is not set): the form shows over the pane's tab")
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
	if m.Level < proto.FormLevel {
		return errors.New("the daemon is older than this pitwall; restart it to use form")
	}
	switch {
	case *closeIt:
		_, err = syncCLI(conn, proto.FormAnswer{ID: *id, Closed: true})
		return err
	case *update:
		_, err = syncCLI(conn, proto.OpenForm{ID: *id, Spec: string(spec), Update: true})
		return err
	}
	if *id == "" {
		*id = newFormID()
	}
	if err := conn.Send(proto.OpenForm{ID: *id, Pane: pane, Spec: string(spec)}); err != nil {
		return err
	}
	if err := conn.socket.SetDeadline(time.Time{}); err != nil { // the user takes as long as they take
		return err
	}
	return awaitAnswer(conn.Conn, *id, out)
}

// readSpec reads the spec from the file pos names, or from stdin for -
// or none, up to form.MaxSize and a byte more so Parse names the limit.
func readSpec(pos []string, stdin io.Reader) ([]byte, error) {
	r := stdin
	if len(pos) == 1 && pos[0] != "-" {
		f, err := os.Open(pos[0])
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r = f
	}
	if r == nil {
		return nil, errors.New(formUsage)
	}
	return io.ReadAll(io.LimitReader(r, form.MaxSize+1))
}

// awaitAnswer waits for form id's FormAnswer and prints the answers, or
// returns errNoAnswer for a cancel or a close.
func awaitAnswer(conn *proto.Conn, id string, out io.Writer) error {
	for {
		msg, err := conn.Recv()
		if err != nil {
			return fmt.Errorf("the daemon went away before an answer: %w", err)
		}
		switch m := msg.(type) {
		case proto.Error:
			return errors.New(m.Message)
		case proto.FormAnswer:
			if m.ID != id {
				continue
			}
			if m.Cancel || m.Closed {
				return errNoAnswer
			}
			_, err := fmt.Fprintln(out, m.Answers)
			return err
		}
	}
}

func newFormID() string {
	b := make([]byte, 4)
	rand.Read(b) // why: never fails (crypto/rand panics instead)
	return "form-" + hex.EncodeToString(b)
}
