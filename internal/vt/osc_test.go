package vt

import (
	"encoding/base64"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
)

func titleRow(g Grid, y int) string {
	var b strings.Builder
	for x := 0; x < g.Cols; x++ {
		b.WriteString(g.At(x, y).Content)
	}
	return strings.TrimRight(b.String(), " ")
}

func TestTitleWithC1ByteInUTF8(t *testing.T) {
	for _, in := range []string{
		"\x1b]0;✳ Fix login redirects\x07ok", // BEL, and ✳ holds byte 0x9C
		"\x1b]2;✳ Fix login redirects\x1b\\ok",
	} {
		e := New(40, 3, io.Discard)
		e.Write([]byte(in))
		g := e.Snapshot()
		if g.Title != "✳ Fix login redirects" || titleRow(g, 0) != "ok" {
			t.Errorf("%q: title %q, row %q", in, g.Title, titleRow(g, 0))
		}
	}
}

func TestTitleSplitAcrossWrites(t *testing.T) {
	e := New(40, 3, io.Discard)
	b := []byte("a\x1b]0;✳ Topic\x07b")
	for i := range b {
		e.Write(b[i : i+1])
	}
	if g := e.Snapshot(); g.Title != "✳ Topic" || titleRow(g, 0) != "ab" {
		t.Fatalf("title %q, row %q", g.Title, titleRow(g, 0))
	}
}

func TestOtherSequencesPassThrough(t *testing.T) {
	e := New(40, 3, io.Discard)
	e.Write([]byte("\x1b[1mbold\x1b[0m \x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\"))
	g := e.Snapshot()
	if titleRow(g, 0) != "bold link" || g.At(0, 0).Attrs&Bold == 0 {
		t.Fatalf("row %q attrs %v", titleRow(g, 0), g.At(0, 0).Attrs)
	}
}

func TestNotifications(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []Notification
	}{
		{"\x1b]9;done\x07", []Notification{{Body: "done"}}},
		{"\x1b]9;✳ build passed\x1b\\", []Notification{{Body: "✳ build passed"}}}, // ✳ holds 0x9C
		{"\x1b]9;4;1;50\x07\x1b]9;1;100\x07", nil},                                // ConEmu progress, sleep
		{"\x1b]9;\x07", nil},
		{"\x1b]777;notify;Gemini;Needs input; now\x1b\\", []Notification{{Title: "Gemini", Body: "Needs input; now"}}},
		{"\x1b]777;notify;Only title\x07", []Notification{{Title: "Only title"}}},
		{"\x1b]777;preexec\x07", nil},
		{"\x1b]99;;Hello ✳\x1b\\", []Notification{{Title: "Hello ✳"}}},
		{"\x1b]99;i=1:d=1:p=body;Body text\x1b\\", []Notification{{Body: "Body text"}}},
		{"\x1b]99;i=1:e=1;" + "w7NzcmVl" + "\x1b\\", []Notification{{Title: "ósree"}}},
		{"\x1b]99;i=1:d=0;Chunk\x1b\\", nil},
		{"\x1b]99;i=1:p=icon;abc\x1b\\", nil},
	} {
		var got []Notification
		var f oscFilter
		b := []byte("a" + tc.in + "b")
		var out []byte
		for i := range b { // one byte per Write: state carries over
			out = append(out, f.feed(b[i:i+1], func(string) {}, func(n Notification) { got = append(got, n) })...)
		}
		if string(out) != "ab" || !slices.Equal(got, tc.want) {
			t.Errorf("%q: out %q, got %+v, want %+v", tc.in, out, got, tc.want)
		}
	}
}

func TestEmulatorNotify(t *testing.T) {
	e := New(40, 3, io.Discard)
	var got []Notification
	e.(interface{ SetNotifyFunc(func(Notification)) }).SetNotifyFunc(func(n Notification) { got = append(got, n) })
	e.Write([]byte("x\x1b]9;Agent ✳ waits\x07y"))
	if g := e.Snapshot(); titleRow(g, 0) != "xy" || len(got) != 1 || got[0].Body != "Agent ✳ waits" {
		t.Fatalf("row %q, got %+v", titleRow(g, 0), got)
	}
}

func TestClipboard(t *testing.T) {
	big := strings.Repeat("a", clipboardMax)
	for _, tc := range []struct {
		name, payload, want string
		ok                  bool
	}{
		{"write", "c;aGVsbG8=", "hello", true},
		{"other selection", "p;aGVsbG8=", "hello", true},
		{"default selection", ";aGVsbG8=", "hello", true},
		{"read", "c;?", "", false},
		{"empty", "c;", "", false},
		{"not base64", "c;h*llo", "", false},
		{"no selection", "aGVsbG8=", "", false},
		{"at the cap", "c;" + base64.StdEncoding.EncodeToString([]byte(big)), big, true},
		{"over the cap", "c;" + base64.StdEncoding.EncodeToString([]byte(big+"a")), "", false},
	} {
		if got, ok := parseClipboard(tc.payload); got != tc.want || ok != tc.ok {
			t.Errorf("%s: %.20q %v, want %.20q %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

// TestEmulatorClipboard sends OSC 52 through the emulator: a write reaches
// the callback, also one longer than the OSC filter buffers, and a read
// leaves the screen alone and gets no reply.
func TestEmulatorClipboard(t *testing.T) {
	reply := &replyBuf{}
	e := New(40, 3, reply)
	var got []string
	e.(interface{ SetClipboardFunc(func(string)) }).SetClipboardFunc(func(s string) { got = append(got, s) })
	long := strings.Repeat("x", 100<<10)
	e.Write([]byte("a\x1b]52;c;aGVsbG8=\x07b\x1b]52;c;?\x1b\\c\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(long)) + "\x07d\x1b[5n"))
	if g := e.Snapshot(); titleRow(g, 0) != "abcd" || len(got) != 2 || got[0] != "hello" || got[1] != long {
		t.Fatalf("row %q, got %d writes", titleRow(g, 0), len(got))
	}
	// Replies keep their order, so an answer to the read would come before
	// the DSR's.
	for deadline := time.Now().Add(time.Second); ; time.Sleep(time.Millisecond) {
		reply.mu.Lock()
		got := reply.b.String()
		reply.mu.Unlock()
		if strings.Contains(got, "\x1b[0n") || time.Now().After(deadline) {
			if got != "\x1b[0n" {
				t.Fatalf("replies %q, want only the DSR's", got)
			}
			break
		}
	}
}

func TestEmulatorBell(t *testing.T) {
	e := New(40, 3, io.Discard)
	n := 0
	e.(interface{ SetBellFunc(func()) }).SetBellFunc(func() { n++ })
	e.Write([]byte("a\x07b\x1b]0;title\x07\x1b]8;;http://x\x07c\x1b]8;;\x07\x07"))
	if n != 2 {
		t.Fatalf("%d bells, want 2: the BEL ending an OSC is none", n)
	}
}
