package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// runLogs prints where the GUI and the daemon log, and crash.log, or with -f
// follows all three.
func runLogs(args []string, out io.Writer) error {
	var paths []string
	for _, name := range []string{"gui.log", "daemon.log", "crash.log"} {
		paths = append(paths, filepath.Join(stateDir(), name))
	}
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
// interval until stop closes, and returns the first read or write error. It
// starts at each file's end; a path that names another file than before was
// rotated or replaced, and the new file is read from its start.
func follow(paths []string, out io.Writer, stop <-chan struct{}, interval time.Duration) error {
	offs := make([]int64, len(paths))
	seen := make([]os.FileInfo, len(paths))
	for i, p := range paths {
		if fi, err := os.Stat(p); err == nil {
			offs[i], seen[i] = fi.Size(), fi
		}
	}
	for {
		for i, p := range paths {
			if err := followOne(p, &offs[i], &seen[i], out); err != nil {
				return err
			}
		}
		select {
		case <-stop:
			return nil
		case <-time.After(interval):
		}
	}
}

func followOne(path string, off *int64, seen *os.FileInfo, out io.Writer) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil // not written yet, or between a rotation's rename and create
	} else if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if *seen == nil || !os.SameFile(*seen, fi) {
		*off = 0
	}
	*seen = fi
	n, err := io.Copy(out, io.NewSectionReader(f, *off, 1<<62))
	*off += n
	return err
}
