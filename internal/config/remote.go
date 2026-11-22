package config

import (
	"fmt"
	"net"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Remote is [remote]: the page a paired phone answers agents from.
type Remote struct {
	Enabled   *bool           `toml:"enabled" doc:"Serve a page where a paired phone sees which agents need you and answers them: Allow or Deny on a permission prompt, a reply to a question. Pair a phone with pitwall remote pair or in Settings. Off by default."`
	Listen    string          `toml:"listen" doc:"Address to serve on. The default, 127.0.0.1:7777, is reachable from this machine only: put it on your tailnet with tailscale serve, or reach it through an SSH tunnel. Any other address, such as 0.0.0.0:7777, needs tls = true."`
	TLS       *bool           `toml:"tls" doc:"Serve HTTPS with a certificate pitwall makes for itself and keeps in its state folder. pitwall remote pair prints its fingerprint to compare with what the phone shows. Required for an address other than loopback."`
	URL       string          `toml:"url" doc:"The address your phone opens, which the pairing QR code holds, such as https://laptop.tail1234.ts.net from tailscale serve. \"\" uses the listen address."`
	Tailscale RemoteTailscale `toml:"tailscale" doc:"Built-in Tailscale (tsnet) for the phone page: pitwall joins your tailnet as its own device and serves the phone page over HTTPS with Tailscale's certificate, checking each request's Tailscale identity without tailscale serve or device pairing. Off by default; turn on with pitwall remote tailscale on or in Settings, Phone."`
}

// RemoteTailscale is [remote.tailscale]: built-in Tailscale (tsnet) for the phone page.
type RemoteTailscale struct {
	Enabled    *bool    `toml:"enabled" doc:"Join your tailnet inside pitwall's background service and serve the phone page over HTTPS with Tailscale's certificate. Off by default; turn on with pitwall remote tailscale on or in Settings, Phone."`
	Hostname   string   `toml:"hostname" doc:"Device name on your tailnet (a DNS label, up to 63 letters, digits and hyphens). Empty uses pitwall-<hostname>."`
	AuthKey    string   `toml:"auth_key" doc:"Tailscale auth key for headless first login, used only when no node state exists yet. Empty reads PITWALL_TAILSCALE_AUTHKEY, then TS_AUTHKEY, from the background service's environment."`
	AllowUsers []string `toml:"allow_users" doc:"Additional Tailscale user logins (such as alice@example.com) allowed to reach the phone page. The tailnet user who logged pitwall in is always allowed."`
	AllowTags  []string `toml:"allow_tags" doc:"Tailscale node tags (such as tag:phones) allowed to reach the phone page."`
}

// DefaultRemoteListen is [remote] listen's default: loopback only.
const DefaultRemoteListen = "127.0.0.1:7777"

// RemoteSettings is [remote] with every value filled in.
type RemoteSettings struct {
	Enabled   bool
	Listen    string
	TLS       bool
	URL       string // "" when unset
	Tailscale RemoteTailscaleSettings
}

// RemoteTailscaleSettings is [remote.tailscale] with every value filled in.
type RemoteTailscaleSettings struct {
	Enabled    bool
	Hostname   string   // DNS label; DefaultTailscaleHostname() when unset
	AuthKey    string   // from config only; env fallback resolved at start
	AllowUsers []string // normalized lowercase, trimmed, deduplicated
	AllowTags  []string // normalized lowercase "tag:...", deduplicated
}

// Equal reports whether a and b carry the same resolved Tailscale settings.
func (a RemoteTailscaleSettings) Equal(b RemoteTailscaleSettings) bool {
	return a.Enabled == b.Enabled &&
		a.Hostname == b.Hostname &&
		a.AuthKey == b.AuthKey &&
		slices.Equal(a.AllowUsers, b.AllowUsers) &&
		slices.Equal(a.AllowTags, b.AllowTags)
}

var (
	tsLabelRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)
	tsTagRe   = regexp.MustCompile(`^tag:[a-zA-Z0-9][a-zA-Z0-9._-]*$`)
)

// DefaultTailscaleHostname returns the default tsnet device name for host
// ("pitwall-<host>"), sanitized to a valid DNS label. When host is empty,
// os.Hostname is used; if that is also empty or has no label characters,
// it returns "pitwall".
func DefaultTailscaleHostname(host string) string {
	if host == "" {
		host, _ = os.Hostname()
	}
	if i := strings.IndexByte(host, '.'); i >= 0 {
		host = host[:i]
	}
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(host) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	clean := strings.Trim(b.String(), "-")
	if clean == "" {
		return "pitwall"
	}
	name := "pitwall-" + clean
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-")
	}
	return name
}

func defaultRemote() Remote {
	off := false
	return Remote{
		Enabled: &off,
		Listen:  DefaultRemoteListen,
		TLS:     &off,
		Tailscale: RemoteTailscale{
			Enabled:    &off,
			AllowUsers: []string{},
			AllowTags:  []string{},
		},
	}
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
	ts, tsIssues := resolveRemoteTailscale(c.Tailscale)
	r.Tailscale = ts
	issues = append(issues, tsIssues...)
	return r, issues
}

func resolveRemoteTailscale(c RemoteTailscale) (RemoteTailscaleSettings, []issue) {
	ts := RemoteTailscaleSettings{
		Enabled:  c.Enabled != nil && *c.Enabled,
		Hostname: strings.TrimSpace(c.Hostname),
		AuthKey:  strings.TrimSpace(c.AuthKey),
	}
	var issues []issue
	if ts.Hostname == "" {
		ts.Hostname = DefaultTailscaleHostname("")
	} else if !tsLabelRe.MatchString(ts.Hostname) {
		issues = append(issues, issue{"remote.tailscale.hostname", fmt.Sprintf("%q is not a valid DNS label (letters, digits and hyphens, 1-63 chars); built-in Tailscale stays off", ts.Hostname)})
		ts.Enabled = false
	} else {
		ts.Hostname = strings.ToLower(ts.Hostname)
	}
	for i, u := range c.AllowUsers {
		u = strings.ToLower(strings.TrimSpace(u))
		if u == "" {
			issues = append(issues, issue{fmt.Sprintf("remote.tailscale.allow_users[%d]", i), "empty user login; ignored"})
			continue
		}
		if !slices.Contains(ts.AllowUsers, u) {
			ts.AllowUsers = append(ts.AllowUsers, u)
		}
	}
	for i, tag := range c.AllowTags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if !tsTagRe.MatchString(tag) {
			issues = append(issues, issue{fmt.Sprintf("remote.tailscale.allow_tags[%d]", i), fmt.Sprintf("%q is not a valid Tailscale tag (want tag:name); ignored", tag)})
			continue
		}
		if !slices.Contains(ts.AllowTags, tag) {
			ts.AllowTags = append(ts.AllowTags, tag)
		}
	}
	return ts, issues
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
