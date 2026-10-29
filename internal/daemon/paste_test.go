package daemon

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// A pasted image lands in a 0600 file in a 0700 folder under the state
// directory, named for its type, and pasting clears files over a day old.
func TestPasteImage(t *testing.T) {
	dir := filepath.Join(config.StateDir(), "paste")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old, fresh := filepath.Join(dir, "image-old.png"), filepath.Join(dir, "image-fresh.png")
	for _, f := range []string{old, fresh} {
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ago := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(old, ago, ago); err != nil {
		t.Fatal(err)
	}

	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	r := pasteImage(proto.PasteImage{Pane: "p", Data: png})
	if r.Err != "" || r.Pane != "p" || filepath.Dir(r.Path) != dir || !strings.HasSuffix(r.Path, ".png") {
		t.Fatalf("result %+v", r)
	}
	if b, err := os.ReadFile(r.Path); err != nil || !bytes.Equal(b, png) {
		t.Errorf("file %q, %v", b, err)
	}
	for f, want := range map[string]os.FileMode{r.Path: 0o600, dir: 0o700} {
		if runtime.GOOS == "windows" {
			break // Windows has no Unix modes, and no remote windows
		}
		// why: Windows has no Unix mode bits; Stat reports 0666 and 0777.
		if fi, err := os.Stat(f); err != nil || runtime.GOOS != "windows" && fi.Mode().Perm() != want {
			t.Errorf("%s: mode %v, %v; want %v", f, fi.Mode().Perm(), err, want)
		}
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("a day-old paste is still there: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("a fresh paste went: %v", err)
	}

	if r := pasteImage(proto.PasteImage{Pane: "p", Data: []byte("plain text")}); r.Err == "" || r.Path != "" {
		t.Errorf("text saved as an image: %+v", r)
	}
	big := append(png, make([]byte, proto.MaxPasteImage)...)
	if r := pasteImage(proto.PasteImage{Pane: "p", Data: big}); r.Err != "the image is over 20 MiB" {
		t.Errorf("too big: %+v", r)
	}
}
