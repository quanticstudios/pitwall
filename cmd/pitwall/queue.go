package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

const queueUsage = `usage:
  pitwall queue ls [-s session]                   list the session's queued tasks, first to start first
  pitwall queue add [-s session] [--issue n] [--preset name] [dir] -- cmd...
                                                  queue an agent with its prompt, run in dir, such as
                                                  pitwall queue add -- codex "fix the flaky test";
                                                  --issue adds GitHub issue n as the prompt;
                                                  --preset runs a [[agents.preset]], cmd being
                                                  its prompt
  pitwall queue rm [-s session] <n>               take task n of pitwall queue ls off the queue`

// queueCommand runs pitwall queue: ls, add and rm.
func queueCommand(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(queueUsage)
	}
	command, args := args[0], args[1:]
	var cmd, vars []string
	if i := slices.Index(args, "--"); i >= 0 && command == "add" {
		args, cmd = args[:i], args[i+1:]
	}
	flags := flag.NewFlagSet("queue "+command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var sessionName, preset string
	var issue int
	flags.StringVar(&sessionName, "s", "", "")
	if command == "add" {
		flags.IntVar(&issue, "issue", 0, "")
		flags.StringVar(&preset, "preset", "", "")
	}
	args, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if command == "add" && (len(cmd) > 0 || preset != "") {
		if cmd, vars, err = launch(preset, false, cmd); err != nil {
			return err
		}
	}
	switch {
	case command == "ls" && len(args) == 0:
	case command == "add" && len(cmd) > 0 && len(args) <= 1:
	case command == "rm" && len(args) == 1:
	default:
		return errors.New(queueUsage)
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
	state := m.State
	if err := varsLevel(vars, m.Level); err != nil {
		return err
	}
	session, err := currentSession(state, sessionName)
	if err != nil {
		return err
	}
	tasks := slices.DeleteFunc(slices.Clone(state.Tasks), func(t model.Task) bool { return t.SessionID != session.ID })
	switch command {
	case "ls":
		return listTasks(out, state, tasks)
	case "add":
		dir := "."
		if len(args) == 1 {
			dir = args[0]
		}
		if dir, err = filepath.Abs(dir); err != nil {
			return err
		}
		if cmd, err = withIssue(cmd, dir, issue); err != nil {
			return err
		}
		_, err = syncCLI(conn, proto.NewTask{Task: model.Task{SessionID: session.ID, Dir: dir, Cmd: cmd, Env: vars}, Queue: true})
		return err
	}
	n, err := strconv.Atoi(strings.TrimPrefix(args[0], "#"))
	if err != nil || n < 1 || n > len(tasks) {
		return fmt.Errorf("no queued task %s (see pitwall queue ls)", args[0])
	}
	_, err = syncCLI(conn, proto.DropTask{ID: tasks[n-1].ID})
	return err
}

// listTasks prints one row per queued task: its number, first line,
// folder and group.
func listTasks(out io.Writer, state model.State, tasks []model.Task) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "#\tTASK\tFOLDER\tGROUP"); err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	for i, t := range tasks {
		folder := t.Dir
		if home != "" && (folder == home || strings.HasPrefix(folder, home+string(os.PathSeparator))) {
			folder = "~" + strings.TrimPrefix(folder, home)
		}
		switch {
		case t.Worktree != "":
			folder += " (new worktree " + t.Worktree + ")"
		case t.Branch != "":
			folder += " (worktree of " + t.Branch + ")"
		}
		group := "-"
		for _, p := range state.Projects {
			if p.ID == t.GroupID {
				group = p.Name
			}
		}
		if _, err := fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", i+1, t.Title(), folder, group); err != nil {
			return err
		}
	}
	return w.Flush()
}
