package phone

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"syscall"
)

// Why is err in words for the app's lines: a network failure says what
// happened, not Go's "Get \"https://…\": dial tcp …"; any other error is
// its own text.
func Why(err error) string {
	var dns *net.DNSError
	var ne net.Error
	switch {
	case err == nil:
		return ""
	case errors.Is(err, syscall.ECONNREFUSED):
		return "nothing answers there: is pitwall's phone page on?"
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return "the network cannot reach the computer"
	case errors.As(err, &dns):
		return "cannot find " + dns.Name + " on the network"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "the computer did not answer in time"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, syscall.ECONNRESET):
		return "the connection dropped"
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err.Error()
	}
	return err.Error()
}
