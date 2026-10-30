//go:build ignore

// Record small, disposable PTY sessions on Linux:
// go run ./internal/vt/testdata/handoff/record.go /absolute/path/to/claude
package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"syscall"
	"time"

	"github.com/creack/pty"
)

type input struct {
	delay time.Duration
	keys  string
}

func main() {
	if len(os.Args) != 2 {
		panic("pass the absolute path to Claude Code")
	}
	folder, err := os.MkdirTemp("", "vt-recording-")
	must(err)
	defer os.RemoveAll(folder)
	// No shell profiles, credentials, or existing home directory reach the children.
	env := []string{"PATH=/usr/bin:/bin", "HOME=" + folder, "XDG_CONFIG_HOME=" + folder,
		"TERM=xterm-256color", "LANG=C.UTF-8", "USER=recorder", "LOGNAME=recorder", "SHELL=/bin/sh"}
	text := "snapshot fixture\nUnicode: 中 café\n"
	for i := range 50 {
		text += fmt.Sprintf("line %03d\n", i)
	}
	must(os.WriteFile(filepath.Join(folder, "sample.txt"), []byte(text), 0600))
	record("vim", []string{"vim", "-Nu", "NONE", "-i", "NONE", "-n", "sample.txt"},
		[]input{{500 * time.Millisecond, "G"}, {200 * time.Millisecond, "gg"}, {200 * time.Millisecond, ":q!\r"}}, folder, env)
	sleeper := exec.Command("sleep", "30")
	sleeper.Env = env
	must(sleeper.Start())
	record("htop", []string{"htop", "-p", fmt.Sprint(sleeper.Process.Pid)},
		[]input{{500 * time.Millisecond, " "}, {200 * time.Millisecond, "q"}}, folder, env)
	must(sleeper.Process.Kill())
	_ = sleeper.Wait()
	record("claude", []string{os.Args[1]},
		[]input{{4 * time.Second, "\r"}, {time.Second, "\x03"}, {200 * time.Millisecond, "\x03"}}, folder, env)
	record("log", []string{"/bin/sh", "-c", `i=0; while [ "$i" -lt 300 ]; do printf '\033[32mrow %04d\033[0m synthetic log payload\n' "$i"; i=$((i+1)); done`},
		[]input{{500 * time.Millisecond, ""}}, folder, env)
}

func record(name string, command []string, inputs []input, folder string, env []string) {
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Env, cmd.Dir = env, folder
	master, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	must(err)
	defer master.Close()
	output := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(master) // Linux returns EIO after the slave closes.
		output <- data
	}()
	for _, in := range inputs {
		time.Sleep(in.delay)
		_, _ = io.WriteString(master, in.keys)
	}
	time.Sleep(300 * time.Millisecond)
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	_ = cmd.Wait()
	data := <-output
	account, err := user.Current()
	must(err)
	for _, personal := range []string{folder, account.Username} {
		data = bytes.ReplaceAll(data, []byte(personal), []byte("recorder"))
	}
	must(os.WriteFile(filepath.Join("internal/vt/testdata/handoff", name+".raw"), data, 0644))
	fmt.Printf("%s %d bytes\n", name, len(data))
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
