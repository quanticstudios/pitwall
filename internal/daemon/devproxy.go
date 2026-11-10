package daemon

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/logs"
	"github.com/quanticstudios/pitwall/internal/model"
)

func (d *Daemon) worktreeEnv(w model.Workspace) []string {
	env := portEnv(w.Ports)
	if u := w.DevURL(d.o.DevProxy.URLPort()); u != "" {
		env = append(env, "PITWALL_URL="+u)
	}
	return env
}

func (d *Daemon) devProxyLoop(ctx context.Context) {
	watchListener(ctx, func() config.DevProxySettings { return d.o.DevProxy },
		func(s config.DevProxySettings) bool { return s.Enabled },
		func(config.DevProxySettings) func() { return d.serveDevProxy() })
}

var devProxyFails = logs.Limiter{Every: time.Minute}

// serveDevProxy starts the loopback listener, retrying bind failures on the loop.
func (d *Daemon) serveDevProxy() func() {
	if !d.o.DevProxy.Enabled {
		return nil
	}
	ln, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(d.o.DevProxy.Port)))
	if err != nil {
		if ok, _ := devProxyFails.Allow("listen", time.Now()); ok {
			log.Printf("dev proxy: %q", err)
		}
		return nil
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		dialer := net.Dialer{Timeout: 2 * time.Second}
		c, err := dialer.DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
		if err != nil {
			return dialer.DialContext(ctx, network, net.JoinHostPort("::1", port))
		}
		return c, nil
	}}
	proxy := &httputil.ReverseProxy{Transport: transport, Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(&url.URL{Scheme: "http", Host: net.JoinHostPort("localhost", strconv.Itoa(r.In.Context().Value(devPortKey{}).(int)))})
		r.Out.Host = r.In.Host
		r.SetXForwarded()
	}}
	var mu sync.Mutex
	conns := map[net.Conn]bool{}
	handler := d.devHandler(proxy)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				mu.Lock()
				delete(conns, r.Context().Value(devConnKey{}).(net.Conn))
				mu.Unlock()
			}()
			handler.ServeHTTP(w, r)
		}),
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return context.WithValue(ctx, devConnKey{}, c)
		},
		ConnState: func(c net.Conn, state http.ConnState) {
			if state == http.StateHijacked {
				mu.Lock()
				conns[c] = true
				mu.Unlock()
			}
		}}
	go srv.Serve(ln)
	return func() {
		srv.Close()
		mu.Lock()
		defer mu.Unlock()
		for c := range conns {
			c.Close() // Server.Close leaves WebSocket connections open.
		}
		transport.CloseIdleConnections()
	}
}

type devPortKey struct{}
type devConnKey struct{}

func (d *Daemon) devHandler(proxy http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(r.Host)
		if strings.Contains(host, ":") {
			var err error
			host, _, err = net.SplitHostPort(host)
			if err != nil {
				http.Error(w, "Expected a .localhost Host", http.StatusForbidden)
				return
			}
		}
		labels := strings.Split(host, ".")
		valid := len(labels) >= 2 && labels[len(labels)-1] == "localhost"
		for _, label := range labels {
			valid = valid && len(label) > 0 && len(label) <= 63 && label[0] != '-' && label[len(label)-1] != '-'
			for _, c := range label {
				valid = valid && (c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-')
			}
		}
		if !valid {
			http.Error(w, "Expected a .localhost Host", http.StatusForbidden)
			return
		}
		d.mu.Lock()
		var names []string
		port, matches := 0, 0
		for _, tab := range d.st.Workspaces {
			name := tab.DevHost()
			if name == "" {
				continue
			}
			names = append(names, name)
			if host == name {
				matches++
				if open := d.st.Listening[tab.ID]; len(open) > 0 {
					port = open[0]
				}
			} else if prefix, ok := strings.CutSuffix(host, "."+name); ok {
				n, err := strconv.Atoi(prefix)
				if err == nil && n >= 0 && n <= tab.Ports.Last-tab.Ports.First && strconv.Itoa(n) == prefix {
					matches++
					port = tab.Ports.First + n
				}
			}
		}
		d.mu.Unlock()
		if matches > 1 {
			http.Error(w, "More than one tab has this name", http.StatusConflict)
			return
		}
		if matches == 0 {
			slices.Sort(names)
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintln(w, "Unknown worktree. Known names:")
			for _, name := range slices.Compact(names) {
				fmt.Fprintln(w, name)
			}
			return
		}
		if port == 0 {
			http.Error(w, "No dev server is listening in this tab's port block", http.StatusServiceUnavailable)
			return
		}
		if port == d.o.DevProxy.Port {
			http.Error(w, "The target is the proxy's own port", http.StatusBadGateway)
			return
		}
		proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), devPortKey{}, port)))
	})
}
