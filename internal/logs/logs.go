// Package logs writes pitwall's event logs, gui.log and daemon.log in the
// state directory. Their lines are events, one per line: never pane
// contents, typed input, prompts, hook payloads, environment variables,
// command arguments or secrets.
//
// crash.log, next to them, is different: it holds raw Go crash traces, with
// the panic value and every goroutine's stack verbatim, and does not follow
// the line format or that list.
package logs

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// MaxSize is the size over which a log file is moved to <name>.1 when a
// process opens it. The cap is checked at startup only: after that a
// process appends for as long as it runs.
const MaxSize = 5 << 20

// queue is how many lines may wait for the writer before new ones are
// dropped.
const queue = 256

// OpenFile opens path for appending, 0600 in a 0700 directory; on Unix an
// existing directory and file are set to those modes. A file over max is
// first moved to path+".1", replacing the old one. When that fails, as it
// does on Windows while another window has the file open, the process
// appends to the file as it is.
func OpenFile(path string, max int64) (*os.File, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	unix := runtime.GOOS != "windows"
	if unix {
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, err
		}
	}
	if fi, err := os.Stat(path); err == nil && fi.Size() > max {
		os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	if unix {
		if err := f.Chmod(0o600); err != nil {
			f.Close()
			return nil, err
		}
	}
	return f, nil
}

// Start sends the standard logger to the file at path, and to stderr when
// stderr is set. Each line starts with the time, role ("gui" or "daemon"),
// version and pid: windows share gui.log. Close the Writer on a clean exit
// to flush it.
func Start(path, role, version string, stderr bool) (*Writer, error) {
	f, err := OpenFile(path, MaxSize)
	if err != nil {
		return nil, err
	}
	outs := []io.Writer{f}
	if stderr {
		outs = append(outs, os.Stderr)
	}
	prefix := fmt.Sprintf("%s %s %d ", role, version, os.Getpid())
	w := NewWriter(prefix, f, outs...)
	log.SetOutput(w)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lmsgprefix)
	log.SetPrefix(prefix)
	return w, nil
}

// Writer hands each line to one goroutine that writes it to every output,
// so a log call never waits on a disk or a stalled stderr. When queue lines
// are waiting, new ones are dropped and counted, and a line saying how many
// is written once there is room.
type Writer struct {
	prefix  string // for the dropped-lines line, as the logger's own lines start
	outs    []io.Writer
	closer  io.Closer
	lines   chan []byte
	dropped atomic.Int64
	done    chan struct{}

	mu     sync.Mutex
	closed bool
}

// NewWriter starts the writing goroutine. closer, when not nil, is closed
// after the last line is written.
func NewWriter(prefix string, closer io.Closer, outs ...io.Writer) *Writer {
	w := &Writer{prefix: prefix, outs: outs, closer: closer, lines: make(chan []byte, queue), done: make(chan struct{})}
	go w.run()
	return w
}

// Write queues a copy of p and never blocks.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		w.dropped.Add(1)
		return len(p), nil
	}
	select {
	case w.lines <- append([]byte(nil), p...):
	default:
		w.dropped.Add(1)
	}
	return len(p), nil
}

func (w *Writer) run() {
	defer close(w.done)
	for p := range w.lines {
		w.noteDropped()
		w.write(p)
	}
	w.noteDropped()
	if w.closer != nil {
		w.closer.Close()
	}
}

func (w *Writer) noteDropped() {
	if n := w.dropped.Swap(0); n > 0 {
		w.write(fmt.Appendf(nil, "%s %s%d log lines dropped\n", time.Now().Format("2006/01/02 15:04:05.000000"), w.prefix, n))
	}
}

func (w *Writer) write(p []byte) {
	for _, o := range w.outs {
		o.Write(p)
	}
}

// Close writes the queued lines, waiting at most timeout for an output
// that has stalled.
func (w *Writer) Close(timeout time.Duration) {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.lines)
	}
	w.mu.Unlock()
	select {
	case <-w.done:
	case <-time.After(timeout):
	}
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
