package eventlog

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/store"
)

// delta is the seq'th delta of a test chat: a snapshot first, then items
// that each get a text append.
func delta(seq int64) session.Delta {
	d := session.Delta{Version: session.DeltaVersion, SessionID: "c1", Seq: seq, Revision: seq, UpdatedAt: "2026-10-30T00:00:00Z"}
	item := "i" + strconv.FormatInt(seq/2, 10)
	switch {
	case seq == 1:
		d.Type = session.DeltaSnapshot
		d.Snapshot = &session.Snapshot{SessionID: "c1", PaneID: "c1", WorkspaceID: "w1", Provider: model.ProviderClaude, SessionState: "ready"}
	case seq%2 == 0:
		d.Type = session.DeltaItemAdd
		d.Item = &session.Item{ID: item, Kind: session.KindAssistantText, Text: "hello ", UpdatedAt: d.UpdatedAt}
	default:
		d.Type = session.DeltaItemTextAppend
		d.ItemID = item
		d.Text = "world " + strconv.FormatInt(seq, 10)
		d.ItemUpdatedAt = d.UpdatedAt
	}
	return d
}

func stream(from, to int64) []session.Delta {
	var ds []session.Delta
	for seq := from; seq <= to; seq++ {
		ds = append(ds, delta(seq))
	}
	return ds
}

// replay is the snapshot after the first n deltas, applied in memory.
func replay(t testing.TB, n int64) session.Snapshot {
	t.Helper()
	var s *session.Snapshot
	for i, d := range stream(1, n) {
		next, err := session.Apply(s, int64(i), d)
		if err != nil {
			t.Fatal(err)
		}
		s = &next
	}
	if s == nil {
		return session.Snapshot{}
	}
	return *s
}

func asJSON(t testing.TB, s session.Snapshot) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func open(t testing.TB, dir string) (*Log, session.Config, session.Snapshot, int64) {
	t.Helper()
	l, cfg, s, seq, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l, cfg, s, seq
}

func check(t testing.TB, dir string, want int64) *Log {
	t.Helper()
	l, _, s, seq := open(t, dir)
	if seq != want {
		t.Fatalf("seq = %d, want %d", seq, want)
	}
	if g, w := asJSON(t, s), asJSON(t, replay(t, want)); g != w {
		t.Fatalf("snapshot after %d deltas:\n got %s\nwant %s", want, g, w)
	}
	return l
}

func TestAppendAndOpen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "c1")
	l, cfg, s, seq := open(t, dir)
	if cfg.ID != "" || s.SessionID != "" || seq != 0 {
		t.Fatalf("new chat: %+v %+v %d", cfg, s, seq)
	}
	want := session.Config{ID: "c1", Provider: model.ProviderClaude, Cwd: "/w", Resume: "r1", Created: time.Unix(1, 0).UTC()}
	if err := l.WriteConfig(want); err != nil {
		t.Fatal(err)
	}
	for _, batch := range [][2]int64{{1, 1}, {2, 7}, {8, 20}} {
		if err := l.Append(stream(batch[0], batch[1])); err != nil {
			t.Fatal(err)
		}
	}
	l.Close()

	_, cfg, _, _ = open(t, dir)
	want.Format = 1
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("config = %+v, want %+v", cfg, want)
	}
	l = check(t, dir, 20)
	if err := l.Append(stream(21, 21)); err != nil {
		t.Fatal(err)
	}
	l.Close()
	check(t, dir, 21)
	for _, name := range []string{".", "config.json", "events.jsonl"} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && fi.Mode().Perm()&0077 != 0 {
			t.Errorf("%s is %v, want no access for group and others", name, fi.Mode())
		}
	}
}

// A crash can cut the last line anywhere. Every cut drops just that line,
// and the next append lands on a clean line.
func TestTornLastLine(t *testing.T) {
	const n = 9
	src := t.TempDir()
	l, _, _, _ := open(t, src)
	if err := l.Append(stream(1, n)); err != nil {
		t.Fatal(err)
	}
	l.Close()
	full, err := os.ReadFile(filepath.Join(src, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	start := len(full) - 1
	for start > 0 && full[start-1] != '\n' {
		start--
	}
	for cut := start; cut < len(full); cut++ {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), full[:cut], 0600); err != nil {
			t.Fatal(err)
		}
		l := check(t, dir, n-1)
		if fi, _ := os.Stat(filepath.Join(dir, "events.jsonl")); fi.Size() != int64(start) {
			t.Fatalf("cut %d: file is %d bytes, want %d", cut, fi.Size(), start)
		}
		if err := l.Append(stream(n, n)); err != nil {
			t.Fatal(err)
		}
		l.Close()
		check(t, dir, n).Close()
		if bad, _ := filepath.Glob(filepath.Join(dir, "*.bad-*")); len(bad) != 0 {
			t.Fatalf("cut %d: a torn line is not bad lines: %v", cut, bad)
		}
	}
}

// A full line that does not apply stops the replay; what came before stays,
// the rest moves aside, and appends go on from the last good seq.
func TestBadLineStops(t *testing.T) {
	for name, line := range map[string]string{
		"garbage": "{not json\n",
		"seq gap": `{"version":1,"sessionId":"c1","seq":9,"type":"item.text.append","itemId":"i2","text":"x"}` + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			l, _, _, _ := open(t, dir)
			if err := l.Append(stream(1, 4)); err != nil {
				t.Fatal(err)
			}
			l.Close()
			f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			f.WriteString(line)
			f.Close()

			l = check(t, dir, 4)
			if err := l.Append(stream(5, 6)); err != nil {
				t.Fatal(err)
			}
			l.Close()
			check(t, dir, 6)
			bad, _ := filepath.Glob(filepath.Join(dir, "events.jsonl.bad-*"))
			if len(bad) != 1 {
				t.Fatalf("bad files: %v", bad)
			}
			if b, _ := os.ReadFile(bad[0]); string(b) != line {
				t.Fatalf("set aside %q, want %q", b, line)
			}
		})
	}
}

func TestCompact(t *testing.T) {
	dir := t.TempDir()
	l, _, _, _ := open(t, dir)
	if err := l.Append(stream(1, 10)); err != nil {
		t.Fatal(err)
	}
	if err := l.Compact(replay(t, 9), 9); err == nil {
		t.Fatal("compacted below the last appended seq")
	}
	if err := l.Compact(replay(t, 10), 10); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "events.jsonl")); fi.Size() != 0 {
		t.Fatalf("events.jsonl is %d bytes after compact", fi.Size())
	}
	if err := l.Append(stream(11, 15)); err != nil {
		t.Fatal(err)
	}
	l.Close()
	check(t, dir, 15).Close()

	// A crash between snapshot.json and the truncate leaves lines the
	// snapshot already holds.
	full := t.TempDir()
	l, _, _, _ = open(t, full)
	if err := l.Append(stream(1, 15)); err != nil {
		t.Fatal(err)
	}
	l.Close()
	b, _ := os.ReadFile(filepath.Join(dir, "snapshot.json"))
	os.WriteFile(filepath.Join(full, "snapshot.json"), b, 0600)
	check(t, full, 15).Close()
}

func TestDurable(t *testing.T) {
	turn := func(state string) session.Delta {
		return session.Delta{Type: session.DeltaTurn, Turn: &session.TurnSummary{State: state}}
	}
	for _, c := range []struct {
		d    session.Delta
		want bool
	}{
		{turn(session.TurnRunning), false},
		{turn(session.TurnCompleted), true},
		{turn(session.TurnInterrupted), true},
		{session.Delta{Type: session.DeltaApprovalResolve}, true},
		{session.Delta{Type: session.DeltaInputResolve}, true},
		{session.Delta{Type: session.DeltaApprovalAdd}, false},
		{delta(2), false},
	} {
		if got := durable(c.d); got != c.want {
			t.Errorf("durable(%s %+v) = %v, want %v", c.d.Type, c.d.Turn, got, c.want)
		}
	}
}

func TestDue(t *testing.T) {
	l, _, _, _ := open(t, t.TempDir())
	if err := l.Append(stream(1, dueLines-1)); err != nil {
		t.Fatal(err)
	}
	if l.Due() {
		t.Fatal("due before 1,000 deltas")
	}
	if err := l.Append(stream(dueLines, dueLines)); err != nil {
		t.Fatal(err)
	}
	if !l.Due() {
		t.Fatal("not due at 1,000 deltas")
	}
	if err := l.Compact(session.Snapshot{SessionID: "c1"}, dueLines); err != nil {
		t.Fatal(err)
	}
	if l.Due() {
		t.Fatal("due after compact")
	}
	l.size = dueBytes
	if !l.Due() {
		t.Fatal("not due at 8 MB")
	}
}

func TestNewerFormat(t *testing.T) {
	for _, name := range []string{"config.json", "snapshot.json"} {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, name), []byte(`{"format":2}`), 0600)
		if _, _, _, _, err := Open(dir); !errors.Is(err, store.ErrNewer) {
			t.Errorf("%s format 2: %v, want ErrNewer", name, err)
		}
	}
}

func TestArchiveAndDelete(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "c1")
	l, _, _, _ := open(t, live)
	if err := l.Append(stream(1, 3)); err != nil {
		t.Fatal(err)
	}
	l.Close()

	if err := Delete(root, "c1"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("delete of a live chat: %v", err)
	}
	if err := Archive(root, "c1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(live); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("live folder after archive: %v", err)
	}
	check(t, filepath.Join(root, "archive", "c1"), 3).Close()

	os.MkdirAll(live, 0700)
	if err := Unarchive(root, "c1"); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("unarchive over a live chat: %v", err)
	}
	os.Remove(live)
	if err := Unarchive(root, "c1"); err != nil {
		t.Fatal(err)
	}
	check(t, live, 3).Close()

	if err := Archive(root, "c1"); err != nil {
		t.Fatal(err)
	}
	if err := Delete(root, "c1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "archive", "c1")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("archived folder after delete: %v", err)
	}
	if err := Archive(root, "c1"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("archive of a deleted chat: %v", err)
	}

	for _, id := range []string{"", ".", "..", "archive", "../c1", "a/b", "/abs"} {
		if Archive(root, id) == nil || Unarchive(root, id) == nil || Delete(root, id) == nil {
			t.Errorf("id %q accepted", id)
		}
	}
}

// A chat of 50,000 deltas loads from its snapshot within the budget. Every
// other delta adding an item is the worst case for the snapshot's size.
func TestLoadTime(t *testing.T) {
	s := session.Snapshot{SessionID: "c1", PaneID: "c1", WorkspaceID: "w1", Provider: model.ProviderClaude, SessionState: "ready"}
	for i := range 25_000 {
		s.Content = append(s.Content, session.Item{ID: "i" + strconv.Itoa(i), Kind: session.KindAssistantText, Text: fmt.Sprintf("hello world %d, a line of about the length an agent writes", i), UpdatedAt: "2026-10-30T00:00:00Z"})
	}
	dir := t.TempDir()
	l, _, _, _ := open(t, dir)
	if err := l.Compact(s, 50_000); err != nil {
		t.Fatal(err)
	}
	l.Close()

	start := time.Now()
	l, _, got, seq, err := Open(dir)
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	if seq != 50_000 || len(got.Content) != 25_000 {
		t.Fatalf("seq %d, %d items", seq, len(got.Content))
	}
	t.Logf("loaded in %v", took)
	if budget := 200 * time.Millisecond; took > budget && !raceEnabled {
		t.Fatalf("loaded in %v, budget %v", took, budget)
	}
}

// The writer is killed (SIGKILL on Unix) without a Sync or Close, most
// likely while it appends; the reload has everything up to the last full
// line.
func TestKilledWriter(t *testing.T) {
	dir := t.TempDir()
	c := exec.Command(os.Args[0], "-test.run=^TestHelperWriter$")
	c.Env = append(os.Environ(), "EVENTLOG_HELPER_DIR="+dir)
	out, err := c.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	var written int64
	sc := bufio.NewScanner(out)
	for written < 300 && sc.Scan() {
		written, _ = strconv.ParseInt(sc.Text(), 10, 64)
	}
	c.Process.Kill()
	c.Wait()
	if written < 300 {
		t.Fatalf("the writer stopped at seq %d", written)
	}

	l, _, s, seq := open(t, dir)
	if seq < written {
		t.Fatalf("reloaded seq %d, the writer had written %d", seq, written)
	}
	if asJSON(t, s) != asJSON(t, replay(t, seq)) {
		t.Fatalf("snapshot at seq %d differs from the replay", seq)
	}
	if err := l.Append(stream(seq+1, seq+1)); err != nil {
		t.Fatal(err)
	}
}

func TestHelperWriter(t *testing.T) {
	dir := os.Getenv("EVENTLOG_HELPER_DIR")
	if dir == "" {
		t.Skip("run by TestKilledWriter")
	}
	l, _, _, _, err := Open(dir)
	if err != nil {
		os.Exit(3)
	}
	for seq := int64(1); seq < 3000; seq += 3 {
		if err := l.Append(stream(seq, seq+2)); err != nil {
			os.Exit(4)
		}
		fmt.Println(seq + 2)
	}
	time.Sleep(time.Hour) // until killed; the cap keeps the parent's replay small
}
