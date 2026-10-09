package flow

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"slices"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

// maxRead is how much of a file's end the first read takes, and the most
// any one read takes.
const maxRead = 8 << 20

// maxLine is the longest partial line read holds; a longer one is dropped.
const maxLine = 4 << 20

// pollEvery is how often Watch looks at the files; tests shorten it.
var pollEvery = 500 * time.Millisecond

// lookFor is how long after a subagent ended its file is still looked for.
var lookFor = 30 * time.Second

func watch(ctx context.Context, provider model.Provider, path string, changed func(Feed), every, look time.Duration) {
	s := newSession(provider, path, false, time.Time{})
	s.look = look
	var last Feed
	first := true
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		if s.poll() || first {
			if f := s.b.feed(); first || !reflect.DeepEqual(f, last) {
				first, last = false, f
				changed(s.b.feed())
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// session is one session file being read: the main one or a subagent's.
type session struct {
	provider model.Provider
	path     string
	child    bool
	since    time.Time     // a subagent's spawn
	look     time.Duration // lookFor, read once by Watch
	t        tail
	b        *builder
	p        parser
}

// parser turns one provider's lines into builder calls.
type parser interface {
	line(b *builder, line []byte)
	// resolve finds the files of subagents whose file is not yet known.
	resolve(b *builder)
}

func newSession(provider model.Provider, path string, child bool, since time.Time) *session {
	s := &session{provider: provider, path: path, child: child, since: since, t: tail{path: path}}
	s.reset()
	return s
}

func (s *session) reset() {
	s.b = newBuilder(s.provider)
	s.b.since = s.since
	switch s.provider {
	case model.ProviderClaude:
		s.p = &claude{path: s.path, child: s.child}
	case model.ProviderCodex:
		s.p = &codex{path: s.path}
	case model.ProviderGemini:
		s.p = &gemini{}
	default:
		s.p = pi{}
	}
}

// poll reads what was appended to the session's file and its running
// subagents' files, and reports whether anything was.
func (s *session) poll() bool {
	lines, reset := s.t.read()
	if reset {
		s.reset()
	}
	for _, l := range lines {
		s.parse(l)
	}
	s.b.usage.Partial = s.t.cut && s.provider != model.ProviderCodex
	changed := reset || len(lines) > 0
	if s.child {
		return changed
	}
	// A subagent's file may appear after the line that spawned it, or even
	// after its end, so it is looked for until lookFor after the end.
	now := time.Now()
	if slices.ContainsFunc(s.b.subs, func(c *sub) bool { return c.file == "" && !c.late(now, s.look) }) {
		s.p.resolve(s.b)
	}
	for _, c := range s.b.subs {
		if c.file == "" || c.done {
			continue
		}
		if c.kid == nil {
			c.kid = newSession(s.provider, c.file, true, c.Start)
		}
		if c.kid.poll() {
			changed = true
		}
		// An ended subagent's file is read once more, or not at all once
		// the look ends.
		c.done = !c.view().Running() && (c.kid.t.info != nil || c.late(now, s.look))
	}
	return changed
}

// late reports whether the subagent ended more than look ago, counted from
// the poll that first saw it ended.
func (c *sub) late(now time.Time, look time.Duration) bool {
	if c.view().Running() {
		c.ended = time.Time{}
		return false
	}
	if c.ended.IsZero() {
		c.ended = now
	}
	return now.Sub(c.ended) > look
}

func (s *session) parse(line []byte) {
	// why: these files come from other programs; a line that trips a parser
	// bug costs that line, not the window.
	defer func() { _ = recover() }()
	s.p.line(s.b, line)
}

// tail reads a growing file from where it last stopped.
type tail struct {
	path string
	info os.FileInfo // nil before the first read and after a reset
	off  int64
	part []byte // a last line without its newline yet
	skip bool   // drop bytes up to the next newline
	cut  bool   // the first read started past the file's start
}

// read returns the complete lines appended since the last read, at most
// maxRead bytes of them. The first read starts maxRead bytes before the end,
// at the first line that starts there. A partial line longer than maxLine is
// dropped up to its newline. reset is true when the file was replaced,
// truncated, removed, or can no longer be opened as a regular file: what was
// read before is void, and the lines start over.
func (t *tail) read() (lines [][]byte, reset bool) {
	if fi, err := os.Stat(t.path); err == nil && t.info != nil && os.SameFile(t.info, fi) &&
		fi.Size() == t.off && fi.ModTime().Equal(t.info.ModTime()) {
		return nil, false
	}
	f, fi, err := openRegular(t.path)
	if err != nil {
		return nil, t.forget()
	}
	defer f.Close()
	if t.info != nil && (!os.SameFile(t.info, fi) || fi.Size() < t.off) {
		reset = t.forget()
	}
	start := t.off
	if t.info == nil && fi.Size() > maxRead {
		start, t.skip, t.cut = fi.Size()-maxRead, true, true
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, t.forget() || reset
	}
	data, err := io.ReadAll(io.LimitReader(f, maxRead))
	if err != nil {
		return nil, t.forget() || reset
	}
	t.info, t.off = fi, start+int64(len(data))
	if t.skip {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			return nil, reset
		}
		data, t.skip = data[i+1:], false
	}
	data = append(t.part, data...)
	end := bytes.LastIndexByte(data, '\n')
	t.part = bytes.Clone(data[end+1:])
	if len(t.part) > maxLine {
		t.part, t.skip = nil, true
	}
	for l := range bytes.SplitSeq(data[:end+1], []byte("\n")) {
		if len(bytes.TrimSpace(l)) > 0 {
			lines = append(lines, l)
		}
	}
	return lines, reset
}

// forget drops what t read and reports whether it had read anything.
func (t *tail) forget() bool {
	had := t.info != nil
	*t = tail{path: t.path}
	return had
}

var errNotRegular = errors.New("not a regular file")

// openRegular opens path for reading if it is, or links to, a regular file.
// A FIFO or a device would block the open or never end.
func openRegular(path string) (*os.File, os.FileInfo, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, nil, errNotRegular
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	return f, fi, nil
}
