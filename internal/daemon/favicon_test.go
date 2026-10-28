package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/quanticstudios/pitwall/internal/proto"
)

func TestFavicon(t *testing.T) {
	write := func(path string, b []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	repo := t.TempDir()
	write(filepath.Join(repo, ".git", "HEAD"), []byte("ref: refs/heads/main\n"))
	write(filepath.Join(repo, "public", "favicon.png"), []byte("png bytes"))
	write(filepath.Join(repo, "favicon.svg"), []byte("<svg/>"))
	sub := filepath.Join(repo, "src", "lib")
	write(filepath.Join(sub, "x.go"), nil)

	r := favicon(proto.Favicon{Dir: sub})
	if r.Dir != sub || r.Root != repo {
		t.Fatalf("dir %q root %q, want %q %q", r.Dir, r.Root, sub, repo)
	}
	if len(r.Files) != 2 || r.Files[0].Name != filepath.Join(repo, "favicon.svg") || string(r.Files[0].Data) != "<svg/>" || string(r.Files[1].Data) != "png bytes" {
		t.Fatalf("files, best first: %+v", r.Files)
	}

	bare := t.TempDir()
	write(filepath.Join(bare, ".git", "HEAD"), []byte("ref: refs/heads/main\n"))
	if r := favicon(proto.Favicon{Dir: bare}); r.Root != bare || len(r.Files) != 0 {
		t.Fatalf("a repo with no icon: %+v", r)
	}
}
