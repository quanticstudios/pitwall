package daemon

import (
	"errors"
	"net"

	"github.com/quanticstudios/pitwall/internal/proto"
)

var errNoHandoff = errors.New("daemon handoff is unsupported on Windows; restart instead")

func (d *Daemon) serveHandoff(nc net.Conn, conn *proto.Conn) {
	conn.Send(handoffAbort{Reason: errNoHandoff.Error()})
}

// Takeover is unsupported on Windows: the caller restarts the daemon.
func Takeover(path string, o Options) (*Daemon, net.Listener, error) {
	return nil, nil, errNoHandoff
}
