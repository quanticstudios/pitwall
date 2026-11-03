package vt

import (
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestHistoryText(t *testing.T) {
	e := New(50, 3, io.Discard)
	defer e.(*emulator).Close()
	e.Write([]byte("old\r\n\x1b[31mred\x1b[0m\r\n\x1b[38;2;1;2;3m界é\x1b[0m\r\nlast"))
	cols, text := HistoryText(e, 3)
	if cols != 50 || strings.Contains(text, "old") || strings.Count(text, "\n") != 3 {
		t.Fatalf("capture %d %q", cols, text)
	}
	if !strings.Contains(text, "\x1b[38;5;1mred") || !strings.Contains(text, "\x1b[38;2;1;2;3m界") || !strings.Contains(text, "\x1b[38;2;1;2;3mé") {
		t.Fatalf("colors or graphemes lost: %q", text)
	}
	e.Write([]byte("\x1b[?1049hsecret alternate screen"))
	_, alt := HistoryText(e, 3)
	if alt != text {
		t.Fatalf("alt screen changed main history: %q", alt)
	}
	if _, off := HistoryText(e, 0); off != "" {
		t.Fatalf("disabled: %q", off)
	}
}

func TestHistoryTextByteBound(t *testing.T) {
	e := New(1000, 2, io.Discard)
	defer e.(*emulator).Close()
	// Long graphemes must not let a line count become an unlimited byte count.
	for range 300 {
		e.Write([]byte(strings.Repeat("e"+strings.Repeat("́", 30), 1000) + "\r\n"))
	}
	_, text := HistoryText(e, 2000)
	if len(text) > maxHistoryText || !strings.HasSuffix(text, "\r\n") {
		t.Fatalf("capture size %d", len(text))
	}
}

func BenchmarkHistoryText(b *testing.B) {
	e := New(120, 24, io.Discard)
	defer e.(*emulator).Close()
	for n := range 2200 {
		fmt.Fprintf(e, "\x1b[32m%04d\x1b[0m compiled internal/example in %d ms: %s\r\n", n, n%500, strings.Repeat("x", 55))
	}
	b.ResetTimer()
	for b.Loop() {
		HistoryText(e, 2000)
	}
}

func TestRestoredHistorySurvivesScreenClear(t *testing.T) {
	e := New(80, 1, io.Discard)
	defer e.(*emulator).Close()
	e.Write([]byte("saved row\r\n"))
	e.Resize(40, 24)
	e.Write([]byte("divider\r\n\x1b[H\x1b[2Jfresh"))
	_, text := HistoryText(e, 2000)
	if !strings.Contains(text, "saved row") || !strings.Contains(text, "fresh") {
		t.Fatalf("screen clear lost history: %q", text)
	}
	if e.ScrollbackLen() != 2 {
		t.Fatalf("history rows %d", e.ScrollbackLen())
	}
}
