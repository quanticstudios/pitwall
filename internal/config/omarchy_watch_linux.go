package config

import (
	"bytes"
	"encoding/binary"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// omarchyDebounce is how long WatchOmarchy waits after the last change: a
// switch replaces the theme folder and writes theme.name within a few ms.
const omarchyDebounce = 100 * time.Millisecond

// WatchOmarchy calls changed after the applied Omarchy theme changes, until
// stop closes. It holds one inotify watch per existing current folder and
// sleeps on it, so an unchanged theme costs nothing. On a machine without
// Omarchy it returns at once: installing Omarchy later needs a restart.
func WatchOmarchy(stop <-chan struct{}, changed func()) {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return
	}
	f := os.NewFile(uintptr(fd), "inotify") // nonblocking: reads wait in Go's poller, Close wakes them
	defer f.Close()
	n := 0
	for _, d := range OmarchyDirs() {
		if _, err := unix.InotifyAddWatch(fd, d, unix.IN_CREATE|unix.IN_DELETE|unix.IN_MOVED_TO|unix.IN_MOVED_FROM|unix.IN_CLOSE_WRITE); err == nil {
			n++
		}
	}
	if n == 0 {
		return
	}
	events := make(chan struct{}, 1)
	go func() {
		buf := make([]byte, 64<<10)
		for {
			k, err := f.Read(buf)
			if err != nil {
				close(events)
				return
			}
			if relevantOmarchy(buf[:k]) {
				select {
				case events <- struct{}{}:
				default:
				}
			}
		}
	}()
	go func() {
		<-stop
		f.Close()
	}()
	var fire <-chan time.Time
	for {
		select {
		case _, ok := <-events:
			if !ok {
				return
			}
			fire = time.After(omarchyDebounce)
		case <-fire:
			fire = nil
			changed()
		case <-stop:
			return
		}
	}
}

// relevantOmarchy reports whether a batch of inotify events touches the
// theme folder or the files that name it. An event is wd, mask, cookie and
// len, four native-endian uint32s, then len bytes of NUL-padded name.
func relevantOmarchy(b []byte) bool {
	for len(b) >= unix.SizeofInotifyEvent {
		mask := binary.NativeEndian.Uint32(b[4:])
		end := unix.SizeofInotifyEvent + int(binary.NativeEndian.Uint32(b[12:]))
		if mask&unix.IN_Q_OVERFLOW != 0 || end > len(b) {
			return true
		}
		switch string(bytes.TrimRight(b[unix.SizeofInotifyEvent:end], "\x00")) {
		case "theme", "theme.name":
			return true
		}
		b = b[end:]
	}
	return false
}
