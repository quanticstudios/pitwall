// Package logs sends the standard logger to a size-capped file in the state
// directory. Lines are events, one per line: never pane contents, typed
// input, prompts, hook payloads, environment variables, command arguments or
// secrets.
package logs

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"
)

// MaxSize is how large a log file grows before it moves to <name>.1.
const MaxSize = 5 << 20

// Start sends the standard logger, and the runtime's crash output, to the
// file at path, and log lines to stderr too. Each line starts with the time,
// role ("gui" or "daemon"), version and pid: windows share gui.log.
func Start(path, role, version string) (*File, error) {
	f, err := Open(path, MaxSize)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.crash = true
	debug.SetCrashOutput(f.f, debug.CrashOptions{})
	f.mu.Unlock()
	log.SetOutput(both{f, os.Stderr})
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lmsgprefix)
	log.SetPrefix(fmt.Sprintf("%s %s %d ", role, version, os.Getpid()))
	return f, nil
}

// both writes to each writer, so a GUI without a console still logs to
// its file.
type both [2]io.Writer

func (w both) Write(p []byte) (int, error) {
	w[0].Write(p)
	w[1].Write(p)
	return len(p), nil
}

// File appends to path, 0600 in a 0700 directory. A write that would take
// the file past max first moves it to path+".1", replacing the old one.
// Processes may share the file: one that finds the file moved reopens it.
type File struct {
	mu    sync.Mutex
	path  string
	max   int64
	f     *os.File
	crash bool // the runtime's crash output follows the file
}

func Open(path string, max int64) (*File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	l := &File{path: path, max: max}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *File) open() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	l.f = f
	if l.crash {
		debug.SetCrashOutput(f, debug.CrashOptions{})
	}
	return nil
}

func (l *File) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		if err := l.open(); err != nil {
			return 0, err
		}
	}
	ours, err := l.f.Stat()
	disk, derr := os.Stat(l.path)
	switch {
	case err == nil && (derr != nil || !os.SameFile(ours, disk)):
		l.reopen(false) // another process moved it
	case derr == nil && disk.Size()+int64(len(p)) > l.max:
		l.reopen(true)
	}
	if l.f == nil {
		return 0, os.ErrClosed
	}
	return l.f.Write(p)
}

// reopen closes the file, moves it to .1 when rotate is set, and opens path
// again. Windows cannot rename a file another handle holds, the crash output's
// included: that one is let go first, and while another window holds the
// file it keeps growing and each write tries again.
func (l *File) reopen(rotate bool) {
	if l.crash {
		debug.SetCrashOutput(nil, debug.CrashOptions{})
	}
	l.f.Close()
	l.f = nil
	if rotate {
		os.Rename(l.path, l.path+".1")
	}
	l.open()
}

func (l *File) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	if l.crash {
		debug.SetCrashOutput(nil, debug.CrashOptions{})
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// Limiter lets one line per key through every Every and counts the lines
// it held back, for events that can repeat many times a second.
// ponytail: keys are never forgotten; a pane id each is a few bytes.
type Limiter struct {
	Every time.Duration
	mu    sync.Mutex
	last  map[string]time.Time
	held  map[string]int
}

// Allow reports whether a line for key may be logged at now, and how many
// were held back since the last one that was.
func (l *Limiter) Allow(key string, now time.Time) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last == nil {
		l.last, l.held = map[string]time.Time{}, map[string]int{}
	}
	if t, ok := l.last[key]; ok && now.Sub(t) < l.Every {
		l.held[key]++
		return false, 0
	}
	held := l.held[key]
	l.last[key], l.held[key] = now, 0
	return true, held
}
