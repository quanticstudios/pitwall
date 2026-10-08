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
	"strconv"
	"strings"
	"sync"
)

func init() {
	for _, m := range Messages {
		gob.Register(m)
	}
}

// SocketPath is $XDG_RUNTIME_DIR/pitwall/pitwall.sock, or without
// XDG_RUNTIME_DIR /tmp/pitwall-<uid>/pitwall.sock (%LOCALAPPDATA%\pitwall
// on Windows). It creates the directory with mode 0700 and rejects unsafe
// existing directories.
func SocketPath() (string, error) {
	dir := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "pitwall")
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		dir = runtimeDir()
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
	if err := private(dir, info); err != nil {
		return "", err
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

// Recv returns the next message, or Unknown for a type this build does not
// know; the connection stays usable after an Unknown.
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
		// why: gob has no typed error for this. It reads the type
		// definitions in the frame before it looks the name up, so the
		// decoder keeps every type it was sent, and the next frame starts
		// clean.
		if _, name, ok := strings.Cut(err.Error(), unregistered); ok {
			if n, qerr := strconv.Unquote(name); qerr == nil {
				return Unknown{Name: n}, nil
			}
		}
		return nil, err
	}
	return e.M, nil
}

// unregistered starts the gob error for an interface value of a type the
// decoder has no Register for.
const unregistered = "name not registered for interface: "

func (c *Conn) Close() error { return c.c.Close() }
