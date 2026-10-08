package flow

import (
	"bytes"
	"encoding/json"
	"hash/maphash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

// Record is one model call's tokens, as a session file logged it.
type Record struct {
	Time     time.Time
	Provider model.Provider
	Model    string // as Usage.Models keys it: Claude's fast mode adds " (fast)"
	Session  string
	Tokens
	key uint64 // Claude's message.id:requestId, hashed; 0 for none
}

// Source is one agent's session files: every .jsonl under Dir.
type Source struct {
	Provider model.Provider
	Dir      string
}

// Sources are where Claude Code, Codex and pi keep their sessions under
// home. pi's moves with $PI_CODING_AGENT_DIR.
func Sources(home string) []Source {
	pi := os.Getenv("PI_CODING_AGENT_DIR")
	switch {
	case pi == "":
		pi = filepath.Join(home, ".pi", "agent")
	case strings.HasPrefix(pi, "~/"):
		pi = filepath.Join(home, pi[2:])
	}
	return []Source{
		{model.ProviderClaude, filepath.Join(home, ".claude", "projects")},
		{model.ProviderCodex, filepath.Join(home, ".codex", "sessions")},
		{model.ProviderPi, filepath.Join(pi, "sessions")},
	}
}

// mtimeSlack is how long before the window a file's last write may be and
// still be opened: a session can log a call well after its timestamp.
const mtimeSlack = 36 * time.Hour

// Scanner reads the usage records of every session file, and keeps what it
// read by file so the next Scan opens only files that changed and reads
// only what a grown file appended. Scans run one at a time.
type Scanner struct {
	mu    sync.Mutex
	files map[string]*scanned
	read  atomic.Int64 // bytes read, for tests
}

// scanned is one file as far as it was read.
type scanned struct {
	provider model.Provider
	info     os.FileInfo
	off      int64 // just past the last complete line read
	recs     []Record
	codex    codexScan
}

// codexScan carries what a Codex rollout's earlier lines set.
type codexScan struct {
	model, session string
	last           codexInfo // the latest token_count's, to drop a repeat
	meta           bool      // the first session_meta was seen
	fork           time.Time // while not zero, token_counts within a second of it are copied history
}

// Scan returns the calls of every source's files at or after since, each
// call once. Files last written more than mtimeSlack before since are not
// opened.
func (s *Scanner) Scan(srcs []Source, since time.Time) []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	type job struct {
		path string
		src  Source
		info os.FileInfo
	}
	var jobs []job
	walked := map[string]bool{}
	for _, src := range srcs {
		_ = filepath.WalkDir(src.Dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
				return nil
			}
			fi, err := os.Stat(path) // follows a link, as openRegular does
			if err != nil || !fi.Mode().IsRegular() {
				return nil
			}
			walked[path] = true
			if fi.ModTime().Before(since.Add(-mtimeSlack)) {
				return nil
			}
			jobs = append(jobs, job{path, src, fi})
			return nil
		})
	}
	if s.files == nil {
		s.files = map[string]*scanned{}
	}
	for path := range s.files {
		if !walked[path] {
			delete(s.files, path)
		}
	}

	// Files parse on every core: the JSON decode is the cost.
	out := make([]*scanned, len(jobs))
	next := atomic.Int64{}
	var wg sync.WaitGroup
	for range min(len(jobs), runtime.NumCPU()) {
		wg.Go(func() {
			buf := make([]byte, 1<<20)
			for i := int(next.Add(1) - 1); i < len(jobs); i = int(next.Add(1) - 1) {
				out[i] = s.file(jobs[i].path, jobs[i].src.Provider, jobs[i].info, s.files[jobs[i].path], &buf)
			}
		})
	}
	wg.Wait()
	for i, j := range jobs {
		s.files[j.path] = out[i]
	}

	// Adapted from t3code (MIT): apps/server/src/usage/usageTranscripts.ts
	// Claude Code repeats a call in a subagent's file and in a resumed
	// session's, under the same message and request ids: the first one seen
	// counts.
	seen := map[uint64]bool{}
	var recs []Record
	for _, f := range out {
		for _, r := range f.recs {
			if r.Time.Before(since) {
				continue
			}
			if r.key != 0 {
				if seen[r.key] {
					continue
				}
				seen[r.key] = true
			}
			recs = append(recs, r)
		}
	}
	return recs
}

// file brings prev up to date with the file at path: as it is when the
// size and mtime match, read on from prev.off when the same file only grew,
// else read whole. A read error keeps what prev had.
func (s *Scanner) file(path string, p model.Provider, fi os.FileInfo, prev *scanned, buf *[]byte) *scanned {
	if prev != nil && prev.provider == p && os.SameFile(prev.info, fi) && fi.Size() == prev.info.Size() && fi.ModTime().Equal(prev.info.ModTime()) {
		return prev
	}
	f := &scanned{provider: p, info: fi}
	if prev != nil && prev.provider == p && os.SameFile(prev.info, fi) && fi.Size() > prev.info.Size() {
		f.off, f.codex, f.recs = prev.off, prev.codex, prev.recs
	}
	fd, err := os.Open(path)
	if err != nil {
		return orEmpty(prev, f)
	}
	defer fd.Close()
	if _, err := fd.Seek(f.off, io.SeekStart); err != nil {
		return orEmpty(prev, f)
	}
	session := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	err = lines(io.LimitReader(fd, fi.Size()-f.off), buf, func(line []byte, end int64) {
		f.line(line, session)
		f.off += end
	}, &s.read)
	if err != nil {
		return orEmpty(prev, f)
	}
	return f
}

func orEmpty(prev, f *scanned) *scanned {
	if prev != nil {
		return prev
	}
	f.info, f.recs = nil, nil // never matches, so the next Scan tries again
	return f
}

// maxScanLine is the longest line Scan decodes; usage lines are far
// shorter, and a longer one is skipped.
const maxScanLine = 64 << 20

// lines calls fn with every complete line r yields, without its newline,
// and the bytes it took with the newline. A trailing line without one is
// left for the next read.
func lines(r io.Reader, buf *[]byte, fn func(line []byte, n int64), read *atomic.Int64) error {
	b := (*buf)[:0]
	skip := false // inside a line too long to keep
	for {
		if len(b) == cap(b) { // a line longer than the buffer
			if cap(b) >= maxScanLine {
				fn(nil, int64(len(b)))
				b, skip = b[:0], true
			} else {
				b = slices.Grow(b, len(b))
				*buf = b[:0]
			}
		}
		n, err := r.Read(b[len(b):cap(b)])
		read.Add(int64(n))
		b = b[:len(b)+n]
		start := 0
		for {
			i := bytes.IndexByte(b[start:], '\n')
			if i < 0 {
				break
			}
			if skip {
				fn(nil, int64(i+1))
				skip = false
			} else {
				fn(b[start:start+i], int64(i+1))
			}
			start += i + 1
		}
		b = b[:copy(b, b[start:])]
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

var (
	hasUsage      = []byte(`"usage"`)
	hasTokenCount = []byte(`"token_count"`)
	hasContext    = []byte(`"turn_context"`)
	hasMeta       = []byte(`"session_meta"`)
	keySeed       = maphash.MakeSeed()
)

// line adds the call a line logs, if it logs one. A substring test skips
// most lines before any JSON is decoded: those are tool output.
func (f *scanned) line(line []byte, session string) {
	switch f.provider {
	case model.ProviderClaude:
		if bytes.Contains(line, hasUsage) {
			f.claude(line, session)
		}
	case model.ProviderCodex:
		if bytes.Contains(line, hasTokenCount) || bytes.Contains(line, hasContext) || bytes.Contains(line, hasMeta) {
			f.codexLine(line, session)
		}
	default:
		if bytes.Contains(line, hasUsage) {
			f.pi(line, session)
		}
	}
}

type claudeUseLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	RequestID string `json:"requestId"`
	SessionID string `json:"sessionId"`
	Message   struct {
		ID    string       `json:"id"`
		Model string       `json:"model"`
		Usage *claudeUsage `json:"usage"`
	} `json:"message"`
}

// claude adds an assistant entry's call. Claude Code writes each content
// block of a reply as its own entry with the reply's usage so far, so an
// entry right after one of the same reply replaces it.
func (f *scanned) claude(line []byte, file string) {
	var e claudeUseLine
	if json.Unmarshal(line, &e) != nil || e.Type != "assistant" {
		return
	}
	u, m := e.Message.Usage, e.Message.Model
	ts := parseTime(e.Timestamp)
	if u == nil || m == "" || m == "<synthetic>" || ts.IsZero() {
		return
	}
	if u.Speed == "fast" {
		m += " (fast)"
	}
	if e.SessionID == "" {
		e.SessionID = file
	}
	r := Record{Time: ts, Provider: model.ProviderClaude, Model: m, Session: e.SessionID,
		Tokens: Tokens{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, CacheWrite1h: u.CacheCreation.Hour}}
	if e.Message.ID != "" || e.RequestID != "" {
		r.key = maphash.String(keySeed, e.Message.ID+":"+e.RequestID) | 1
	}
	if n := len(f.recs); n > 0 && r.key != 0 && f.recs[n-1].key == r.key {
		f.recs[n-1].Tokens = r.Tokens
		return
	}
	f.add(r)
}

type codexUseLine struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		Type         string          `json:"type"`
		Model        string          `json:"model"`
		Info         *codexInfo      `json:"info"`
		ID           string          `json:"id"`
		ForkedFromID *string         `json:"forked_from_id"`
		Source       json.RawMessage `json:"source"` // "cli", or an object for a subagent
	} `json:"payload"`
}

// spawned reports whether a session_meta's source names a parent thread.
func spawned(source json.RawMessage) bool {
	var s struct {
		Subagent *struct {
			ThreadSpawn *struct {
				Parent *string `json:"parent_thread_id"`
			} `json:"thread_spawn"`
		} `json:"subagent"`
	}
	return json.Unmarshal(source, &s) == nil && s.Subagent != nil && s.Subagent.ThreadSpawn != nil && s.Subagent.ThreadSpawn.Parent != nil
}

// codexLine follows a rollout's session and model and adds a token_count's
// call: its last_token_usage, unless the event repeats the one before.
func (f *scanned) codexLine(line []byte, file string) {
	var e codexUseLine
	if json.Unmarshal(line, &e) != nil {
		return
	}
	c, p, ts := &f.codex, &e.Payload, parseTime(e.Timestamp)
	switch {
	case e.Type == "session_meta":
		if c.meta {
			return // a fork repeats its ancestors' metas after its own
		}
		c.meta, c.session = true, p.ID
		// Adapted from t3code (MIT): apps/server/src/usage/usageTranscripts.ts
		// A forked or subagent rollout starts with its parent's history,
		// token_counts included, all written in one burst at the fork.
		if p.ForkedFromID != nil || spawned(p.Source) {
			c.fork = ts
		}
	case e.Type == "turn_context":
		if p.Model != "" {
			c.model = p.Model
		}
	case p.Type == "token_count":
		if p.Info == nil || c.model == "" || ts.IsZero() || *p.Info == c.last {
			return
		}
		c.last = *p.Info
		if !c.fork.IsZero() {
			if ts.Sub(c.fork) < time.Second {
				c.fork = ts
				return
			}
			c.fork = time.Time{}
		}
		t := p.Info.Last.tokens()
		if t.Input < 0 {
			t.Input = 0
		}
		if t.Total() == 0 {
			return
		}
		session := c.session
		if session == "" {
			session = file
		}
		f.add(Record{Time: ts, Provider: model.ProviderCodex, Model: c.model, Session: session, Tokens: t})
	}
}

// pi adds an assistant message's call.
func (f *scanned) pi(line []byte, file string) {
	var e piEntry
	if json.Unmarshal(line, &e) != nil || e.Type != "message" || e.Message.Role != "assistant" {
		return
	}
	m, u := e.Message, e.Message.Usage
	ts := parseTime(e.Timestamp)
	if ts.IsZero() && m.Timestamp > 0 {
		ts = time.UnixMilli(m.Timestamp)
	}
	if u == nil || m.Model == "" || ts.IsZero() {
		return
	}
	f.add(Record{Time: ts, Provider: model.ProviderPi, Model: m.Model, Session: file,
		Tokens: Tokens{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite}})
}

// add appends r. It shares the model and session strings of the call
// before, which most calls repeat, so a file holds one copy of each.
func (f *scanned) add(r Record) {
	if n := len(f.recs); n > 0 {
		if r.Model == f.recs[n-1].Model {
			r.Model = f.recs[n-1].Model
		}
		if r.Session == f.recs[n-1].Session {
			r.Session = f.recs[n-1].Session
		}
	}
	f.recs = append(f.recs, r)
}
