package proto

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

func init() {
	for _, m := range Messages {
		gob.Register(m)
	}
}

// SocketPath is $XDG_RUNTIME_DIR/pitwall/pitwall.sock, or
// /tmp/pitwall-<uid>/pitwall.sock without XDG_RUNTIME_DIR. It creates the
// directory with mode 0700 and rejects unsafe existing directories.
func SocketPath() (string, error) {
	dir := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "pitwall")
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		dir = fmt.Sprintf("/tmp/pitwall-%d", os.Getuid())
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create socket directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", fmt.Errorf("inspect socket directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("socket directory %s must be a directory, not a symlink", dir)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return "", fmt.Errorf("socket directory %s must belong to uid %d", dir, os.Getuid())
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("socket directory %s must have no group or other permissions", dir)
	}
	return filepath.Join(dir, "pitwall.sock"), nil
}

// envelope carries one message; gob needs a concrete struct around the
// interface value.
type envelope struct{ M any }

const maxFrameSize = 64 << 20

// Conn frames gob-encoded envelopes with a uint32 big-endian byte length.
// Send takes message values (Hello{}, not &Hello{}) and is safe for
// concurrent use; Recv is not. Recv returns message values.
type Conn struct {
	c   net.Conn
	mu  sync.Mutex
	out bytes.Buffer
	in  bytes.Reader
	enc *gob.Encoder
	dec *gob.Decoder
}

func NewConn(c net.Conn) *Conn {
	conn := &Conn{c: c}
	// Keep the gob type registry across frames. bytes.Reader implements
	// io.ByteReader, so the decoder does not buffer bytes from another frame.
	conn.enc = gob.NewEncoder(&conn.out)
	conn.dec = gob.NewDecoder(&conn.in)
	return conn
}

// Dial connects to the daemon. It sends nothing: the caller sends
// Hello{Version: Version, Kind: ...} first, because only the caller knows its
// Kind. The daemon answers anything else first with Error and closes.
func Dial(path string) (*Conn, error) {
	c, err := net.Dial("unix", path)
	if err != nil {
		return nil, err
	}
	return NewConn(c), nil
}

func (c *Conn) Send(msg any) (err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer func() {
		// Unsent type definitions invalidate the encoder's registry for a retry.
		if err != nil {
			c.Close()
		}
	}()
	c.out.Reset()
	c.out.Write([]byte{0, 0, 0, 0})
	if err := c.enc.Encode(envelope{msg}); err != nil {
		return err
	}
	size := c.out.Len() - 4
	if size > maxFrameSize {
		return fmt.Errorf("frame size %d exceeds limit %d", size, maxFrameSize)
	}
	binary.BigEndian.PutUint32(c.out.Bytes()[:4], uint32(size))
	_, err = c.out.WriteTo(c.c)
	return err
}

func (c *Conn) Recv() (any, error) {
	var header [4]byte
	if _, err := io.ReadFull(c.c, header[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size > maxFrameSize {
		return nil, fmt.Errorf("frame size %d exceeds limit %d", size, maxFrameSize)
	}
	body := make([]byte, int(size))
	if _, err := io.ReadFull(c.c, body); err != nil {
		return nil, err
	}
	c.in.Reset(body)
	var e envelope
	if err := c.dec.Decode(&e); err != nil {
		return nil, err
	}
	return e.M, nil
}

func (c *Conn) Close() error { return c.c.Close() }
