package web

// The web protocol as JSON lines, for the daemon's own socket
// (docs/web-protocol.md, The local socket): one frame per line each way,
// a hello whose token is optional and ignored, and a close line where
// WebSocket sends a close frame.

import (
	"bufio"
	"bytes"
	"net"
)

// ServeLines serves the web protocol on nc as JSON lines until the
// connection ends. It checks no token: whoever can open nc is trusted, as
// the daemon's socket trusts every client in its private folder.
func (s *Server) ServeLines(nc net.Conn) {
	s.run(newConn(nc, bufio.NewReader(nc), true), "the socket")
}

// readLine returns the next line that is not blank, without its newline.
// A line over 16 MiB is refused before it is all read.
func (c *conn) readLine() ([]byte, error) {
	var line []byte
	for {
		p, err := c.br.ReadSlice('\n')
		if len(line)+len(p) > maxMessage+1 {
			return nil, errTooBig
		}
		line = append(line, p...)
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(line)) == 0 {
			line = line[:0]
			continue
		}
		return line[:len(line)-1], nil
	}
}
