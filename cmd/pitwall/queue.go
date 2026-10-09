package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
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
  pitwall queue add [-s session] [dir] -- cmd...  queue an agent with its prompt, run in dir, such as
                                                  pitwall queue add -- codex "fix the flaky test"
  pitwall queue rm [-s session] <n>               take task n of pitwall queue ls off the queue`

// queueCommand runs pitwall queue: ls, add and rm.
func queueCommand(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(queueUsage)
	}
	command, args := args[0], args[1:]
	var cmd []string
	if i := slices.Index(args, "--"); i >= 0 && command == "add" {
		args, cmd = args[:i], args[i+1:]
		if len(cmd) == 0 {
			return errors.New(queueUsage)
		}
		// why: as for pitwall new, the daemon runs it with its own PATH.
		path, err := exec.LookPath(cmd[0])
		if err != nil && !errors.Is(err, exec.ErrDot) {
			return err
		}
		if cmd[0], err = filepath.Abs(path); err != nil {
			return err
		}
	}
	flags := flag.NewFlagSet("queue "+command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var sessionName string
	flags.StringVar(&sessionName, "s", "", "")
	args, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	switch {
	case command == "ls" && len(args) == 0:
	case command == "add" && cmd != nil && len(args) <= 1:
	case command == "rm" && len(args) == 1:
	default:
		return errors.New(queueUsage)
	}
	conn, err := dialCLI()
	if err != nil {
		return err
	}
	defer conn.Close()
	state, err := syncCLI(conn)
	if err != nil {
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
		_, err = syncCLI(conn, proto.NewTask{Task: model.Task{SessionID: session.ID, Dir: dir, Cmd: cmd}, Queue: true})
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
