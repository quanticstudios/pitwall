package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/update"
)

// runUpdate installs the latest release over this binary, unless a package
// manager owns it, then moves the daemon onto the binary with finishUpdate.
// PITWALL_UPDATE_URL replaces update.LatestURL, for the release drill.
func runUpdate(args []string, w io.Writer) error {
	if len(args) > 0 {
		return errors.New("usage: pitwall update")
	}
	log.SetOutput(io.Discard) // startDaemon's lines, for a window's gui.log
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	want := versionString()
	if real, err := filepath.EvalSymlinks(exe); err == nil && update.Managed(real) {
		fmt.Fprintln(w, "A package manager installed this pitwall and updates it.")
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		rel, newer, err := update.Check(ctx, http.DefaultClient, cmp.Or(os.Getenv("PITWALL_UPDATE_URL"), update.LatestURL), want)
		switch {
		case err != nil:
			return fmt.Errorf("check for a release: %w", err)
		case newer:
			if err := update.Install(ctx, http.DefaultClient, rel, exe); err != nil {
				return fmt.Errorf("install %s: %w", rel.Tag, err)
			}
			fmt.Fprintf(w, "Installed pitwall %s over %s.\n", rel.Tag, want)
			want = rel.Tag
		case rel.Tag == "":
			fmt.Fprintf(w, "pitwall %s is not a release, so it checks for none.\n", want)
		default:
			fmt.Fprintf(w, "pitwall %s is the latest release.\n", want)
		}
	}
	m, err := doctorDaemon()
	if errors.Is(err, errNotRunning) {
		fmt.Fprintf(w, "No daemon runs; the next window starts %s.\n", want)
		return nil
	}
	if _, refused := proto.RefusedVersion(proto.Error{Message: fmt.Sprint(err)}); err != nil && !refused {
		return fmt.Errorf("ask the daemon its version: %w", err)
	}
	if err == nil && m.Build == want {
		fmt.Fprintf(w, "The daemon already runs %s.\n", want)
		return nil
	}
	path, err := socketPath()
	if err != nil {
		return err
	}
	return finishUpdate(w, func() error { return handOff(exe) }, func() error { return restartDaemon(path, exe) })
}

// finishUpdate moves the daemon onto the pitwall just installed: through
// handOff, which keeps every program in a pane running, else through
// restart, which resumes agents and stops the rest. It says which on w.
func finishUpdate(w io.Writer, handOff, restart func() error) error {
	err := handOff()
	if err == nil {
		fmt.Fprintln(w, "The new daemon took over; every program in a pane kept running.")
		return nil
	}
	fmt.Fprintf(w, "The new daemon could not take over (%v), so pitwall restarts the daemon: agents resume, other programs in panes stop.\n", err)
	if err := restart(); err != nil {
		return fmt.Errorf("restart the daemon: %w", err)
	}
	fmt.Fprintln(w, "The daemon restarted.")
	return nil
}

var errNoHandoff = errors.New("Windows has no handoff")

// handoffWait bounds a handoff from the new daemon's start to its answer.
// Each of its own steps times out well before.
const handoffWait = time.Minute

// handOff starts bin as a daemon that takes over the panes of the daemon
// running, as docs/handoff.md describes, and waits for its answer. On an
// error the old daemon still serves every pane.
func handOff(bin string) error {
	if runtime.GOOS == "windows" {
		return errNoHandoff
	}
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	defer r.Close()
	cmd, err := daemonCmd(bin, "--handoff")
	if err != nil {
		w.Close()
		return err
	}
	cmd.ExtraFiles = []*os.File{w} // fd 3, runHandoffDaemon's answer
	err = cmd.Start()
	cmd.Stdout.(io.Closer).Close()
	w.Close()
	if err != nil {
		return err
	}
	answer := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(io.LimitReader(r, 64<<10))
		answer <- string(b)
	}()
	select {
	case a := <-answer:
		if a == "ok" {
			go cmd.Wait()
			return nil
		}
		cmd.Wait()
		if a == "" {
			return errors.New("the new daemon exited without an answer; see crash.log")
		}
		return errors.New(a)
	case <-time.After(handoffWait):
		cmd.Process.Kill() // before it confirms, the old daemon then resumes
		cmd.Wait()
		return fmt.Errorf("the new daemon did not answer within %s", handoffWait)
	}
}

// restartDaemon stops the daemon serving path and starts bin's, as a
// window's Restart now does.
// why: bin, not os.Executable: on Windows the install renamed this process's
// binary to pitwall.exe.old.
func restartDaemon(path, bin string) error {
	if err := stopIncompatibleDaemon(path); err != nil {
		return err
	}
	nc, err := startDaemonOf(path, bin)
	if err != nil {
		return err
	}
	return nc.Close()
}
