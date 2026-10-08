package config

import (
	"fmt"
	"net"
	"strconv"
)

// Remote is [remote]: the page a paired phone answers agents from.
type Remote struct {
	Enabled *bool  `toml:"enabled" doc:"Serve a page where a paired phone sees which agents need you and answers them: Allow or Deny on a permission prompt, a reply to a question. Pair a phone with pitwall remote pair or in Settings. Off by default."`
	Listen  string `toml:"listen" doc:"Address to serve on. The default, 127.0.0.1:7777, is reachable from this machine only: put it on your tailnet with tailscale serve, or reach it through an SSH tunnel. Any other address, such as 0.0.0.0:7777, needs tls = true."`
	TLS     *bool  `toml:"tls" doc:"Serve HTTPS with a certificate pitwall makes for itself and keeps in its state folder. pitwall remote pair prints its fingerprint to compare with what the phone shows. Required for an address other than loopback."`
	URL     string `toml:"url" doc:"The address your phone opens, which the pairing QR code holds, such as https://laptop.tail1234.ts.net from tailscale serve. \"\" uses the listen address."`
}

// DefaultRemoteListen is [remote] listen's default: loopback only.
const DefaultRemoteListen = "127.0.0.1:7777"

// RemoteSettings is [remote] with every value filled in.
type RemoteSettings struct {
	Enabled bool
	Listen  string
	TLS     bool
	URL     string // "" when unset
}

func defaultRemote() Remote {
	off := false
	return Remote{Enabled: &off, Listen: DefaultRemoteListen, TLS: &off}
}

// resolveRemote fills in defaults. A listen address that is not a
// host:port, or one off loopback without TLS, is a problem that keeps the
// page off.
func resolveRemote(c Remote) (RemoteSettings, []issue) {
	r := RemoteSettings{Enabled: c.Enabled != nil && *c.Enabled, Listen: c.Listen, TLS: c.TLS != nil && *c.TLS, URL: c.URL}
	if r.Listen == "" {
		r.Listen = DefaultRemoteListen
	}
	host, port, err := net.SplitHostPort(r.Listen)
	if n, perr := strconv.Atoi(port); err == nil && (perr != nil || n < 1 || n > 65535) {
		err = fmt.Errorf("bad port %q", port)
	}
	var issues []issue
	switch {
	case err != nil:
		issues = append(issues, issue{"remote.listen", fmt.Sprintf("%q is not host:port (%v); the phone page stays off", r.Listen, err)})
		r.Enabled = false
	case !r.TLS && !Loopback(host):
		issues = append(issues, issue{"remote.listen", fmt.Sprintf("%q is reachable from other machines, so it needs tls = true; the phone page stays off. "+
			"Or keep 127.0.0.1 and use tailscale serve or an SSH tunnel", r.Listen)})
		r.Enabled = false
	}
	return r, issues
}

// Loopback reports whether host, from a listen address, reaches this
// machine only: localhost or a loopback IP. "" and 0.0.0.0 mean every
// interface, so they are not.
func Loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// LoadRemote reads only [remote] from the config at path, for the daemon.
// Problems are the GUI's and `config check`'s to report.
func LoadRemote(path string) RemoteSettings {
	s, _ := LoadFile(path)
	return s.Remote
}
