package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

const sessionUsage = `usage:
  pitwall session ls [--json]              list sessions
  pitwall session new [name] [-d] [dir]    make a session and open a window on it (-d: don't)
  pitwall session attach <name>            open a window on a session, or raise the one showing it
  pitwall session rename [old] <new>       rename a session (the current one without old)
  pitwall session kill [-f] <name>         end a session and close its processes`

// sessionJSON is one row of pitwall session ls --json.
type sessionJSON struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Current  bool      `json:"current"`
	Tabs     int       `json:"tabs"`
	Detached int       `json:"detached"`
	Working  int       `json:"working"`
	NeedsYou int       `json:"needs_you"`
	Windows  int       `json:"windows"`
	LastUsed time.Time `json:"last_used"`
}

// parseInterspersed parses flags that may come after positional arguments,
// as in "session new work -d".
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos, args = append(pos, fs.Arg(0)), fs.Args()[1:]
	}
}

func sessionsCommand(args []string, in *os.File, out, errOut io.Writer) error {
	if len(args) == 0 {
		return errors.New(sessionUsage)
	}
	command := args[0]
	fs := flag.NewFlagSet("session "+command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var asJSON, detached, force bool
	switch command {
	case "ls":
		fs.BoolVar(&asJSON, "json", false, "")
	case "new":
		fs.BoolVar(&detached, "d", false, "")
	case "kill":
		fs.BoolVar(&force, "f", false, "")
	case "attach", "rename":
	default:
		return errors.New(sessionUsage)
	}
	pos, err := parseInterspersed(fs, args[1:])
	if err != nil {
		return err
	}
	if (command == "ls" && len(pos) != 0) || (command == "new" && len(pos) > 2) ||
		((command == "attach" || command == "kill") && len(pos) != 1) ||
		(command == "rename" && (len(pos) < 1 || len(pos) > 2)) {
		return errors.New(sessionUsage)
	}
	conn, err := dialCLI()
	if errors.Is(err, errNotRunning) && command == "ls" {
		if asJSON {
			_, err = fmt.Fprintln(out, "[]")
			return err
		}
		return nil
	}
	if err != nil {
		return err
	}
	defer conn.Close()
	state, err := syncCLI(conn)
	if err != nil {
		return err
	}
	switch command {
	case "ls":
		return listSessions(out, state, asJSON)
	case "new":
		name, dir := "", "."
		if len(pos) > 0 {
			name = pos[0]
		}
		if len(pos) > 1 {
			dir = pos[1]
		}
		if dir, err = filepath.Abs(dir); err != nil {
			return err
		}
		after, err := syncCLI(conn, proto.SessionNew{Name: name, Cwd: dir})
		if err != nil {
			return err
		}
		var made []model.Session
		for _, s := range after.Sessions {
			if state.Session(s.ID) == nil {
				made = append(made, s)
			}
		}
		if len(made) != 1 {
			return errors.New("cannot identify the new session from daemon state")
		}
		if _, err := fmt.Fprintln(out, made[0].Name); err != nil {
			return err
		}
		if detached {
			return nil
		}
		return launchGUI(made[0].Name, "")
	case "attach":
		s, err := resolveSession(state, pos[0])
		if err != nil {
			return err
		}
		if s.Windows > 0 {
			_, err = syncCLI(conn, proto.FocusSession{SessionID: s.ID})
			return err
		}
		return launchGUI(s.Name, "")
	case "rename":
		s, err := currentSession(state, "")
		if len(pos) == 2 {
			s, err = resolveSession(state, pos[0])
		}
		if err != nil {
			return err
		}
		_, err = syncCLI(conn, proto.SessionRename{SessionID: s.ID, Name: pos[len(pos)-1]})
		return err
	case "kill":
		s, err := resolveSession(state, pos[0])
		if err != nil {
			return err
		}
		if !force && in != nil && term.IsTerminal(in.Fd()) {
			sum := state.Summary(s.ID)
			if _, err := fmt.Fprintf(errOut, "kill session %s and its %s? [y/N] ", s.Name, plural(sum.Tabs+sum.Detached, "tab")); err != nil {
				return err
			}
			answer, err := bufio.NewReader(in).ReadString('\n')
			if err != nil {
				return err
			}
			if answer = strings.ToLower(strings.TrimSpace(answer)); answer != "y" && answer != "yes" {
				return nil
			}
		}
		_, err = syncCLI(conn, proto.SessionKill{SessionID: s.ID})
		return err
	}
	return nil
}

// listSessions prints one row per session, the current one starred.
func listSessions(out io.Writer, state model.State, asJSON bool) error {
	cur, _ := currentSession(state, "")
	if asJSON {
		rows := []sessionJSON{}
		for _, s := range state.Sessions {
			sum := state.Summary(s.ID)
			rows = append(rows, sessionJSON{ID: s.ID, Name: s.Name, Current: s.ID == cur.ID, Tabs: sum.Tabs, Detached: sum.Detached,
				Working: sum.Working, NeedsYou: sum.NeedsYou, Windows: s.Windows, LastUsed: s.UsedAt})
		}
		return json.NewEncoder(out).Encode(rows)
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "NAME\tTABS\tWORKING\tNEEDS YOU\tWINDOWS\tLAST USED"); err != nil {
		return err
	}
	for _, s := range state.Sessions {
		sum := state.Summary(s.ID)
		name, tabs := s.Name, fmt.Sprint(sum.Tabs)
		if s.ID == cur.ID {
			name += " *"
		}
		if sum.Detached > 0 {
			tabs += fmt.Sprintf(" (+%d detached)", sum.Detached)
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%s\n", name, tabs, sum.Working, sum.NeedsYou, s.Windows, ago(time.Since(s.UsedAt))); err != nil {
			return err
		}
	}
	return w.Flush()
}

// ago is a duration as "just now", "5m ago", "3h ago" or "2d ago".
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// guiTarget is the session a new window opens on: the one named name, else
// the most recently used one no window shows. raise is true when a named
// session has a window already, so the new process raises that window.
func guiTarget(state model.State, name string) (session model.Session, raise bool) {
	if name == "" {
		// The daemon gave a window without -s the most recent free session.
		s := state.RecentFree()
		if s == nil {
			s = state.Recent()
		}
		if s == nil {
			return model.Session{}, false
		}
		return *s, false
	}
	s := state.SessionNamed(name)
	if s == nil {
		return model.Session{}, false
	}
	return *s, s.Windows > 0
}
