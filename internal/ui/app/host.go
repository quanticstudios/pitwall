package app

import (
	"cmp"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
)

// Host is the ssh host whose daemon the window shows, set by main for
// pitwall --host; "" is this machine. Paths in the state are the host's
// then, so the window reads no files, runs no git and opens no file links
// for them here.
var Host string

// shownHost is the host of the session a window of several hosts shows,
// "" for this machine. The window's goroutine sets it every frame and is
// the only one to read it.
var shownHost string

// curHost is the host the window's session is on: Host, else shownHost.
// Paths, repos and agents of a session there are not on this machine.
func curHost() string {
	if Host != "" {
		return Host
	}
	return shownHost
}

// Hoster is optionally implemented by a Backend whose sessions are on
// several machines: this one and the [[hosts]] with window = true.
type Hoster interface {
	SessionHost(session string) string // "" for this machine
	Hosts() []sidebar.HostInfo         // this machine first
	// HostOf is the host of a tab, group or pane id, "" for this machine.
	// A shared session's tabs are on several.
	HostOf(id string) string
}

// NewTabOn opens Tab on Host, "" for this machine, through a Hoster
// backend. Tab.SessionID is the window's session; on another host the
// backend opens it in that host's part of the shared session, which it
// makes first when there is none.
type NewTabOn struct {
	Host string
	Tab  proto.NewSession
}

// fromTab is a tab that tab opens for itself, as Create PR, Merge PR and
// Rerun checks do. send opens it on tab's host: in a shared session an
// ungrouped tab of another host names no id of that host, and the command
// must run where the tab's checkout is.
type fromTab struct {
	tab string
	ns  proto.NewSession
}

// open is f as the backend takes it.
func (f fromTab) open(b Backend) any {
	if h, ok := b.(Hoster); ok {
		if host := h.HostOf(f.tab); host != "" {
			return NewTabOn{Host: host, Tab: f.ns}
		}
	}
	return f.ns
}

// hostOf is the host of a tab, group or pane id, "" for this machine.
func (u *ui) hostOf(id string) string {
	if h, ok := u.b.(Hoster); ok {
		return h.HostOf(id)
	}
	return Host
}

// showHost sets shownHost to the host of the session the window shows.
// In a shared session it is the shown tab's host, so folders, links and
// repos of a tab on another host are not looked for here.
func (u *ui) showHost() {
	if _, ok := u.b.(Hoster); ok {
		shownHost = cmp.Or(u.sessionHost(u.nav.session), u.hostOf(u.nav.workspace))
	}
}

// hostPrefix is "<host> · " for a session on another machine in a window
// of several machines, so the switcher and notifications say where it is.
func hostPrefix(b Backend, session string) string {
	if h, ok := b.(Hoster); ok {
		if name := h.SessionHost(session); name != "" {
			return name + " · "
		}
	}
	return ""
}

// sessionHost is the host session is on, "" for this machine.
func (u *ui) sessionHost(session string) string {
	if h, ok := u.b.(Hoster); ok {
		return h.SessionHost(session)
	}
	return Host
}

// Iconer is optionally implemented by a Backend: the daemon's answer to
// a proto.Favicon for dir, asked for the first time it is wanted.
type Iconer interface {
	Favicon(dir string) (proto.FaviconResult, bool)
}

// repoIcons is the sidebar's RepoIcons: the daemon's lookup when the
// repos are on Host, nil when they are on this machine's disk.
func (u *ui) repoIcons() func(dir string) (proto.FaviconResult, bool) {
	if ic, ok := u.b.(Iconer); ok && curHost() != "" {
		return ic.Favicon
	}
	return nil
}

// Numberer is optionally implemented by a Backend of several machines:
// a tab's number as its host's pitwall ls gives it, "box:3" on another
// host.
type Numberer interface {
	TabNumber(id string) string
}
