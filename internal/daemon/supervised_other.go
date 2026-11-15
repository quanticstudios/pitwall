//go:build !linux

package daemon

// supervised: only Linux has systemd units.
var supervised = func() (unit string, user bool) { return "", false }
