package proto

import "net"

// SocketPath is $XDG_RUNTIME_DIR/pitwall/pitwall.sock.
func SocketPath() string { panic("unimplemented") }

// Conn frames messages from Messages over a stream. Send is safe for
// concurrent use; Recv is not.
type Conn struct{}

func NewConn(c net.Conn) *Conn        { panic("unimplemented") }
func Dial(path string) (*Conn, error) { panic("unimplemented") }
func (c *Conn) Send(msg any) error    { panic("unimplemented") }
func (c *Conn) Recv() (any, error)    { panic("unimplemented") }
func (c *Conn) Close() error          { panic("unimplemented") }
