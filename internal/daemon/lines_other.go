//go:build !unix

package daemon

import (
	"errors"
	"net"
	"time"
)

// helloWait is how long a new connection has to send its first byte; a
// var for tests.
var helloWait = 10 * time.Second

// peekConn replays one already-read byte before delegating to the underlying
// net.Conn. Used on platforms without MSG_PEEK (Windows, where handoff and
// peerPID do not require a raw *net.UnixConn).
type peekConn struct {
	net.Conn
	first    byte
	hasFirst bool
}

func (p *peekConn) Read(b []byte) (int, error) {
	if p.hasFirst && len(b) > 0 {
		b[0] = p.first
		p.hasFirst = false
		return 1, nil
	}
	return p.Conn.Read(b)
}

// jsonClient reads the first byte of nc within helloWait and reports whether
// it is '{', wrapping nc so the next reader still receives that byte.
func jsonClient(nc net.Conn) (net.Conn, bool, error) {
	if _, ok := nc.(*net.UnixConn); !ok {
		return nc, false, nil
	}
	nc.SetReadDeadline(time.Now().Add(helloWait))
	defer nc.SetReadDeadline(time.Time{})
	var b [1]byte
	n, err := nc.Read(b[:])
	if err != nil {
		return nc, false, err
	}
	if n == 0 {
		return nc, false, errors.New("closed before its first byte")
	}
	return &peekConn{Conn: nc, first: b[0], hasFirst: true}, b[0] == '{', nil
}
