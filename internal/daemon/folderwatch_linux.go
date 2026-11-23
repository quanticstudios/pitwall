package daemon

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

// folderWatch wakes a pane stream when a file in one of its folders is
// made, written, moved or removed: inotify on the folders, not the files,
// so a transcript made late or renamed still wakes it.
type folderWatch struct {
	f    *os.File
	fd   int // f's, which f.Fd would set blocking, and Close could then not stop the read
	mu   sync.Mutex
	wds  map[string]int // the watched folders' watch descriptors
	gone bool
}

// foldersWatched counts the folders every folderWatch watches, for tests.
var foldersWatched atomic.Int64

// folderMask is the changes in a folder that wake the stream.
const folderMask = unix.IN_CREATE | unix.IN_MODIFY | unix.IN_CLOSE_WRITE | unix.IN_MOVED_TO | unix.IN_MOVED_FROM | unix.IN_DELETE | unix.IN_ONLYDIR

// newFolderWatch is a watch that calls wake at each change; nil when
// inotify fails, which leaves the stream to its tick.
func newFolderWatch(wake func()) *folderWatch {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		log.Printf("pane chat: inotify: %q", err)
		return nil
	}
	w := &folderWatch{f: os.NewFile(uintptr(fd), "inotify"), fd: fd, wds: map[string]int{}}
	go func() {
		buf := make([]byte, 16<<10)
		for {
			if _, err := w.f.Read(buf); err != nil {
				return // closed
			}
			wake()
		}
	}()
	return w
}

// Set watches dirs and no other folder. A folder not made yet is watched
// through the nearest one above it that is, which tells when it is made.
func (w *folderWatch) Set(dirs []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.gone {
		return
	}
	want := map[string]bool{}
	for _, d := range dirs {
		for range 4 {
			if _, ok := w.wds[d]; ok {
				want[d] = true
				break
			}
			wd, err := unix.InotifyAddWatch(w.fd, d, folderMask)
			if err == nil {
				w.wds[d], want[d] = wd, true
				foldersWatched.Add(1)
				break
			}
			if !errors.Is(err, unix.ENOENT) {
				break
			}
			up := filepath.Dir(d)
			if up == d {
				break
			}
			d = up
		}
	}
	for d, wd := range w.wds {
		if !want[d] {
			unix.InotifyRmWatch(w.fd, uint32(wd)) // fails for a folder removed, whose watch went with it
			delete(w.wds, d)
			foldersWatched.Add(-1)
		}
	}
}

// Close drops every watch.
func (w *folderWatch) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.gone {
		return
	}
	w.gone = true
	foldersWatched.Add(-int64(len(w.wds)))
	w.wds = nil
	w.f.Close()
}
