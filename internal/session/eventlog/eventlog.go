// Package eventlog keeps managed sessions (chats) on disk: per chat a
// config.json, a snapshot.json and an append-only events.jsonl of deltas
// since that snapshot. docs/managed-sessions.md (Storage) is the design.
package eventlog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/store"
)

const (
	format = 1 // of config.json and snapshot.json

	// Compact when the log passes either, so it never grows without bound.
	dueLines = 1000
	dueBytes = 8 << 20
)

// Root is the folder that holds every chat: chats/ in config.StateDir.
func Root() string { return filepath.Join(config.StateDir(), "chats") }

// Log is one chat's open log. It is not safe for concurrent use: the chat's
// loop goroutine is the only writer of its files.
type Log struct {
	dir   string
	f     *os.File // events.jsonl
	seq   int64    // of the last delta written or compacted
	lines int      // in events.jsonl
	size  int64    // of events.jsonl
}

type snapshotFile struct {
	Format   int             `json:"format"`
	Seq      int64           `json:"seq"`
	Snapshot json.RawMessage `json:"snapshot"`
}

// Open loads the chat in dir: snapshot.json, then each events.jsonl line
// with a seq above the snapshot's, applied in order. A torn last line is
// dropped and the file truncated after the last full line, so the next
// append starts clean. A seq gap or an Apply error stops the replay there,
// keeps what applied, and logs it; the lines from there on move to
// events.jsonl.bad-<time> and the log continues from what applied.
//
// A dir with no files is a new chat: zero Config and Snapshot, seq 0. A
// config.json or snapshot.json a newer pitwall wrote is store.ErrNewer.
func Open(dir string) (*Log, session.Config, session.Snapshot, int64, error) {
	fail := func(err error) (*Log, session.Config, session.Snapshot, int64, error) {
		return nil, session.Config{}, session.Snapshot{}, 0, err
	}
	var cfg session.Config
	if _, err := readJSON(filepath.Join(dir, "config.json"), &cfg); err != nil {
		return fail(err)
	}
	if cfg.Format > format {
		return fail(fmt.Errorf("%s: %w", filepath.Join(dir, "config.json"), store.ErrNewer))
	}

	var base *session.Snapshot
	var sf snapshotFile
	found, err := readJSON(filepath.Join(dir, "snapshot.json"), &sf)
	if err != nil {
		return fail(err)
	}
	if sf.Format > format {
		return fail(fmt.Errorf("%s: %w", filepath.Join(dir, "snapshot.json"), store.ErrNewer))
	}
	if found {
		base = new(session.Snapshot)
		if err := json.Unmarshal(sf.Snapshot, base); err != nil {
			return fail(fmt.Errorf("%s: %w", filepath.Join(dir, "snapshot.json"), err))
		}
	}

	if err := os.MkdirAll(dir, 0700); err != nil {
		return fail(err)
	}
	path := filepath.Join(dir, "events.jsonl")
	// Not O_APPEND: Windows refuses to truncate a file opened for append only.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return fail(err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		f.Close()
		return fail(err)
	}

	l := &Log{dir: dir, f: f, seq: sf.Seq}
	for int(l.size) < len(data) {
		rest := data[l.size:]
		end := bytes.IndexByte(rest, '\n')
		if end < 0 {
			log.Printf("eventlog: %s: dropped a torn last line of %d bytes", path, len(rest))
			break
		}
		var d session.Delta
		if err := json.Unmarshal(rest[:end], &d); err != nil {
			log.Printf("eventlog: %s: stopped at line %d: %v", path, l.lines+1, err)
			break
		}
		if d.Seq > l.seq { // at or below: already in snapshot.json (a crash inside Compact)
			s, err := session.Apply(base, l.seq, d)
			if err != nil {
				log.Printf("eventlog: %s: stopped at line %d: %v", path, l.lines+1, err)
				break
			}
			base, l.seq = &s, d.Seq
		}
		l.size += int64(end) + 1
		l.lines++
	}
	if rest := data[l.size:]; len(rest) > 0 {
		if bytes.IndexByte(rest, '\n') >= 0 { // full lines that did not apply: keep them for a look
			bad := path + ".bad-" + strconv.FormatInt(time.Now().UnixNano(), 10)
			if err := store.WriteAtomic(bad, rest); err != nil {
				f.Close()
				return fail(fmt.Errorf("keep the lines that did not apply: %w", err))
			}
			log.Printf("eventlog: %s: moved %d bytes that did not apply to %s", path, len(rest), bad)
		}
		if err := f.Truncate(l.size); err != nil {
			f.Close()
			return fail(err)
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return fail(err)
		}
	}
	var s session.Snapshot
	if base != nil {
		s = *base
	}
	return l, cfg, s, l.seq, nil
}

// ReadConfig reads dir's config.json alone, which is all the daemon reads
// of a chat at start. A missing file is fs.ErrNotExist, one a newer pitwall
// wrote store.ErrNewer.
func ReadConfig(dir string) (session.Config, error) {
	var cfg session.Config
	path := filepath.Join(dir, "config.json")
	found, err := readJSON(path, &cfg)
	switch {
	case err != nil:
		return cfg, err
	case !found:
		return cfg, fmt.Errorf("%s: %w", path, fs.ErrNotExist)
	case cfg.Format > format:
		return cfg, fmt.Errorf("%s: %w", path, store.ErrNewer)
	}
	return cfg, nil
}

// readJSON decodes path into v and reports whether the file exists.
func readJSON(path string, v any) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return true, fmt.Errorf("%s: %w", path, err)
	}
	return true, nil
}

// Append writes ds, one line each, in one write. It syncs when a delta ends
// a turn or resolves an approval or input, so those survive a power cut.
func (l *Log) Append(ds []session.Delta) error {
	var buf bytes.Buffer
	sync := false
	for _, d := range ds {
		b, err := json.Marshal(d)
		if err != nil {
			return err
		}
		buf.Write(b)
		buf.WriteByte('\n')
		sync = sync || durable(d)
	}
	if _, err := l.f.WriteAt(buf.Bytes(), l.size); err != nil {
		l.f.Truncate(l.size) // a partial write must not join the next line
		return err
	}
	l.size += int64(buf.Len())
	l.lines += len(ds)
	if len(ds) > 0 {
		l.seq = ds[len(ds)-1].Seq
	}
	if sync {
		return l.Sync()
	}
	return nil
}

// durable reports whether d ends a turn or resolves an approval or input.
func durable(d session.Delta) bool {
	return d.Type == session.DeltaApprovalResolve || d.Type == session.DeltaInputResolve ||
		d.Type == session.DeltaTurn && d.Turn != nil && d.Turn.State != session.TurnRunning
}

func (l *Log) Sync() error { return l.f.Sync() }

// Due reports whether the log passed 1,000 deltas or 8 MB since the last
// Compact; the chat loop compacts then.
func (l *Log) Due() bool { return l.lines >= dueLines || l.size >= dueBytes }

// Compact writes snapshot.json at seq, then truncates events.jsonl. A crash
// between the two is safe: Open skips lines at or below the snapshot's seq.
// s must hold every delta appended so far.
func (l *Log) Compact(s session.Snapshot, seq int64) error {
	if seq < l.seq {
		return fmt.Errorf("eventlog: compact at seq %d would drop deltas up to %d", seq, l.seq)
	}
	snap, err := json.Marshal(s)
	if err != nil {
		return err
	}
	b, err := json.Marshal(snapshotFile{format, seq, snap})
	if err != nil {
		return err
	}
	if err := store.WriteAtomic(filepath.Join(l.dir, "snapshot.json"), append(b, '\n')); err != nil {
		return err
	}
	if err := l.f.Truncate(0); err != nil {
		return err
	}
	l.seq, l.lines, l.size = seq, 0, 0
	return l.Sync()
}

// WriteConfig writes config.json atomically, as format 1.
func (l *Log) WriteConfig(c session.Config) error {
	c.Format = format
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return store.WriteAtomic(filepath.Join(l.dir, "config.json"), append(b, '\n'))
}

func (l *Log) Close() error { return l.f.Close() }

// Archive moves chats/<id> to chats/archive/<id>. Close the chat's Log
// first: Windows cannot move a folder with an open file in it.
func Archive(root, id string) error {
	return move(id, filepath.Join(root, id), filepath.Join(root, "archive", id))
}

// Unarchive moves chats/archive/<id> back to chats/<id>.
func Unarchive(root, id string) error {
	return move(id, filepath.Join(root, "archive", id), filepath.Join(root, id))
}

// Delete removes an archived chat's folder. A live chat is fs.ErrNotExist.
func Delete(root, id string) error {
	if err := checkID(id); err != nil {
		return err
	}
	dir := filepath.Join(root, "archive", id)
	if _, err := os.Lstat(dir); err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func move(id, from, to string) error {
	if err := checkID(id); err != nil {
		return err
	}
	if _, err := os.Lstat(from); err != nil {
		return err
	}
	if _, err := os.Lstat(to); err == nil {
		return fmt.Errorf("eventlog: %s: %w", to, fs.ErrExist)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0700); err != nil {
		return err
	}
	return os.Rename(from, to)
}

// checkID refuses an id that is not one plain folder name under root.
func checkID(id string) error {
	if id == "archive" || id == "." || !filepath.IsLocal(id) || filepath.Base(id) != id {
		return fmt.Errorf("eventlog: bad chat id %q", id)
	}
	return nil
}
