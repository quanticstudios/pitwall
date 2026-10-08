package daemon

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// portBlock is the lowest block of step ports at base + n×step, n from 1,
// that overlaps no tab's Ports, so the main checkout keeps base. It is none
// when step is 0 or no free block ends below 65536.
func portBlock(ws []model.Workspace, base, step int) model.PortBlock {
	if base <= 0 || step <= 0 {
		return model.PortBlock{}
	}
	for lo := base + step; lo+step-1 <= 65535; lo += step {
		hi := lo + step - 1
		taken := slices.ContainsFunc(ws, func(w model.Workspace) bool {
			return w.Ports.First != 0 && w.Ports.First <= hi && lo <= w.Ports.Last
		})
		if !taken {
			return model.PortBlock{First: lo, Last: hi}
		}
	}
	return model.PortBlock{}
}

// portEnv is what a pane in a tab with ports b adds to its environment.
func portEnv(b model.PortBlock) []string {
	if b.First == 0 {
		return nil
	}
	first := strconv.Itoa(b.First)
	return []string{"PORT=" + first, "PITWALL_PORT_BASE=" + first, "PITWALL_PORTS=" + b.String()}
}

// portsOf is the Ports of tab id, none for a missing tab. Callers hold d.mu.
func (d *Daemon) portsOf(id string) model.PortBlock {
	if w := d.workspace(id); w != nil {
		return w.Ports
	}
	return model.PortBlock{}
}

// prepareWorktree copies s.Copy and links s.Link from the main checkout
// into the new worktree tree. It never overwrites: a path the worktree
// already has, or the main checkout lacks, is skipped.
func prepareWorktree(main, tree string, s config.WorktreeSettings) error {
	var errs []error
	each := func(paths []string, do func(src, dst string, fi os.FileInfo) error) {
		for _, rel := range paths {
			src, dst := filepath.Join(main, rel), filepath.Join(tree, rel)
			if _, err := os.Lstat(dst); err == nil {
				continue
			}
			fi, err := os.Stat(src)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err == nil {
				err = os.MkdirAll(filepath.Dir(dst), 0o755)
			}
			if err == nil {
				err = do(src, dst, fi)
			}
			if err != nil {
				errs = append(errs, err)
			}
		}
	}
	each(s.Copy, copyFile)
	each(s.Link, func(src, dst string, _ os.FileInfo) error { return os.Symlink(src, dst) })
	return errors.Join(errs...)
}

// setupInput is what to type into a new worktree tab's shell for s.Setup:
// the command and Enter when the user's own config set it. One from the
// repo's file is typed without Enter, for the user to read and run, since
// cloning a repo must not be enough to run its code; a control character
// in it, which could submit or edit the line, refuses it.
func setupInput(s config.WorktreeSettings) ([]byte, error) {
	switch {
	case s.Setup == "":
		return nil, nil
	case !s.SetupFromRepo:
		return []byte(s.Setup + "\r"), nil
	case strings.ContainsFunc(s.Setup, unicode.IsControl):
		return nil, fmt.Errorf("%s: setup has a control character; not typed", config.WorktreeFile)
	}
	return []byte(s.Setup), nil
}

// copyFile copies src to dst, which must not exist, with src's permissions,
// so a 0600 .env stays private.
func copyFile(src, dst string, fi os.FileInfo) error {
	if fi.IsDir() {
		return fmt.Errorf("copy %s: a folder; link it instead", src)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fi.Mode().Perm())
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	return errors.Join(err, f.Close())
}

// inUse is what programs print when the port they bind is taken: Node's
// EADDRINUSE, and libc's "Address already in use", either case of the A.
var inUse = [][]byte{[]byte("EADDRINUSE"), []byte("ddress already in use")}

// portWatch passes a pane's output on to its emulator and calls hit when a
// program says its port is taken.
type portWatch struct {
	vt.Emulator
	buf []byte // the end of the output so far, for a message split across reads
	hit func()
}

func (w *portWatch) Write(p []byte) (int, error) {
	const keep = len("ddress already in use") - 1
	w.buf = append(w.buf, p...)
	if slices.ContainsFunc(inUse, func(s []byte) bool { return bytes.Contains(w.buf, s) }) {
		w.hit()
		w.buf = w.buf[:0] // a match kept in the tail would fire again
	}
	w.buf = w.buf[:copy(w.buf, w.buf[max(0, len(w.buf)-keep):])]
	return w.Emulator.Write(p)
}

// SetDirtyFunc and Close forward what package pane asks of the emulator.
func (w *portWatch) SetDirtyFunc(f func()) {
	if s, ok := w.Emulator.(interface{ SetDirtyFunc(func()) }); ok {
		s.SetDirtyFunc(f)
	}
}

func (w *portWatch) Close() error {
	if c, ok := w.Emulator.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// portInUse shows that a program in pane id found its port taken, with
// the tab's own ports. Agent panes are left alone: agents print the
// message whenever they read or write about it.
func (d *Daemon) portInUse(id string) {
	d.mu.Lock()
	var ports model.PortBlock
	if i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id }); i >= 0 && d.st.Panes[i].Provider == "" {
		ports = d.portsOf(d.st.Panes[i].WorkspaceID)
	}
	d.mu.Unlock()
	if ports.First != 0 {
		d.notice(id, vt.Notification{Body: "Port in use: this worktree's ports are " + ports.String()})
	}
}
