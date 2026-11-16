package logs

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

type rec struct {
	V int    `json:"v"`
	N string `json:"n"`
}

// shortFile writes only half of its next write, then fails, as write(2)
// does when the disk fills mid-line.
type shortFile struct {
	*os.File
	short     bool
	failTrunc bool
}

func (s *shortFile) Truncate(n int64) error {
	if s.failTrunc {
		return errors.New("no truncate")
	}
	return s.File.Truncate(n)
}

func (s *shortFile) Write(p []byte) (int, error) {
	if s.short {
		s.short = false
		n, _ := s.File.Write(p[:len(p)/2])
		return n, syscall.ENOSPC
	}
	return s.File.Write(p)
}

func TestRotatingShortWriteLeavesNoTornLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.jsonl")
	r, err := OpenRotating(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	sf := &shortFile{File: r.f.(*os.File)}
	r.f = sf
	first, cut, third := `{"v":1,"n":"first"}`+"\n", `{"v":1,"n":"cut"}`+"\n", `{"v":1,"n":"third"}`+"\n"
	if _, err := r.Write([]byte(first)); err != nil {
		t.Fatal(err)
	}
	sf.short = true
	if _, err := r.Write([]byte(cut)); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("short write err = %v, want ENOSPC", err)
	}
	if _, err := r.Write([]byte(third)); err != nil {
		t.Fatal(err)
	}
	r.Close()
	got, err := ReadJSONL(path, func(e rec) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].N != "first" || got[1].N != "third" {
		t.Fatalf("read %+v, want first and third", got)
	}
	// Windows may refuse to truncate an append-only handle; the fallback
	// newline still keeps third on its own line, checked above.
	if runtime.GOOS != "windows" {
		b, _ := os.ReadFile(path)
		if string(b) != first+third {
			t.Fatalf("file = %q, want %q", b, first+third)
		}
		if r.size != int64(len(first+third)) {
			t.Fatalf("size = %d, want %d", r.size, len(first+third))
		}
	}
}

func TestRotatingUntruncatableCutEndsTheLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.jsonl")
	r, err := OpenRotating(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	r.f = &shortFile{File: r.f.(*os.File), short: true, failTrunc: true}
	r.Write([]byte(`{"v":1,"n":"cut"}` + "\n"))
	r.Write([]byte(`{"v":1,"n":"next"}` + "\n"))
	r.Close()
	b, _ := os.ReadFile(path)
	if want := `{"v":1,"n` + "\n" + `{"v":1,"n":"next"}` + "\n"; string(b) != want {
		t.Fatalf("file = %q, want %q", b, want)
	}
	if r.size != int64(len(b)) {
		t.Fatalf("size = %d, want %d", r.size, len(b))
	}
}

func TestReadJSONLRecoversRecordAfterCutOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.jsonl")
	lines := `{"v":1,"n":"a"}` + "\n" +
		`{"v":1,"n":"cut","x":{"y":` + `{"v":1,"n":"b","x":{"y":2}}` + "\n" +
		`{"v":1,"n":"no` + "\n" +
		`{"v":1,"n":"c"}` + "\n"
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadJSONL(path, func(e rec) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range got {
		names = append(names, e.N)
	}
	if len(names) != 3 || names[0] != "a" || names[1] != "b" || names[2] != "c" {
		t.Fatalf("read %v, want [a b c]", names)
	}
}
