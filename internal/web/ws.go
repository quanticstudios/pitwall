package web

// The server side of RFC 6455, as much of it as the protocol needs: the
// upgrade, masked client frames, fragments, ping, close with a code and a
// reason. Stdlib only; the protocol's frames are small JSON objects.

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	wsGUID       = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	maxMessage   = 16 << 20
	writeTimeout = 30 * time.Second

	opContinue = 0x0
	opText     = 0x1
	opBinary   = 0x2
	opClose    = 0x8
	opPing     = 0x9
	opPong     = 0xA
)

// closeErr ends a connection with a WebSocket close code and a reason a
// person can read.
type closeErr struct {
	code   int
	reason string
}

func (e closeErr) Error() string { return e.reason }

var (
	errTooBig   = closeErr{1009, "frame over 16 MiB"}
	errProtocol = closeErr{1002, "protocol error"}
)

type conn struct {
	nc  net.Conn
	br  *bufio.Reader
	wmu sync.Mutex
}

// upgrade answers a WebSocket upgrade request and takes the connection
// over from net/http.
func upgrade(w http.ResponseWriter, r *http.Request) (*conn, error) {
	key := r.Header.Get("Sec-WebSocket-Key")
	if k, err := base64.StdEncoding.DecodeString(key); err != nil || len(k) != 16 ||
		!headerHas(r.Header, "Connection", "upgrade") || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") ||
		r.Header.Get("Sec-WebSocket-Version") != "13" {
		w.Header().Set("Sec-WebSocket-Version", "13")
		http.Error(w, "not a WebSocket upgrade", http.StatusBadRequest)
		return nil, errors.New("not a WebSocket upgrade")
	}
	nc, brw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, err
	}
	sum := sha1.Sum([]byte(key + wsGUID))
	nc.SetDeadline(time.Time{}) // drop the server's header timeout
	c := &conn{nc: nc, br: brw.Reader}
	_, err = c.writeRaw([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " +
		base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n"))
	if err != nil {
		nc.Close()
		return nil, err
	}
	return c, nil
}

// headerHas reports whether the comma-separated header name holds token.
func headerHas(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for t := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}

// read returns the next text or binary message, answering pings on the
// way. A close from the client is a closeErr with code 1000.
func (c *conn) read() ([]byte, error) {
	var msg []byte
	inMsg := false
	for {
		var h [2]byte
		if _, err := io.ReadFull(c.br, h[:]); err != nil {
			return nil, err
		}
		fin, op, n := h[0]&0x80 != 0, h[0]&0x0f, uint64(h[1]&0x7f)
		if h[0]&0x70 != 0 || h[1]&0x80 == 0 { // reserved bits, or a client frame not masked
			return nil, errProtocol
		}
		switch n {
		case 126:
			var b [2]byte
			if _, err := io.ReadFull(c.br, b[:]); err != nil {
				return nil, err
			}
			n = uint64(binary.BigEndian.Uint16(b[:]))
		case 127:
			var b [8]byte
			if _, err := io.ReadFull(c.br, b[:]); err != nil {
				return nil, err
			}
			n = binary.BigEndian.Uint64(b[:])
		}
		control := op >= opClose
		if control && (n > 125 || !fin) {
			return nil, errProtocol
		}
		if n > maxMessage-uint64(len(msg)) { // checked before anything is allocated
			return nil, errTooBig
		}
		var mask [4]byte
		if _, err := io.ReadFull(c.br, mask[:]); err != nil {
			return nil, err
		}
		p := make([]byte, n)
		if _, err := io.ReadFull(c.br, p); err != nil {
			return nil, err
		}
		for i := range p {
			p[i] ^= mask[i%4]
		}
		switch {
		case op == opClose:
			return nil, closeErr{1000, ""}
		case op == opPing:
			if err := c.write(opPong, p); err != nil {
				return nil, err
			}
		case op == opPong:
		case (op == opText || op == opBinary) && !inMsg, op == opContinue && inMsg:
			msg, inMsg = append(msg, p...), true
			if fin {
				return msg, nil
			}
		default:
			return nil, errProtocol
		}
	}
}

// write sends one unfragmented frame. A client that takes more than
// writeTimeout to read it loses the connection.
func (c *conn) write(op byte, p []byte) error {
	hdr := []byte{0x80 | op}
	switch n := len(p); {
	case n < 126:
		hdr = append(hdr, byte(n))
	case n <= 0xffff:
		hdr = binary.BigEndian.AppendUint16(append(hdr, 126), uint16(n))
	default:
		hdr = binary.BigEndian.AppendUint64(append(hdr, 127), uint64(n))
	}
	_, err := c.writeRaw(hdr, p)
	return err
}

func (c *conn) writeRaw(bufs ...[]byte) (int64, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.nc.SetWriteDeadline(time.Now().Add(writeTimeout))
	b := net.Buffers(bufs)
	return b.WriteTo(c.nc)
}

// close sends a close frame with code and reason, and closes the
// connection without waiting for the client's.
func (c *conn) close(code int, reason string) {
	c.write(opClose, append(binary.BigEndian.AppendUint16(nil, uint16(code)), reason...))
	c.nc.Close()
}
