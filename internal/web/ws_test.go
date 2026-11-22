package web

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCloseReasonClamped(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason string
	}{
		{"ascii-long", strings.Repeat("a", 300)},
		{"utf8-boundary", strings.Repeat("界", 50)}, // 150 bytes (3 bytes/rune)
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := net.Pipe()
			defer b.Close()
			c := newConn(a, bufio.NewReader(a), false)
			done := make(chan struct{})
			go func() {
				c.close(1008, tc.reason)
				close(done)
			}()
			var hdr [2]byte
			if _, err := io.ReadFull(b, hdr[:]); err != nil {
				t.Fatal(err)
			}
			if hdr[0] != 0x80|opClose {
				t.Fatalf("opcode byte %#x, want %#x", hdr[0], 0x80|opClose)
			}
			if hdr[1]&0x80 != 0 {
				t.Fatalf("close frame length byte %#x has mask bit set", hdr[1])
			}
			n := int(hdr[1] & 0x7f)
			if n > 125 {
				t.Fatalf("control frame length %d exceeds RFC 6455 max 125", n)
			}
			payload := make([]byte, n)
			if _, err := io.ReadFull(b, payload); err != nil {
				t.Fatal(err)
			}
			<-done
			code := int(binary.BigEndian.Uint16(payload[:2]))
			reason := string(payload[2:])
			if code != 1008 {
				t.Fatalf("close code %d, want 1008", code)
			}
			if len(reason) > 123 || !utf8.ValidString(reason) || !strings.HasPrefix(tc.reason, reason) {
				t.Fatalf("reason len=%d valid=%v %q", len(reason), utf8.ValidString(reason), reason)
			}
		})
	}
}
