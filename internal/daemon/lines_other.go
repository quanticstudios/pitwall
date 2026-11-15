//go:build !linux

package daemon

import "net"

// jsonClient is false: the socket speaks JSON lines on Linux only.
func jsonClient(net.Conn) (bool, error) { return false, nil }
