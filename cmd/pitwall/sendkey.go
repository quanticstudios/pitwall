package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/quanticstudios/pitwall/internal/input"
)

const sendKeyUsage = `usage: pitwall send-key [-s session] <pane> [--] <key>...

Send single keys by name to a pane, encoded the way the GUI encodes them for
the pane's current terminal modes (application cursor keys, kitty keyboard
protocol, etc.). Refused on guarded tabs.

Key names:
  enter, esc, tab, shift+tab, space, backspace, delete, insert,
  up, down, left, right, home, end, pageup, pagedown, f1..f12,
  modifiers ctrl+, alt+, shift+, super+ (e.g. ctrl+c, shift+enter, alt+x),
  or any single digit or letter (e.g. 1, y, Y).

Flags:
  -s, --session S  session id, name, or unique prefix
  -h, --help       show this help`

type sendKeyOptions struct {
	session string
	pane    string
	keys    []string
	help    bool
}

func parseSendKeyArgs(args []string) (sendKeyOptions, error) {
	var after []string
	if i := slices.Index(args, "--"); i >= 0 {
		args, after = args[:i], args[i+1:]
	}
	flags := flag.NewFlagSet("send-key", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var opt sendKeyOptions
	flags.StringVar(&opt.session, "s", "", "")
	flags.StringVar(&opt.session, "session", "", "")
	pos, err := parseFlags(flags, args)
	if errors.Is(err, flag.ErrHelp) {
		opt.help = true
		return opt, nil
	}
	if err != nil {
		return sendKeyOptions{}, errors.New(sendKeyUsage)
	}
	pos = append(pos, after...)
	if len(pos) < 2 {
		return sendKeyOptions{}, errors.New(sendKeyUsage)
	}
	opt.pane = pos[0]
	opt.keys = pos[1:]
	for _, k := range opt.keys {
		if _, _, err := input.ParseKey(k); err != nil {
			return sendKeyOptions{}, err
		}
	}
	return opt, nil
}

func sendKeyCommand(args []string, out, errOut io.Writer) int {
	opt, err := parseSendKeyArgs(args)
	if err != nil {
		fmt.Fprintln(errOut, "pitwall:", err)
		return 1
	}
	if opt.help {
		fmt.Fprintln(out, sendKeyUsage)
		return 0
	}
	sess := callerSessionForJSON(opt.session, opt.pane)
	c, _, err := dialJSON("pitwall-send-key")
	if err != nil {
		fmt.Fprintln(errOut, "pitwall:", err)
		return 1
	}
	defer c.Close()
	req := map[string]any{
		"type": "sendKey",
		"pane": opt.pane,
		"keys": opt.keys,
	}
	if sess != "" {
		req["session"] = sess
	}
	if _, err := c.call(req, 5*time.Second); err != nil {
		fmt.Fprintln(errOut, "pitwall:", err)
		return 1
	}
	return 0
}
