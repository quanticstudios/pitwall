package repoicon

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

// TestRead reads an icon in the repo, and no file a symlink, the icon's
// own or a folder's above it, leads out of the repo to. An icon swapped
// for a big file after Files listed it is refused without reading it all.
func TestRead(t *testing.T) {
	dir := t.TempDir()
	repo, outside := filepath.Join(dir, "repo"), filepath.Join(dir, "outside")
	png := []byte("\x89PNG\r\n\x1a\nsecret")
	for name, data := range map[string][]byte{
		"repo/.git/HEAD":       []byte("ref: refs/heads/main\n"),
		"repo/favicon.png":     png,
		"outside/favicon.svg":  []byte("<svg>transcript</svg>"),
		"outside/favicon.ico":  png,
		"outside/transcript":   png,
		"repo/assets/real.txt": png,
	} {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if b, err := Read(repo, filepath.Join(repo, "favicon.png")); err != nil || string(b) != string(png) {
		t.Fatalf("an icon in the repo: %q %v", b, err)
	}

	if err := os.Symlink(filepath.Join(outside, "favicon.svg"), filepath.Join(repo, "favicon.svg")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	os.Symlink(outside, filepath.Join(repo, "public"))
	os.Symlink("assets/real.txt", filepath.Join(repo, "logo.png"))
	files := Files(repo)
	for _, f := range []string{filepath.Join(repo, "favicon.svg"), filepath.Join(repo, "public/favicon.ico")} {
		if b, err := Read(repo, f); err == nil {
			t.Errorf("%s leads out of the repo, read %q", f, b)
		}
	}
	if b, err := Read(repo, filepath.Join(repo, "logo.png")); err != nil || string(b) != string(png) {
		t.Errorf("a symlink inside the repo: %q %v", b, err)
	}

	// Files saw favicon.png at a few bytes; swap in 64 MiB under it.
	if !slices.Contains(files, filepath.Join(repo, "favicon.png")) {
		t.Fatalf("Files: %q", files)
	}
	big := filepath.Join(dir, "big")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	f.Truncate(64 << 20)
	f.Close()
	if err := os.Rename(big, filepath.Join(repo, "favicon.png")); err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	b, err := Read(repo, filepath.Join(repo, "favicon.png"))
	runtime.ReadMemStats(&after)
	if err == nil {
		t.Errorf("a 64 MiB icon: read %d bytes", len(b))
	}
	if got := after.TotalAlloc - before.TotalAlloc; got > 8*Max {
		t.Errorf("a 64 MiB icon: allocated %d bytes to refuse it, want at most %d", got, 8*Max)
	}
}
