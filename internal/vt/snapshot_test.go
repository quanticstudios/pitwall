package vt

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func restored(t *testing.T, e Emulator) Emulator {
	t.Helper()
	data, err := Save(e)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Restore(data, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = out.(*emulator).Close() })
	return out
}

func sameFrame(t *testing.T, a, b Emulator) {
	t.Helper()
	if a.Modes() != b.Modes() || a.ScrollbackLen() != b.ScrollbackLen() || a.ScrollbackPushed() != b.ScrollbackPushed() {
		t.Fatal("modes or history counters differ")
	}
	ah, bh := &a.(*emulator).st.hist, &b.(*emulator).st.hist
	if ah.len() != bh.len() {
		t.Fatal("hidden primary history length differs")
	}
	for i := range ah.len() {
		if !reflect.DeepEqual(ah.at(i), bh.at(i)) {
			t.Fatalf("history line %d differs", i)
		}
	}
	for _, off := range []int{0, 1, a.ScrollbackLen() / 2, a.ScrollbackLen()} {
		x, _ := json.Marshal(a.SnapshotAt(off))
		y, _ := json.Marshal(b.SnapshotAt(off))
		if !bytes.Equal(x, y) {
			t.Fatalf("frame bytes differ at scroll offset %d", off)
		}
	}
	if a.(*emulator).e.Render() != b.(*emulator).e.Render() {
		t.Fatal("backend ANSI-rendered frame bytes differ")
	}
}

func TestHandoffRecordings(t *testing.T) {
	for _, name := range []string{"vim", "htop", "claude", "log"} {
		t.Run(name, func(t *testing.T) {
			p, err := os.ReadFile(filepath.Join("testdata", "handoff", name+".raw"))
			if err != nil {
				t.Fatal(err)
			}
			if len(p) == 0 {
				t.Fatal("empty PTY recording")
			}
			a := New(80, 24, nil)
			defer a.(*emulator).Close()
			b := restored(t, a)
			// Break real PTY output inside UTF-8, CSI and OSC sequences. Compare
			// every frame, restore repeatedly, then continue parsing the recording.
			for len(p) > 0 {
				n := min(257, len(p))
				_, _ = a.Write(p[:n])
				_, _ = b.Write(p[:n])
				sameFrame(t, a, b)
				old := b
				b = restored(t, b)
				_ = old.(*emulator).Close()
				sameFrame(t, a, b)
				p = p[n:]
			}
			// Return to primary screen and force a reflow, exposing hidden state.
			for _, e := range []Emulator{a, b} {
				_, _ = e.Write([]byte("\x1b[?1049l\x1b[?1047l\x1b8\r\ncontinued 中\r\n"))
				e.Resize(53, 19)
			}
			sameFrame(t, a, b)
		})
	}
}

func TestHandoffEveryStreamBoundary(t *testing.T) {
	stream := "abc\x1b[31;4:3m中\x1b]2;title ✳\x1b\\" +
		"\x1b]8;id=fixture;https://example.test\x1b\\link\x1b]8;;\x1b\\" +
		"\x1bP$qm\x1b\\\x1b[2\n;3H!\x1b(0lqk\x1b(B\x1b[3b" +
		"\x1b[?2004;1003;1006;1h\x1b=\x1b[>7u\x1b[?1049hALT\x1b[>3u"
	for cut := range len(stream) + 1 {
		t.Run(fmt.Sprint(cut), func(t *testing.T) {
			a := New(20, 4, nil)
			defer a.(*emulator).Close()
			_, _ = a.Write([]byte(stream[:cut]))
			b := restored(t, a)
			sameFrame(t, a, b)
			for _, e := range []Emulator{a, b} {
				_, _ = e.Write([]byte(stream[cut:]))
			}
			sameFrame(t, a, b)
			for _, e := range []Emulator{a, b} {
				_, _ = e.Write([]byte("\x1b[?1049l\x1b8\x1b[<uNEXT"))
			}
			sameFrame(t, a, b)
		})
	}
}

func TestHandoffHistoryAndIndependence(t *testing.T) {
	a := New(12, 3, nil).(*emulator)
	defer a.Close()
	a.SetScrollback(7)
	for i := range 20 {
		_, _ = fmt.Fprintf(a, "\x1b]133;A\a\x1b[31m%02d中\x1b[0m\r\n", i)
	}
	_, _ = a.Write([]byte("\x1b]7;file:///tmp/fixture\a\x1b[5 q\x1b[?1049hALT"))
	b := restored(t, a).(*emulator)
	if b.Cwd() != "/tmp/fixture" || b.st.hist.limit() != 7 {
		t.Fatal("cwd or custom history limit lost")
	}
	if !reflect.DeepEqual(a.st.hist.at(0), b.st.hist.at(0)) {
		t.Fatal("history marks, cells or styles lost")
	}
	for _, e := range []Emulator{a, b} {
		_, _ = e.Write([]byte("\x1b[?1049l"))
	}
	sameFrame(t, a, b)
	for off := 0; off <= a.ScrollbackLen(); off++ {
		if !reflect.DeepEqual(a.SnapshotAt(off), b.SnapshotAt(off)) {
			t.Fatalf("history row %d differs", off)
		}
	}
	before := a.Snapshot()
	_, _ = b.Write([]byte("\x1b[2Jchanged\r\n"))
	if !reflect.DeepEqual(before, a.Snapshot()) {
		t.Fatal("restored emulator shares mutable state")
	}
}

func TestHandoffSyncFrameAndCallbacks(t *testing.T) {
	a := New(20, 3, nil).(*emulator)
	defer a.Close()
	feed(a, "old frame")
	_, _ = a.Write([]byte("\x1b[?2026h\rnew frame"))
	b := restored(t, a).(*emulator)
	sameFrame(t, a, b)
	if row(b.Snapshot(), 0) != "old frame" {
		t.Fatal("synchronized output cache lost")
	}
	for _, e := range []Emulator{a, b} {
		_, _ = e.Write([]byte("\x1b[?2026l"))
	}
	sameFrame(t, a, b)
	if row(b.Snapshot(), 0) != "new frame" {
		t.Fatal("synchronized output did not finish")
	}
	var reply replyBuf
	data, err := Save(a)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Restore(data, &reply)
	if err != nil {
		t.Fatal(err)
	}
	defer c.(*emulator).Close()
	called := 0
	c.(*emulator).SetBellFunc(func() { called++ })
	_, _ = c.Write([]byte("\a\x1b[5n"))
	reply.waitFor(t, "\x1b[0n")
	if called != 1 {
		t.Fatalf("new callback called %d times", called)
	}
	// A restored frame must wake the new daemon at its original deadline.
	_, _ = a.Write([]byte("\x1b[?2026h"))
	a.st.syncStart = time.Now().Add(-syncTimeout)
	d := restored(t, a).(*emulator)
	wake := make(chan struct{}, 1)
	d.SetDirtyFunc(func() { wake <- struct{}{} })
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("restored synchronized frame never woke")
	}
}

// displayOnly deliberately exposes only the shared Emulator contract.
type displayOnly struct{ Emulator }

func TestHandoffRefusesInvalidState(t *testing.T) {
	e := New(10, 3, nil)
	defer e.(*emulator).Close()
	if data, err := Save(displayOnly{e}); data != nil || err == nil {
		t.Fatal("accepted an emulator without serialization support")
	}
	data, err := Save(e)
	if err != nil {
		t.Fatal(err)
	}
	unknown := bytes.Clone(data)
	binary.BigEndian.PutUint32(unknown[4:8], SnapshotVersion+1)
	if out, err := Restore(unknown, nil); out != nil || !errors.Is(err, ErrSnapshotVersion) {
		t.Fatalf("unknown version: %v, %v", out, err)
	}
	for _, bad := range [][]byte{nil, []byte("bad header"), data[:8], data[:len(data)/2]} {
		if out, err := Restore(bad, nil); err == nil || out != nil {
			t.Fatal("accepted truncated/corrupt snapshot")
		}
	}
	var s savedState
	if err := gob.NewDecoder(bytes.NewReader(data[8:])).Decode(&s); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*savedState){
		func(s *savedState) { s.OSCState = 4 },
		func(s *savedState) { s.History = []savedLine{{Text: "x", Cells: []uint16{0}}}; s.Pushed = 1 },
		func(s *savedState) { s.History = []savedLine{{Text: "x"}}; s.Pushed = 1 },
		func(s *savedState) { s.Kitty[0] = make([]uint8, kittyStackMax+1) },
	} {
		bad := s
		mutate(&bad)
		var buf bytes.Buffer
		buf.Write(data[:8])
		if err := gob.NewEncoder(&buf).Encode(bad); err != nil {
			t.Fatal(err)
		}
		if out, err := Restore(buf.Bytes(), nil); err == nil || out != nil {
			t.Fatal("accepted invalid state")
		}
	}
}

func largeHandoffPane(tb testing.TB) Emulator {
	tb.Helper()
	e := New(120, 40, nil)
	tb.Cleanup(func() { _ = e.(*emulator).Close() })
	for i := range 10050 {
		_, _ = fmt.Fprintf(e, "\x1b[32m%05d\x1b[0m %s\r\n", i, strings.Repeat("log payload ", 9))
	}
	_ = e.Snapshot() // A live pane normally has a cached display frame.
	return e
}

func TestHandoffSize(t *testing.T) {
	e := largeHandoffPane(t)
	start := time.Now()
	data, err := Save(e)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("10,000 history lines: %d bytes, %s", len(data), time.Since(start))
	if e.ScrollbackLen() != 10000 || len(data) >= 5_000_000 {
		t.Fatalf("history %d, snapshot %d bytes", e.ScrollbackLen(), len(data))
	}
	b := restored(t, e)
	sameFrame(t, e, b)
}

func BenchmarkHandoffSave10000(b *testing.B) {
	e := largeHandoffPane(b)
	b.ResetTimer()
	for b.Loop() {
		if _, err := Save(e); err != nil {
			b.Fatal(err)
		}
	}
}
