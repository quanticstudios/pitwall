package logs

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
)

// Rotating is a file moved to <path>.1, replacing the old one, when a line
// would take it past Max, so the two hold at most about twice Max. Only
// one goroutine may use it, such as a Writer's.
type Rotating struct {
	path string
	max  int64
	f    file
	size int64
	torn bool // a cut line could not be truncated away; start the next on a new line
}

type file interface {
	io.WriteCloser
	Truncate(size int64) error
}

// OpenRotating opens path for appending as OpenFile does.
func OpenRotating(path string, max int64) (*Rotating, error) {
	r := &Rotating{path: path, max: max}
	if err := r.reopen(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Rotating) reopen() error {
	f, err := OpenFile(r.path, r.max)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	r.f, r.size = f, fi.Size()
	return nil
}

func (r *Rotating) Write(p []byte) (int, error) {
	if r.f != nil && r.size > 0 && r.size+int64(len(p)) > r.max {
		r.f.Close()
		r.f = nil
		os.Rename(r.path, r.path+".1")
	}
	if r.f == nil {
		if err := r.reopen(); err != nil {
			return 0, err
		}
	}
	if r.torn {
		if _, err := r.f.Write([]byte("\n")); err != nil {
			return 0, err
		}
		r.size++
		r.torn = false
	}
	n, err := r.f.Write(p)
	if err != nil && n > 0 {
		// A cut line would glue the next record onto it, and readers drop
		// the whole line. Cut it off, or failing that end it.
		if r.f.Truncate(r.size) != nil {
			r.size += int64(n)
			r.torn = true
		}
		return 0, err
	}
	r.size += int64(n)
	return n, err
}

func (r *Rotating) Close() error {
	if r.f == nil {
		return nil
	}
	return r.f.Close()
}

// ReadJSONL returns the lines of path.1 and path, oldest first, that
// decode as T and that keep accepts. Missing files are no lines. A line
// holding a cut record and then a whole one, as a full disk left them
// before pitwall truncated failed writes, yields the whole one.
func ReadJSONL[T any](path string, keep func(T) bool) ([]T, error) {
	var out []T
	for _, p := range []string{path + ".1", path} {
		f, err := os.Open(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			if e, ok := decodeLine[T](sc.Bytes()); ok && keep(e) {
				out = append(out, e)
			}
		}
		err = sc.Err()
		f.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// decodeLine decodes line, or else its first suffix starting at '{' that
// is one whole value: trailing bytes fail Unmarshal, so a nested object of
// the whole record never matches before the record itself.
func decodeLine[T any](line []byte) (T, bool) {
	var e T
	if json.Unmarshal(line, &e) == nil {
		return e, true
	}
	for i := 1; i < len(line); i++ {
		j := bytes.IndexByte(line[i:], '{')
		if j < 0 {
			break
		}
		i += j
		var e T
		if json.Unmarshal(line[i:], &e) == nil {
			return e, true
		}
	}
	return e, false
}
