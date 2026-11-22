package tsnet

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	tsnetlib "tailscale.com/tsnet"

	"github.com/quanticstudios/pitwall/internal/logs"
	"github.com/quanticstudios/pitwall/internal/remote"
)

// Config configures a pitwall built-in Tailscale node.
type Config struct {
	Dir        string   // isolated tsnet state directory (mode 0700)
	Hostname   string   // device name on the tailnet, e.g. "pitwall-mybox"
	AuthKey    string   // optional auth key from config (env fallback resolved if empty)
	AllowUsers []string // additional allowed Tailscale user logins
	AllowTags  []string // allowed Tailscale node tags
	ControlURL string   // optional coordination server URL (empty uses Tailscale default; tests set testcontrol)

	// OnStatus is called whenever the node's status changes (starting,
	// needs_login, running, stopping, off, error).
	OnStatus func(remote.TailscaleStatus)

	// TLSConfig, when non-nil, overrides the default Tailscale certificate
	// lookup (used by tests with a local test control server).
	TLSConfig *tls.Config
	// PlainHTTPForTest serves plain HTTP on :80 instead of requiring HTTPS
	// certs (used only in unit tests against testcontrol without ACME).
	PlainHTTPForTest bool
}

// ResolveAuthKey returns the first non-empty auth key from config,
// PITWALL_TAILSCALE_AUTHKEY, or TS_AUTHKEY.
func ResolveAuthKey(configKey string) string {
	if k := strings.TrimSpace(configKey); k != "" {
		return k
	}
	if k := strings.TrimSpace(os.Getenv("PITWALL_TAILSCALE_AUTHKEY")); k != "" {
		return k
	}
	return strings.TrimSpace(os.Getenv("TS_AUTHKEY"))
}

// Node runs an in-process tsnet.Server serving a remote.Server over HTTPS
// with Tailscale identity authorization.
type Node struct {
	cfg Config
	ts  *tsnetlib.Server
	lc  *local.Client

	mu       sync.RWMutex
	policy   Policy
	peers    map[string]Peer // deviceID -> Peer for live stream re-checks
	status   remote.TailscaleStatus
	started  bool
	closed   bool
	srv      *http.Server
	redirSrv *http.Server

	cancel context.CancelFunc
	done   chan struct{}
}

// Start launches the in-process tsnet node in the background and begins
// serving handler once the node reaches Running on the tailnet.
//
// The node runs purely in userspace (fake TUN + gVisor netstack, ephemeral
// UDP port, isolated Dir) so it never disturbs a system or userspace
// tailscaled running on the same machine.
func Start(cfg Config, buildHandler func(authorize func(*http.Request) (remote.Device, int, string), deviceLive func(remote.Device) bool) http.Handler) (*Node, error) {
	if cfg.Dir == "" {
		return nil, errors.New("tsnet: state directory is empty")
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("tsnet: state dir: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	n := &Node{
		cfg: cfg,
		policy: Policy{
			AllowUsers: append([]string(nil), cfg.AllowUsers...),
			AllowTags:  append([]string(nil), cfg.AllowTags...),
		},
		peers:  map[string]Peer{},
		cancel: cancel,
		done:   make(chan struct{}),
	}
	n.ts = &tsnetlib.Server{
		Dir:        cfg.Dir,
		Hostname:   cfg.Hostname,
		AuthKey:    ResolveAuthKey(cfg.AuthKey),
		ControlURL: cfg.ControlURL,
		Logf:       func(string, ...any) {}, // keep daemon.log clean of wireguard/magicsock noise
		UserLogf: func(format string, args ...any) {
			msg := logs.Redact(fmt.Sprintf(format, args...))
			log.Printf("remote: tailscale: %s", msg)
		},
	}
	n.setStatus(remote.TailscaleStatus{
		Enabled:  true,
		State:    remote.TailscaleStarting,
		Hostname: cfg.Hostname,
		PID:      os.Getpid(),
	})
	if err := n.ts.Start(); err != nil {
		cancel()
		n.setStatus(remote.TailscaleStatus{
			Enabled:  true,
			State:    remote.TailscaleError,
			Hostname: cfg.Hostname,
			Error:    err.Error(),
			PID:      os.Getpid(),
		})
		return nil, fmt.Errorf("tsnet: start: %w", err)
	}
	lc, err := n.ts.LocalClient()
	if err != nil {
		cancel()
		_ = n.ts.Close()
		n.setStatus(remote.TailscaleStatus{
			Enabled:  true,
			State:    remote.TailscaleError,
			Hostname: cfg.Hostname,
			Error:    err.Error(),
			PID:      os.Getpid(),
		})
		return nil, fmt.Errorf("tsnet: local client: %w", err)
	}
	n.lc = lc
	n.mu.Lock()
	n.started = true
	n.mu.Unlock()

	handler := buildHandler(n.Authorize, n.DeviceLive)
	go n.run(ctx, handler)
	return n, nil
}

// UpdatePolicy updates the live allow_users and allow_tags without restarting
// the tsnet node.
func (n *Node) UpdatePolicy(allowUsers, allowTags []string) {
	n.mu.Lock()
	n.policy.AllowUsers = append([]string(nil), allowUsers...)
	n.policy.AllowTags = append([]string(nil), allowTags...)
	n.mu.Unlock()
}

// Status returns the node's most recent status snapshot.
func (n *Node) Status() remote.TailscaleStatus {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.status
}

// Server returns the underlying tsnet.Server (for tests).
func (n *Node) Server() *tsnetlib.Server { return n.ts }

// Authorize checks a request's Tailscale identity via LocalClient.WhoIs
// against the current Policy.
func (n *Node) Authorize(r *http.Request) (remote.Device, int, string) {
	who, err := n.lc.WhoIs(r.Context(), r.RemoteAddr)
	if err != nil {
		log.Printf("remote: tailscale: WhoIs(%s): %v", r.RemoteAddr, err)
		return remote.Device{}, http.StatusForbidden, "could not verify Tailscale identity"
	}
	peer := PeerFromWhoIs(who)
	n.mu.RLock()
	pol := n.policy
	n.mu.RUnlock()
	ok, reason := pol.Check(peer)
	if !ok {
		log.Printf("remote: tailscale: denied %q (%s): %s", peer.DeviceName(), r.RemoteAddr, reason)
		return remote.Device{}, http.StatusForbidden, reason
	}
	dev := remote.Device{
		ID:        peer.DeviceID(),
		Name:      peer.DeviceName(),
		Tailscale: true,
	}
	n.mu.Lock()
	n.peers[dev.ID] = peer
	n.mu.Unlock()
	return dev, 0, ""
}

// DeviceLive re-checks a Tailscale device against the live Policy (and falls
// back to paired-device lookup for non-Tailscale devices).
func (n *Node) DeviceLive(d remote.Device) bool {
	if !d.Tailscale {
		return true // caller checks Paired for non-Tailscale devices
	}
	n.mu.RLock()
	if n.closed {
		n.mu.RUnlock()
		return false
	}
	peer, ok := n.peers[d.ID]
	pol := n.policy
	n.mu.RUnlock()
	if !ok {
		return false
	}
	allowed, _ := pol.Check(peer)
	return allowed
}

// Stop shuts down the HTTP server and closes the tsnet node. When logout is
// true (user explicitly turned Tailscale off), it logs the node out of the
// tailnet first so the device is removed cleanly. When logout is false
// (daemon exit / restart / hostname change), state is preserved.
func (n *Node) Stop(logout bool) error {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return nil
	}
	n.closed = true
	srv := n.srv
	redir := n.redirSrv
	n.mu.Unlock()

	n.cancel()
	if srv != nil {
		_ = srv.Close()
	}
	if redir != nil {
		_ = redir.Close()
	}
	<-n.done

	var logoutErr error
	if logout && n.lc != nil {
		n.setStatus(remote.TailscaleStatus{
			Enabled:  false,
			State:    remote.TailscaleStopping,
			Hostname: n.cfg.Hostname,
			PID:      os.Getpid(),
		})
		lctx, lcancel := context.WithTimeout(context.Background(), 10*time.Second)
		logoutErr = n.lc.Logout(lctx)
		lcancel()
		if logoutErr != nil {
			log.Printf("remote: tailscale: logout: %v", logoutErr)
		} else {
			log.Printf("remote: tailscale: logged out %q from tailnet", n.cfg.Hostname)
		}
	}

	closeErr := n.ts.Close()
	n.setStatus(remote.TailscaleStatus{
		Enabled:  !logout,
		State:    remote.TailscaleOff,
		Hostname: n.cfg.Hostname,
		PID:      os.Getpid(),
	})
	if logoutErr != nil {
		return logoutErr
	}
	return closeErr
}

// LogoutDir logs out a saved tsnet state directory when no daemon is running.
func LogoutDir(ctx context.Context, dir, hostname, controlURL string) error {
	if dir == "" {
		return nil
	}
	s := &tsnetlib.Server{
		Dir:        dir,
		Hostname:   hostname,
		ControlURL: controlURL,
		Logf:       func(string, ...any) {},
		UserLogf:   func(string, ...any) {},
	}
	if err := s.Start(); err != nil {
		return err
	}
	defer s.Close()
	lc, err := s.LocalClient()
	if err != nil {
		return err
	}
	return lc.Logout(ctx)
}

func (n *Node) setStatus(st remote.TailscaleStatus) {
	st.UpdatedAt = time.Now().UTC()
	n.mu.Lock()
	n.status = st
	n.mu.Unlock()
	if n.cfg.OnStatus != nil {
		n.cfg.OnStatus(st)
	}
}

func (n *Node) run(ctx context.Context, handler http.Handler) {
	defer close(n.done)

	st, err := n.waitRunning(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		log.Printf("remote: tailscale: %v", err)
		n.setStatus(remote.TailscaleStatus{
			Enabled:  true,
			State:    remote.TailscaleError,
			Hostname: n.cfg.Hostname,
			Error:    err.Error(),
			PID:      os.Getpid(),
		})
		return
	}

	selfUser := SelfUserFromStatus(st)
	n.mu.Lock()
	n.policy.SelfUser = selfUser
	n.mu.Unlock()

	dnsName := ""
	if st.Self != nil {
		dnsName = strings.TrimSuffix(st.Self.DNSName, ".")
	}
	var ips []string
	for _, ip := range st.TailscaleIPs {
		ips = append(ips, ip.String())
	}

	var ln net.Listener
	var urlStr string
	if n.cfg.PlainHTTPForTest {
		ln, err = n.ts.Listen("tcp", ":80")
		if err == nil {
			host := dnsName
			if host == "" && len(ips) > 0 {
				host = ips[0]
			}
			urlStr = "http://" + host + "/"
		}
	} else {
		ln, err = n.listenHTTPS(st)
		if err == nil {
			host := dnsName
			if host == "" && len(st.CertDomains) > 0 {
				host = st.CertDomains[0]
			}
			urlStr = "https://" + host + "/"
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		log.Printf("remote: tailscale: listen: %v", err)
		n.setStatus(remote.TailscaleStatus{
			Enabled:  true,
			State:    remote.TailscaleError,
			Hostname: n.cfg.Hostname,
			DNSName:  dnsName,
			SelfUser: selfUser,
			IPs:      ips,
			Error:    err.Error(),
			PID:      os.Getpid(),
		})
		return
	}

	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	var redir *http.Server
	if !n.cfg.PlainHTTPForTest && dnsName != "" {
		if ln80, err := n.ts.Listen("tcp", ":80"); err == nil {
			redir = &http.Server{
				ReadHeaderTimeout: 10 * time.Second,
				Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, "https://"+dnsName+r.URL.RequestURI(), http.StatusMovedPermanently)
				}),
			}
			go redir.Serve(ln80)
		}
	}

	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		_ = ln.Close()
		if redir != nil {
			_ = redir.Close()
		}
		return
	}
	n.srv = srv
	n.redirSrv = redir
	n.mu.Unlock()

	log.Printf("remote: tailscale: serving %s (device %q, owner %q)", urlStr, n.cfg.Hostname, selfUser)
	n.setStatus(remote.TailscaleStatus{
		Enabled:  true,
		State:    remote.TailscaleRunning,
		Hostname: n.cfg.Hostname,
		DNSName:  dnsName,
		URL:      urlStr,
		SelfUser: selfUser,
		IPs:      ips,
		PID:      os.Getpid(),
	})

	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && ctx.Err() == nil {
		log.Printf("remote: tailscale: serve: %v", err)
		n.setStatus(remote.TailscaleStatus{
			Enabled:  true,
			State:    remote.TailscaleError,
			Hostname: n.cfg.Hostname,
			DNSName:  dnsName,
			SelfUser: selfUser,
			IPs:      ips,
			Error:    err.Error(),
			PID:      os.Getpid(),
		})
	}
}

func (n *Node) listenHTTPS(st *ipnstate.Status) (net.Listener, error) {
	if n.cfg.TLSConfig == nil {
		if st.CurrentTailnet == nil || !st.CurrentTailnet.MagicDNSEnabled {
			return nil, errors.New("MagicDNS is off on your tailnet; enable MagicDNS and HTTPS Certificates in the Tailscale admin console (https://tailscale.com/s/https)")
		}
		if len(st.CertDomains) == 0 {
			return nil, errors.New("HTTPS certificates are off on your tailnet; enable HTTPS Certificates in the Tailscale admin console (https://tailscale.com/s/https)")
		}
	}
	ln, err := n.ts.Listen("tcp", ":443")
	if err != nil {
		return nil, err
	}
	tlsCfg := n.cfg.TLSConfig
	if tlsCfg == nil {
		tlsCfg = &tls.Config{
			GetCertificate: n.lc.GetCertificate,
			MinVersion:     tls.VersionTLS12,
		}
	}
	return tls.NewListener(ln, tlsCfg), nil
}

// waitRunning watches the tsnet IPN bus until the node is Running, reporting
// NeedsLogin + AuthURL immediately so Settings and the CLI can display it.
func (n *Node) waitRunning(ctx context.Context) (*ipnstate.Status, error) {
	watcher, err := n.lc.WatchIPNBus(ctx, ipn.NotifyInitialState)
	if err != nil {
		return nil, fmt.Errorf("watch ipn bus: %w", err)
	}
	defer watcher.Close()

	lastAuthURL := ""
	reportAuth := func(url string) {
		if url == "" || url == lastAuthURL {
			return
		}
		lastAuthURL = url
		log.Printf("remote: tailscale: login required: %s", url)
		n.setStatus(remote.TailscaleStatus{
			Enabled:  true,
			State:    remote.TailscaleNeedsLogin,
			Hostname: n.cfg.Hostname,
			AuthURL:  url,
			PID:      os.Getpid(),
		})
	}

	for {
		notif, err := watcher.Next()
		if err != nil {
			return nil, err
		}
		if notif.ErrMessage != nil && *notif.ErrMessage != "" {
			return nil, errors.New(*notif.ErrMessage)
		}
		if notif.BrowseToURL != nil && *notif.BrowseToURL != "" {
			reportAuth(*notif.BrowseToURL)
		}
		if notif.State != nil {
			switch *notif.State {
			case ipn.NeedsLogin:
				if st, err := n.lc.StatusWithoutPeers(ctx); err == nil && st.AuthURL != "" {
					reportAuth(st.AuthURL)
				}
			case ipn.Running:
				st, err := n.lc.Status(ctx)
				if err != nil {
					return nil, err
				}
				if len(st.TailscaleIPs) == 0 {
					continue
				}
				return st, nil
			}
		}
	}
}
