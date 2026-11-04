package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

const forkUsage = "usage: pitwall fork [tab|pane] [--worktree name] [-n name]"

func forkCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("fork", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var worktree, name string
	flags.StringVar(&worktree, "worktree", "", "")
	flags.StringVar(&name, "n", "", "")
	args, err := parseFlags(flags, args)
	if err != nil {
		return fmt.Errorf("%w\n%s", err, forkUsage)
	}
	if len(args) > 1 {
		return errors.New(forkUsage)
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
	if m.Level < proto.ForkLevel {
		return errors.New("terminal forks need a newer daemon; update pitwall on the daemon's host")
	}
	session, err := currentSession(m.State, "")
	if err != nil {
		return err
	}
	address := ""
	if len(args) > 0 {
		address = args[0]
	} else if pane := os.Getenv("PITWALL_PANE"); pane != "" {
		address = "@" + pane
	}
	_, p, err := resolvePane(m.State, session.ID, address)
	if err != nil {
		return err
	}
	after, err := syncCLI(conn, proto.ForkPane{Pane: p.ID, Worktree: worktree, Name: name, ParentPane: os.Getenv("PITWALL_PANE")})
	if err != nil {
		return err
	}
	var added []model.Workspace
	for _, w := range after.Workspaces {
		if !slices.ContainsFunc(m.State.Workspaces, func(old model.Workspace) bool { return old.ID == w.ID }) && (name == "" || w.Name == name) {
			added = append(added, w)
		}
	}
	if len(added) != 1 {
		return errors.New("cannot identify the new tab from daemon state")
	}
	n := slices.IndexFunc(numbered(after, added[0].SessionID), func(w model.Workspace) bool { return w.ID == added[0].ID })
	_, err = fmt.Fprintf(out, "#%d\n", n+1)
	return err
}
