package logs

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestRotate: a write that would pass the cap moves the file to .1,
// replacing the old one, and a second writer on the same path follows the
// move instead of writing into .1.
func TestRotate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "gui.log")
	a, err := Open(path, 20)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(path, 20)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	read := func(p string) string {
		t.Helper()
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	a.Write([]byte("one 456789\n"))
	b.Write([]byte("two 456789\n")) // 22 bytes would pass 20
	if got, old := read(path), read(path+".1"); got != "two 456789\n" || old != "one 456789\n" {
		t.Fatalf("after first rotation: %q, .1 %q", got, old)
	}
	a.Write([]byte("three\n")) // a's file was moved: a reopens path
	if got := read(path); got != "two 456789\nthree\n" {
		t.Fatalf("a did not follow the rotation: %q", got)
	}
	b.Write([]byte("four 6789\n"))
	if got, old := read(path), read(path+".1"); got != "four 6789\n" || old != "two 456789\nthree\n" {
		t.Fatalf("after second rotation: %q, .1 %q", got, old)
	}
	if runtime.GOOS != "windows" {
		for _, p := range []string{path, path + ".1", filepath.Dir(path)} {
			fi, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			if want := map[bool]os.FileMode{true: 0o700, false: 0o600}[fi.IsDir()]; fi.Mode().Perm() != want {
				t.Fatalf("%s: mode %v, want %v", p, fi.Mode().Perm(), want)
			}
		}
	}
	if strings.Contains(read(path), "one") {
		t.Fatal("rotated lines came back")
	}
}

// TestLimiter: one line per key per interval, with the count held back.
func TestLimiter(t *testing.T) {
	l := Limiter{Every: 10 * time.Second}
	t0 := time.Now()
	if ok, held := l.Allow("a", t0); !ok || held != 0 {
		t.Fatalf("first: %v %d", ok, held)
	}
	for i := 1; i <= 3; i++ {
		if ok, _ := l.Allow("a", t0.Add(time.Duration(i)*time.Second)); ok {
			t.Fatalf("second %d passed", i)
		}
	}
	if ok, _ := l.Allow("b", t0); !ok {
		t.Fatal("another key was held")
	}
	if ok, held := l.Allow("a", t0.Add(10*time.Second)); !ok || held != 3 {
		t.Fatalf("after the interval: %v, held %d, want 3", ok, held)
	}
}
