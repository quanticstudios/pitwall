package vt

import (
	"io"
	"strings"
	"testing"
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
