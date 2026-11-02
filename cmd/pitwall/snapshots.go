package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/quanticstudios/pitwall/internal/store"
)

// runSnapshots lists the saved state's snapshots, oldest first, or puts one
// back as the saved state. A restore stops the daemon first, as Restart now
// does, since its last save would replace the restored file; the next start
// opens the restored tabs.
func runSnapshots(args []string, out io.Writer) error {
	path := store.Path()
	switch {
	case len(args) == 0:
		names := store.ListSnapshots(path)
		fmt.Fprintln(out, store.SnapshotDir(path))
		if len(names) == 0 {
			fmt.Fprintln(out, "  no snapshots yet")
		}
		for _, n := range names {
			if tabs, err := store.SnapshotTabs(path, n); err != nil {
				fmt.Fprintf(out, "  %s  does not load: %v\n", n, err)
			} else {
				fmt.Fprintf(out, "  %s  %s\n", n, plural(tabs, "tab"))
			}
		}
		return nil
	case len(args) == 2 && args[0] == "restore":
		return restoreSnapshot(path, args[1], out)
	}
	return errors.New("usage: pitwall snapshots [restore <name>]")
}

func restoreSnapshot(path, name string, out io.Writer) error {
	if tabs, err := store.SnapshotTabs(path, name); err != nil {
		return err
	} else if tabs == 0 {
		return fmt.Errorf("%s has no tabs", name)
	}
	sock, err := socketPath()
	if err != nil {
		return err
	}
	lock, err := os.OpenFile(sock+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	ok, err := tryLock(lock)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintln(out, "stopping pitwall's daemon: programs in panes stop, agents resume at the next start")
		if err := stopIncompatibleDaemon(sock); err != nil {
			return err
		}
		// why: a window that lost the daemon starts another; holding the
		// lock until the file is in makes that one exit.
		if ok, err = tryLock(lock); err != nil {
			return err
		} else if !ok {
			return errors.New("a new daemon started meanwhile; run this again")
		}
	}
	if err := store.RestoreSnapshot(path, name); err != nil {
		return err
	}
	fmt.Fprintf(out, "restored %s; the state it replaced is in %s. Run pitwall to open it.\n", name, store.SnapshotDir(path))
	return nil
}
