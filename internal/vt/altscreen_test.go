package vt

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
)

func TestAltScreenMemory(t *testing.T) {
	const cols, rows, batch = 120, 40, 250
	data := bytes.Repeat([]byte(strings.Repeat("x", cols)+"\r\n"), batch)
	e := New(cols, rows, nil)
	if _, err := e.Write([]byte("\x1b[?1049h")); err != nil {
		t.Fatal(err)
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for range 50000 / batch { // each write fits the pane's 32KB read buffer
		if n, err := e.Write(data); n != len(data) || err != nil {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	total := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	t.Logf("50,000 alt-screen lines at %d cols retain %d bytes", cols, total)
	if total >= 2<<20 {
		t.Fatalf("alt-screen scrolling retains %d bytes, want under 2MB", total)
	}
	if g := e.Snapshot(); !g.AltScreen || row(g, rows-2) != strings.Repeat("x", cols) || row(g, rows-1) != "" {
		t.Fatal("alt screen did not scroll")
	}
	if e.ScrollbackLen() != 0 || e.ScrollbackPushed() != 0 {
		t.Fatal("alt-screen lines entered main-screen history")
	}
	runtime.KeepAlive(data)
	runtime.KeepAlive(e)
}
