package app

import "github.com/quanticstudios/pitwall/internal/proto"

// Host is the ssh host whose daemon the window shows, set by main for
// pitwall --host; "" is this machine. Paths in the state are the host's
// then, so the window reads no files, runs no git and opens no file links
// for them here.
var Host string

// Iconer is optionally implemented by a Backend: the daemon's answer to
// a proto.Favicon for dir, asked for the first time it is wanted.
type Iconer interface {
	Favicon(dir string) (proto.FaviconResult, bool)
}

// repoIcons is the sidebar's RepoIcons: the daemon's lookup when the
// repos are on Host, nil when they are on this machine's disk.
func (u *ui) repoIcons() func(dir string) (proto.FaviconResult, bool) {
	if ic, ok := u.b.(Iconer); ok && Host != "" {
		return ic.Favicon
	}
	return nil
}
