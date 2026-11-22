package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const eventsUsage = `usage: pitwall events [--json] [--session S | -s S] [--pane P]

Stream changes from the pitwall daemon, one line per event: pane agent states
(working, pending-approval, awaiting-input, plan-ready, done, error, with
detail), titles, tabs opened or closed, PR state changes, queued messages
sent, turn ends and pane exits.

Flags:
  --json           print one JSON object per line (stable fields; see docs/web-protocol.md)
  --session, -s S  only events for session S (id, name, or unique prefix)
  --pane P         only events for pane P (#N, TAB.N, title, or @id)
  -h, --help       show this help

Exits 0 on Ctrl+C, non-zero if the daemon connection is lost.`

type eventsOptions struct {
	asJSON  bool
	session string
	pane    string
	help    bool
}

func parseEventsArgs(args []string) (eventsOptions, error) {
	flags := flag.NewFlagSet("events", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var opt eventsOptions
	flags.BoolVar(&opt.asJSON, "json", false, "")
	flags.StringVar(&opt.session, "session", "", "")
	flags.StringVar(&opt.session, "s", "", "")
	flags.StringVar(&opt.pane, "pane", "", "")
	rest, err := parseFlags(flags, args)
	if errors.Is(err, flag.ErrHelp) {
		opt.help = true
		return opt, nil
	}
	if err != nil || len(rest) != 0 {
		return eventsOptions{}, errors.New(eventsUsage)
	}
	return opt, nil
}

func eventsCommand(args []string, out, errOut io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runEvents(ctx, args, out, errOut)
}

func runEvents(ctx context.Context, args []string, out, errOut io.Writer) int {
	opt, err := parseEventsArgs(args)
	if err != nil {
		fmt.Fprintln(errOut, "pitwall:", err)
		return 1
	}
	if opt.help {
		fmt.Fprintln(out, eventsUsage)
		return 0
	}
	sess := opt.session
	if opt.pane != "" {
		sess = callerSessionForJSON(sess, opt.pane)
	}
	c, _, err := dialJSON("pitwall-events")
	if err != nil {
		fmt.Fprintln(errOut, "pitwall:", err)
		return 1
	}
	defer c.Close()

	req := map[string]any{"type": "subscribeEvents"}
	if sess != "" {
		req["session"] = sess
	}
	if opt.pane != "" {
		req["pane"] = opt.pane
	}
	if _, err := c.call(req, 5*time.Second); err != nil {
		fmt.Fprintln(errOut, "pitwall:", err)
		return 1
	}

	stopConn := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stopConn()

	for {
		raw, frame, err := c.nextFrame()
		if err != nil {
			if ctx.Err() != nil {
				return 0
			}
			if errors.Is(err, io.EOF) {
				fmt.Fprintln(errOut, "pitwall: daemon connection lost")
			} else {
				fmt.Fprintln(errOut, "pitwall:", err)
			}
			return 1
		}
		switch frame["type"] {
		case "close":
			if ctx.Err() != nil {
				return 0
			}
			reason, _ := frame["reason"].(string)
			if reason == "" {
				reason = "daemon exiting"
			}
			fmt.Fprintf(errOut, "pitwall: daemon connection lost (%s)\n", reason)
			return 1
		case "event":
			if opt.asJSON {
				if _, err := out.Write(append(raw, '\n')); err != nil {
					fmt.Fprintln(errOut, "pitwall:", err)
					return 1
				}
			} else {
				if _, err := fmt.Fprintln(out, formatEventLine(frame)); err != nil {
					fmt.Fprintln(errOut, "pitwall:", err)
					return 1
				}
			}
		}
	}
}

func formatEventLine(frame map[string]any) string {
	str := func(k string) string { s, _ := frame[k].(string); return s }
	flat := func(s string) string {
		s = strings.ReplaceAll(s, "\r\n", " ")
		s = strings.ReplaceAll(s, "\n", " ")
		return strings.Join(strings.Fields(s), " ")
	}
	ts := str("at")
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		ts = t.Local().Format("15:04:05")
	}
	ev := str("event")
	target := ""
	if p := str("pane"); p != "" {
		target = "@" + p
	} else if tab := str("tab"); tab != "" {
		target = "@" + tab
	}
	parts := []string{}
	if ts != "" {
		parts = append(parts, ts)
	}
	parts = append(parts, ev)
	if target != "" {
		parts = append(parts, target)
	}
	switch ev {
	case "pane.state":
		if a := str("agent"); a != "" {
			parts = append(parts, a)
		}
		st := str("state")
		if d := flat(str("detail")); d != "" {
			parts = append(parts, st+": "+d)
		} else if st != "" {
			parts = append(parts, st)
		}
	case "title", "tab.opened", "tab.closed":
		if t := flat(str("title")); t != "" {
			parts = append(parts, t)
		}
	case "pr.state":
		if n, ok := frame["prNumber"].(float64); ok && n > 0 {
			parts = append(parts, fmt.Sprintf("#%d", int(n)))
		}
		if ps := str("prState"); ps != "" {
			parts = append(parts, ps)
		}
		if r := str("prReason"); r != "" {
			parts = append(parts, r)
		}
		if d := flat(str("prDetail")); d != "" {
			parts = append(parts, "("+d+")")
		}
	case "queued.sent":
		if f := str("from"); f != "" {
			parts = append(parts, "from @"+f)
		}
		if q, ok := frame["queued"].(float64); ok {
			parts = append(parts, fmt.Sprintf("(remaining: %d)", int(q)))
		}
	case "turn.end":
		if a := str("agent"); a != "" {
			parts = append(parts, a)
		}
		st := str("state")
		if d := flat(str("detail")); d != "" {
			parts = append(parts, st+": "+d)
		} else if st != "" {
			parts = append(parts, st)
		}
	case "pane.exited":
		if code, ok := frame["exitCode"].(float64); ok {
			parts = append(parts, fmt.Sprintf("exit %d", int(code)))
		}
	}
	return strings.Join(parts, " ")
}
