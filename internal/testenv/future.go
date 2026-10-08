package testenv

import (
	"bytes"
	"encoding/gob"
	"net"
)

// Future stands for a message type a newer pitwall adds. Sent through a
// FromFuture conn, it reaches the other end under a gob name that no build
// registers, as a newer peer's message reaches an older one.
type Future struct{ Note string }

const (
	sentName = "pitwall.test/future-sent"
	lostName = "pitwall.test/future-lost" // same length, so frame sizes hold
)

func init() { gob.RegisterName(sentName, Future{}) }

// FromFuture wraps c so that every write renames Future to a type nobody
// registered.
func FromFuture(c net.Conn) net.Conn { return futureConn{c} }

type futureConn struct{ net.Conn }

func (c futureConn) Write(p []byte) (int, error) {
	return c.Conn.Write(bytes.ReplaceAll(p, []byte(sentName), []byte(lostName)))
}
