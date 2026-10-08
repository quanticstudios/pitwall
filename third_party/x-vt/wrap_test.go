package vt

import "testing"

func TestSoftWrapFlags(t *testing.T) {
	e := NewEmulator(5, 3)
	_, _ = e.WriteString("abcdefg\r\nhi")
	s := &e.scrs[0]
	if !s.Wrapped(0) || s.Wrapped(1) || s.Wrapped(2) {
		t.Fatalf("wrapped %v %v %v", s.Wrapped(0), s.Wrapped(1), s.Wrapped(2))
	}
	// Erasing to the end of a row ends its wrap.
	_, _ = e.WriteString("\x1b[1;3H\x1b[K")
	if s.Wrapped(0) {
		t.Fatal("EL left row 0 wrapped")
	}

	// A wrapped row scrolls into history with its flag and its trailing
	// blank, which is text.
	e = NewEmulator(5, 2)
	_, _ = e.WriteString("abcd efgh\r\n\r\n")
	sb := e.Scrollback()
	if sb.Len() != 2 || !sb.Wrapped(0) || sb.Wrapped(1) || len(sb.Line(0)) != 5 {
		t.Fatalf("scrollback %d lines, wrapped %v %v, first %d cells", sb.Len(), sb.Wrapped(0), sb.Wrapped(1), len(sb.Line(0)))
	}

	// A wide char that does not fit wraps whole, leaving a zero cell.
	e = NewEmulator(5, 2)
	_, _ = e.WriteString("abcd中")
	if c := e.CellAt(4, 0); !c.IsZero() || e.CellAt(0, 1).Content != "中" || !e.scrs[0].Wrapped(0) {
		t.Fatalf("cell 4 %+v, next row %q", c, e.CellAt(0, 1).Content)
	}
	// One that ends in the last column waits there to wrap.
	e = NewEmulator(4, 2)
	_, _ = e.WriteString("ab中x")
	if e.CellAt(2, 0).Content != "中" || e.CellAt(0, 1).Content != "x" {
		t.Fatalf("rows %q %q", e.CellAt(2, 0).Content, e.CellAt(0, 1).Content)
	}
}
