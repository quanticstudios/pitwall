package vt

import (
	"fmt"
	"io"
	"testing"
)

func TestScrollbackText(t *testing.T) {
	e := New(5, 3, io.Discard)
	e.Write([]byte("\x1b[31mabcdefghij\x1b[0m\r\n漢字!\r\nlast"))
	if got, want := Scrollback(e), "abcdefghij\n漢字!\nlast\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	e.Write([]byte("\x1b[?1049hfull"))
	if got := Scrollback(e); got != "full\n" {
		t.Fatalf("alt screen: %q", got)
	}
	e.Write([]byte("\x1b[?1049l"))
	if got := Scrollback(e); got != "abcdefghij\n漢字!\nlast\n" {
		t.Fatalf("main screen: %q", got)
	}
}

func TestScrollbackTrimmedHistory(t *testing.T) {
	e := New(12, 3, io.Discard)
	e.(interface{ SetScrollback(int) }).SetScrollback(2)
	for i := range 10 {
		fmt.Fprintf(e, "line %d\r\n", i)
	}
	if got, want := Scrollback(e), "line 6\nline 7\nline 8\nline 9\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := Scrollback(New(5, 3, io.Discard)); got != "" {
		t.Fatalf("empty screen: %q", got)
	}
}

func TestScrollbackSnapshotSurvivesWrites(t *testing.T) {
	e := New(5, 3, io.Discard)
	e.Write([]byte("\x1b[31mabcdefghij\x1b[0m\r\n漢字!\r\nlast"))
	build := CaptureScrollback(e)
	e.Resize(3, 2)
	for i := range 30 {
		fmt.Fprintf(e, "row %d\r\n", i)
	}
	e.Write([]byte("\x1b[3J\x1b[2J"))
	if got, want := build(), "abcdefghij\n漢字!\nlast\n"; got != want {
		t.Fatalf("captured text changed: %q, want %q", got, want)
	}
}
