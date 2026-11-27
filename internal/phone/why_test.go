package phone

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
)

// A refused, unknown or silent address reads as words, not Go's dial error.
// The errors are built as net/http returns them: no test reaches the network.
func TestWhy(t *testing.T) {
	get := func(err error) error {
		return &url.Error{Op: "Get", URL: "http://box:7777/api/stream", Err: err}
	}
	dial := func(errno syscall.Errno) error {
		return get(&net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", errno)})
	}
	for _, tc := range []struct {
		err  error
		want string
	}{
		{dial(syscall.ECONNREFUSED), "nothing answers there"},
		{dial(syscall.EHOSTUNREACH), "the network cannot reach"},
		{get(&net.OpError{Op: "dial", Err: &net.DNSError{Name: "box", Err: "no such host"}}), "cannot find box"},
		{get(context.DeadlineExceeded), "the computer did not answer in time"},
		{get(io.ErrUnexpectedEOF), "the connection dropped"},
		{get(errors.New("tls: bad record")), "tls: bad record"},
		{errors.New("no chat p1"), "no chat p1"},
	} {
		if got := Why(tc.err); !strings.HasPrefix(got, tc.want) {
			t.Errorf("Why(%v) = %q, want %q…", tc.err, got, tc.want)
		}
	}
	if Why(nil) != "" {
		t.Error("nil")
	}
}
