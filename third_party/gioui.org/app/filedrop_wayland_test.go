//go:build ((linux && !android) || freebsd) && !nowayland

package app

import (
	"os"
	"testing"
	"unsafe"
)

func TestFileDropDisplayClose(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	d := &wlDisplay{dropReaders: map[*os.File]unsafe.Pointer{r: nil}}
	d.destroy()
	if !d.dropClosed || len(d.dropReaders) != 0 {
		t.Fatal("display close retained drop readers")
	}
	if _, err := r.Read(make([]byte, 1)); err == nil {
		t.Fatal("display close left a pending drop pipe open")
	}
}
