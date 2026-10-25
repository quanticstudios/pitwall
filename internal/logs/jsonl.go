package logs

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
)

// Rotating is a file moved to <path>.1, replacing the old one, when a line
// would take it past Max, so the two hold at most about twice Max. Only
// one goroutine may use it, such as a Writer's.
type Rotating struct {
	path string
	max  int64
	f    *os.File
	size int64
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
	n, err := r.f.Write(p)
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
// decode as T and that keep accepts. Missing files are no lines.
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
			var e T
			if json.Unmarshal(sc.Bytes(), &e) == nil && keep(e) {
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
