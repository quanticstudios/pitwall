package proto

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

func init() {
	for _, m := range Messages {
		gob.Register(m)
	}
}

// SocketPath is $XDG_RUNTIME_DIR/pitwall/pitwall.sock, or without
// XDG_RUNTIME_DIR, an owned /run/user/<uid>/pitwall on Linux, else
// /tmp/pitwall-<uid>/pitwall.sock (%LOCALAPPDATA%\pitwall on Windows).
// A running daemon in /tmp keeps its path. It creates the directory with
// mode 0700 and rejects unsafe existing directories.
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

const (
	maxFrameSize = 64 << 20
	maxRetainCap = 256 << 10
)

// Conn frames gob-encoded envelopes with a uint32 big-endian byte length.
// Send takes message values (Hello{}, not &Hello{}) and is safe for
// concurrent use; Recv is not. Recv returns message values.
type Conn struct {
	c     net.Conn
	r     io.Reader
	mu    sync.Mutex
	out   bytes.Buffer
	in    bytes.Reader
	inBuf []byte
	enc   *gob.Encoder
	dec   *gob.Decoder
}

func newConn(c net.Conn, r io.Reader) *Conn {
	conn := &Conn{c: c, r: r}
	// Keep the gob type registry across frames. bytes.Reader implements
	// io.ByteReader, so the decoder does not buffer bytes from another frame.
	conn.enc = gob.NewEncoder(&conn.out)
	conn.dec = gob.NewDecoder(&conn.in)
	return conn
}

func NewConn(c net.Conn) *Conn {
	return newConn(c, bufio.NewReaderSize(c, 32<<10))
}

// NewUnbufferedConn wraps c without read-ahead buffering, for handoff
// connections that interleave SCM_RIGHTS descriptors on the same socket.
func NewUnbufferedConn(c net.Conn) *Conn {
	return newConn(c, c)
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
		if err == nil {
			return
		}
		// A peer that is gone may have said why before it left, a version
		// refusal for one: keep the read side so Recv still returns that.
		if w, ok := c.c.(interface{ CloseWrite() error }); ok && errors.Is(err, syscall.EPIPE) {
			w.CloseWrite()
			return
		}
		c.Close()
	}()
	c.out.Reset()
	c.out.Write([]byte{0, 0, 0, 0})
	if err := c.enc.Encode(envelope{msg}); err != nil {
		return err
	}
	size := c.out.Len() - 4
	if size > maxFrameSize {
		// why: the connection closes over it, and nothing else would say why.
		log.Printf("dropped a %T of %d bytes: over the %d byte frame limit; closing the connection", msg, size, maxFrameSize)
		return fmt.Errorf("frame size %d exceeds limit %d", size, maxFrameSize)
	}
	binary.BigEndian.PutUint32(c.out.Bytes()[:4], uint32(size))
	_, err = c.out.WriteTo(c.c)
	// Release oversized send buffers (e.g. Kitty images or large ChatDeltas)
	// without replacing c.enc, which retains the gob type registry.
	if c.out.Cap() > maxRetainCap {
		c.out = bytes.Buffer{}
	}
	return err
}

// Recv returns the next message, or Unknown for a type this build does not
// know; the connection stays usable after an Unknown.
func (c *Conn) Recv() (any, error) {
	var header [4]byte
	if _, err := io.ReadFull(c.r, header[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size > maxFrameSize {
		return nil, fmt.Errorf("frame size %d exceeds limit %d", size, maxFrameSize)
	}
	n := int(size)
	if cap(c.inBuf) < n {
		c.inBuf = make([]byte, n)
	}
	body := c.inBuf[:n]
	if _, err := io.ReadFull(c.r, body); err != nil {
		return nil, err
	}
	c.in.Reset(body)
	var e envelope
	err := c.dec.Decode(&e)
	c.in.Reset(nil)
	if cap(c.inBuf) > maxRetainCap {
		c.inBuf = nil
	}
	if err != nil {
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
