package pane

import (
	"errors"
	"os"
	"testing"
)

func TestAdoptUnsupported(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "master")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Adopt(Config{}, f, 1); err == nil {
		t.Fatal("Windows adoption succeeded")
	}
	if _, err := f.Write([]byte("still open")); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("master still open after failed adoption: %v", err)
	}
}
