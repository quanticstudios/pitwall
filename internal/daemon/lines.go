//go:build linux

package daemon

import (
	"errors"
	"net"
	"time"

	"golang.org/x/sys/unix"
)

// helloWait is how long a new connection has to send its first byte; a
// var for tests.
var helloWait = 10 * time.Second

// jsonClient reports whether the client on nc opened with '{', a JSON
// line for d.lines rather than a gob frame, whose 4-byte length is at
// most 64 MiB and so never starts with 0x7b. It peeks with MSG_PEEK, so
// the gob path reads every byte through the same *net.UnixConn, which
// handoff and peerPID need. Only a unix socket is peeked.
func jsonClient(nc net.Conn) (bool, error) {
	uc, ok := nc.(*net.UnixConn)
	if !ok {
		return false, nil
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return false, err
	}
	uc.SetReadDeadline(time.Now().Add(helloWait))
	defer uc.SetReadDeadline(time.Time{})
	var b [1]byte
	var n int
	var perr error
	err = raw.Read(func(fd uintptr) bool {
		n, _, perr = unix.Recvfrom(int(fd), b[:], unix.MSG_PEEK)
		return !errors.Is(perr, unix.EAGAIN)
	})
	switch {
	case err != nil:
		return false, err
	case perr != nil:
		return false, perr
	case n == 0:
		return false, errors.New("closed before its first byte")
	}
	return b[0] == '{', nil
}
