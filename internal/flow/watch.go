package flow

import (
	"bytes"
	"context"
	"io"
	"os"
	"reflect"
	"slices"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

// maxRead is how much of a file's end the first read takes.
const maxRead = 8 << 20

// pollEvery is how often Watch looks at the files; tests shorten it.
var pollEvery = 500 * time.Millisecond

func watch(ctx context.Context, provider model.Provider, path string, changed func(Feed)) {
	s := newSession(provider, path, false)
	var last Feed
	first := true
	tick := time.NewTicker(pollEvery)
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

func newSession(provider model.Provider, path string, child bool) *session {
	s := &session{provider: provider, path: path, child: child, t: tail{path: path}}
	s.reset()
	return s
}

func (s *session) reset() {
	s.b = newBuilder(s.provider)
	switch s.provider {
	case model.ProviderClaude:
		s.p = &claude{path: s.path, child: s.child}
	case model.ProviderCodex:
		s.p = &codex{path: s.path}
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
	changed := reset || len(lines) > 0
	if s.child {
		return changed
	}
	// A subagent's file may appear after the line that spawned it.
	if slices.ContainsFunc(s.b.subs, func(c *sub) bool { return c.file == "" && (changed || c.Running()) }) {
		s.p.resolve(s.b)
	}
	for _, c := range s.b.subs {
		if c.file == "" || c.done {
			continue
		}
		if c.kid == nil {
			c.kid = newSession(s.provider, c.file, true)
		}
		if c.kid.poll() {
			changed = true
		}
		c.done = !c.view().Running()
	}
	return changed
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
}

// read returns the complete lines appended since the last read. The first
// read takes at most the last maxRead bytes, from the first line that starts
// in them. reset is true when the file was replaced, truncated or removed,
// and the lines start over from the new file.
func (t *tail) read() (lines [][]byte, reset bool) {
	fi, err := os.Stat(t.path)
	if t.info != nil && (err != nil || !os.SameFile(t.info, fi) || fi.Size() < t.off) {
		t.info, t.off, t.part, reset = nil, 0, nil, true
	}
	if err != nil || t.info != nil && fi.Size() == t.off && fi.ModTime().Equal(t.info.ModTime()) {
		return nil, reset
	}
	f, err := os.Open(t.path)
	if err != nil {
		return nil, reset
	}
	defer f.Close()
	start, skip := t.off, false
	if t.info == nil && fi.Size() > maxRead {
		start, skip = fi.Size()-maxRead, true
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, reset
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, reset
	}
	t.info, t.off = fi, start+int64(len(data))
	if skip {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			return nil, reset
		}
		data = data[i+1:]
	}
	data = append(t.part, data...)
	end := bytes.LastIndexByte(data, '\n')
	t.part = bytes.Clone(data[end+1:])
	for l := range bytes.SplitSeq(data[:end+1], []byte("\n")) {
		if len(bytes.TrimSpace(l)) > 0 {
			lines = append(lines, l)
		}
	}
	return lines, reset
}
