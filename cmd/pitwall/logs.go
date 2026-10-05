package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// runLogs prints where the GUI and the daemon log, or with -f follows both.
func runLogs(args []string, out io.Writer) error {
	paths := []string{filepath.Join(stateDir(), "gui.log"), filepath.Join(stateDir(), "daemon.log")}
	switch {
	case len(args) == 0:
		for _, p := range paths {
			fmt.Fprintln(out, p)
		}
		return nil
	case len(args) == 1 && args[0] == "-f":
		return follow(paths, out, nil, 250*time.Millisecond)
	}
	return errors.New("usage: pitwall logs [-f]")
}

// follow copies what is appended to each of paths to out, polling every
// interval until stop closes. It starts at each file's end; a file that
// shrank was rotated and is read from its start.
func follow(paths []string, out io.Writer, stop <-chan struct{}, interval time.Duration) error {
	offs := make([]int64, len(paths))
	for i, p := range paths {
		if fi, err := os.Stat(p); err == nil {
			offs[i] = fi.Size()
		}
	}
	for {
		for i, p := range paths {
			f, err := os.Open(p)
			if err != nil {
				continue // not written yet
			}
			if fi, err := f.Stat(); err == nil && fi.Size() < offs[i] {
				offs[i] = 0
			}
			n, _ := io.Copy(out, io.NewSectionReader(f, offs[i], 1<<62))
			offs[i] += n
			f.Close()
		}
		select {
		case <-stop:
			return nil
		case <-time.After(interval):
		}
	}
}
