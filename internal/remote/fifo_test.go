//go:build unix

package remote

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A FIFO named like markdown is refused at once, not opened to wait for
// a writer.
func TestFileRefusesFIFO(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "pipe.md"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	done := make(chan error, 1)
	go func() {
		_, _, err := ReadFile(r, root, root, "pipe.md")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, errNotFile) {
			t.Errorf("FIFO: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO blocked")
	}
}
