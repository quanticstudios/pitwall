package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/quanticstudios/pitwall/internal/control"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

const awayUsage = "usage: pitwall away [-s session] [tab|@pane] [--json], or pitwall away --dismiss <tab|@pane>"

// awayRow is one Since you left card as pitwall away --json prints it:
// the tab's address, id and title, the agent's state now, how long the
// user was away, and the card's fields (model.Away).
type awayRow struct {
	Addr  string `json:"addr"`
	Tab   string `json:"tab"`
	Title string `json:"title"`
	Now   string `json:"now"` // Working, Needs you, Done or ""
	For   string `json:"for"` // how long, as "2h 14m"
	model.Away
}

// awayCommand is pitwall away: it prints the Since you left cards of the
// session's tabs, of one tab, or of one pane, and with --dismiss clears
// one tab's or pane's cards everywhere. Reading never dismisses.
func awayCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("away", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	sessionName := flags.String("s", "", "")
	asJSON := flags.Bool("json", false, "")
	dismiss := flags.String("dismiss", "", "")
	pos, err := parseFlags(flags, args)
	if err != nil || len(pos) > 1 || *dismiss != "" && (len(pos) > 0 || *asJSON) {
		return errors.New(awayUsage)
	}
	conn, err := dialCLI()
	if errors.Is(err, errNotRunning) && *dismiss == "" {
		if *asJSON {
			_, err = fmt.Fprintln(out, "[]")
		}
		return err
	}
	if err != nil {
		return err
	}
	defer conn.Close()
	m, err := syncMsg(conn)
	if err != nil {
		return err
	}
	if m.Level < proto.AwayLevel {
		return errors.New("the daemon is older than this pitwall; restart it to use away")
	}
	session, err := currentSession(m.State, *sessionName)
	if err != nil {
		return err
	}
	target := strings.Join(pos, "")
	if *dismiss != "" {
		target = *dismiss
	}
	rows, err := awayRows(m.State, session.ID, target, func(name string) (model.Workspace, string, error) {
		if strings.HasPrefix(name, "@") {
			if w, err := resolveTab(m.State, session.ID, name); err == nil && w.ID == strings.TrimPrefix(name, "@") {
				return w, "", nil
			}
			w, p, err := resolvePane(m.State, session.ID, name)
			return w, p.ID, err
		}
		w, err := resolveTab(m.State, session.ID, name)
		return w, "", err
	})
	if err != nil {
		return err
	}
	if *dismiss != "" {
		if len(rows) == 0 {
			return fmt.Errorf("%s has no Since you left card", target)
		}
		var reqs []any
		for _, r := range rows {
			reqs = append(reqs, proto.ChatAwayDismiss{Chat: r.Chat})
		}
		_, err := syncCLI(conn, reqs...)
		return err
	}
	return printAway(out, rows, *asJSON)
}

// awayRows are the cards pitwall away prints: of every numbered tab of
// session when target is "", else of the tab or pane resolve names (a
// pane id "" for the whole tab).
func awayRows(state model.State, session, target string, resolve func(string) (model.Workspace, string, error)) ([]awayRow, error) {
	tabs := state.Numbered(session)
	var only *model.Workspace
	pane := ""
	if target != "" {
		w, p, err := resolve(target)
		if err != nil {
			return nil, err
		}
		only, pane = &w, p
	}
	rows := []awayRow{}
	add := func(n int, w model.Workspace) {
		for _, a := range control.TabAway(state, w) {
			if pane != "" && a.Chat != pane {
				continue
			}
			r := awayRow{Addr: control.Addr("", n), Tab: w.ID, Title: control.Title(w), For: model.AwayFor(a.At.Sub(a.Since)), Away: a}
			for _, act := range state.Activities {
				if act.PaneID == a.Chat {
					r.Now = model.AwayNow(state.AwaitPR(act).State)
				}
			}
			rows = append(rows, r)
		}
	}
	for i, w := range tabs {
		if only == nil || w.ID == only.ID {
			add(i+1, w)
		}
	}
	if only != nil && len(rows) == 0 && !containsTab(tabs, only.ID) {
		add(0, *only) // a tab of another session, by @id
	}
	return rows, nil
}

func containsTab(tabs []model.Workspace, id string) bool {
	for _, w := range tabs {
		if w.ID == id {
			return true
		}
	}
	return false
}

// printAway writes rows as JSON, or as one block per card: the tab, how
// long, the agent's state, the summary and the facts.
func printAway(out io.Writer, rows []awayRow, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(out).Encode(rows)
	}
	var b strings.Builder
	for _, r := range rows {
		head := r.Title
		if r.Addr != "#0" {
			head = r.Addr + " " + head
		}
		head += " · since you left " + r.For
		if r.Now != "" {
			head += " · " + r.Now
		}
		b.WriteString(head + "\n")
		for _, line := range []string{r.Status, r.Needs, strings.Join(model.AwayFacts(r.Away), " · "), lastFailed(r.Failed)} {
			if r.Asking && line == r.Needs {
				line = "(summarizing)"
			}
			if line != "" {
				b.WriteString("  " + line + "\n")
			}
		}
	}
	_, err := io.WriteString(out, b.String())
	return err
}

func lastFailed(cmd string) string {
	if cmd == "" {
		return ""
	}
	return "Last failed: " + cmd
}
