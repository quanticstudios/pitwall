package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/tmuxshim"
)

func tmuxConnection() (*cliConn, string, error) {
	from := os.Getenv("PITWALL_PANE")
	if from == "" {
		return nil, "", errors.New("tmux-shim must run inside a pitwall pane")
	}
	conn, err := dialCLI()
	if err != nil {
		return nil, "", err
	}
	state, err := syncMsg(conn)
	if err == nil && state.Level < proto.TmuxLevel {
		err = fmt.Errorf("tmux-shim needs daemon protocol level %d; this daemon is at %d", proto.TmuxLevel, state.Level)
	}
	found := false
	for _, p := range state.State.Panes {
		if p.ID == from {
			found = true
			break
		}
	}
	if err == nil && !found {
		err = errors.New("tmux-shim: PITWALL_PANE does not name a pane in this daemon")
	}
	if err != nil {
		conn.Close()
		return nil, "", err
	}
	return conn, from, nil
}

func runTmux(args []string, out io.Writer) error {
	global, err := tmuxshim.ParseFlags("tmux", tmuxshim.Spec{Bools: "V", Values: "S"}, args)
	if err != nil {
		return err
	}
	socket, err := socketPath()
	if err != nil {
		return err
	}
	marker, _, _ := strings.Cut(os.Getenv("TMUX"), ",")
	if explicit, ok := global.Value('S'); ok {
		if explicit != socket {
			return errors.New("tmux shim: -S names another server")
		}
	} else if marker != socket {
		return errors.New("tmux shim: TMUX does not name this pitwall daemon; use pitwall tmux-shim")
	}
	if global.Has('V') {
		_, err = fmt.Fprintln(out, "tmux 3.4")
		return err
	}
	conn, from, err := tmuxConnection()
	if err != nil {
		return err
	}
	defer conn.Close()
	request := proto.Tmux{From: from, Args: global.Args, Env: []string{"PATH=" + os.Getenv("PATH"), "TMUX=" + socket + fmt.Sprintf(",%d,0", os.Getpid()), "PITWALL_SOCKET=" + socket}}
	if err = conn.Send(request); err != nil {
		return err
	}
	for {
		msg, e := conn.Recv()
		if e != nil {
			return e
		}
		switch m := msg.(type) {
		case proto.TmuxResult:
			if m.Error != "" {
				return errors.New(m.Error)
			}
			_, e = io.WriteString(out, m.Output)
			return e
		case proto.Error:
			return errors.New(m.Message)
		}
	}
}

func runTmuxShim(args []string, in *os.File, out, errOut io.Writer) int {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 || args[0] == "--help" {
		fmt.Fprintln(errOut, "usage: pitwall tmux-shim -- <command> [args...]")
		return 2
	}
	fail := func(err error) int { fmt.Fprintln(errOut, "pitwall tmux-shim:", err); return 1 }
	if runtime.GOOS == "windows" {
		return fail(errors.New("the tmux shim requires Unix PTYs"))
	}
	conn, from, err := tmuxConnection()
	if err != nil {
		return fail(err)
	}
	conn.Close()
	socket, err := socketPath()
	if err != nil {
		return fail(err)
	}
	exe, err := os.Executable()
	if err != nil {
		return fail(err)
	}
	dir := filepath.Join(filepath.Dir(socket), "tmux-shim")
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return fail(err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fail(err)
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return fail(errors.New("tmux-shim directory must be private and must not be a symlink"))
	}
	link := filepath.Join(dir, fmt.Sprintf(".tmux-%d", os.Getpid()))
	if err = os.Symlink(exe, link); err != nil {
		return fail(err)
	}
	defer os.Remove(link)
	if err = os.Rename(link, filepath.Join(dir, "tmux")); err != nil {
		return fail(err)
	}
	// Keep the link in the runtime directory so a worker can outlive its lead.
	program := args[0]
	if program == "tmux" {
		program = filepath.Join(dir, "tmux")
	}
	command := exec.Command(program, args[1:]...)
	command.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "TMUX="+socket+fmt.Sprintf(",%d,0", os.Getpid()), "TMUX_PANE="+tmuxshim.ID("%", from), "PITWALL_SOCKET="+socket)
	command.Stdin, command.Stdout, command.Stderr = in, out, errOut
	if err = command.Run(); err != nil {
		var exited *exec.ExitError
		if errors.As(err, &exited) {
			code := exited.ExitCode()
			if code < 0 {
				return 1
			}
			return code
		}
		return fail(err)
	}
	return 0
}
