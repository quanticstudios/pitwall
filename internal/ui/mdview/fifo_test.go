//go:build unix

package mdview

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestReadRefusesFIFO: a FIFO, or a link to one, named as a markdown file
// or an image is refused at once, not waited on for a writer.
func TestReadRefusesFIFO(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "hang.md")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	link := filepath.Join(dir, "link.md")
	if err := os.Symlink(fifo, link); err != nil {
		t.Fatal(err)
	}
	// A writer frees a read that waits, so a failing test still ends.
	defer func() {
		if f, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			f.Close()
		}
	}()
	for _, p := range []string{fifo, link} {
		done := make(chan error, 1)
		go func() {
			var v View
			err := v.Open(p, "")
			v.Close()
			if err == nil {
				_, err = readCapped(p, MaxImageBytes)
			}
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("%s: read", filepath.Base(p))
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: still waiting for a writer", filepath.Base(p))
		}
	}
}
