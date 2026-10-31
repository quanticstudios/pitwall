package web

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// SessionTTL is how long a browser session lasts at most.
const SessionTTL = 24 * time.Hour

// OpenTTL is how long a code from NewCode opens a session.
const OpenTTL = time.Minute

// Client serves a built web client, the bundle, to browsers that hold a
// session, and the bootstrap that tells the page where its WebSocket is
// and which token its hello sends.
//
// A session is a cookie, HttpOnly and SameSite=Strict, that never goes in
// a URL. The local listener starts one for a one-time code from
// NewCode, which pitwall web open puts in the URL it opens; the phone page
// starts one for a device that sends its device token (Start). The
// bootstrap answers {"url": "/ws", "token": ...} with a token of the
// session's own, never web.json's or a device's: the page keeps it in
// memory and sends it in hello.
type Client struct {
	Dir string // the bundle; "" serves none
	// Live reports whether the owner a session was started for may still
	// use it: the web.json token it started under, or a paired device's
	// id. A rotated token or a revoked device ends its sessions at once.
	Live func(owner string) bool

	mu       sync.Mutex
	sessions map[string]*session // by the cookie's hash
}

type session struct {
	owner   string
	token   string // the hello token
	expires time.Time
}

// Start starts a session for owner and sets its cookie on w.
func (c *Client) Start(w http.ResponseWriter, r *http.Request, owner string) {
	cookie, token := newToken(), newToken()
	now := time.Now()
	c.mu.Lock()
	if c.sessions == nil {
		c.sessions = map[string]*session{}
	}
	for k, s := range c.sessions {
		if now.After(s.expires) {
			delete(c.sessions, k)
		}
	}
	c.sessions[hashHex(cookie)] = &session{owner: owner, token: token, expires: now.Add(SessionTTL)}
	c.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: cookieName(r), Value: cookie, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
}

// session returns r's session, while it is live.
func (c *Client) session(r *http.Request) (*session, bool) {
	ck, err := r.Cookie(cookieName(r))
	if err != nil {
		return nil, false
	}
	c.mu.Lock()
	s := c.sessions[hashHex(ck.Value)]
	c.mu.Unlock()
	return s, s != nil && time.Now().Before(s.expires) && c.Live(s.owner)
}

// Check reports whether token is the hello token of a live session.
func (c *Client) Check(token string) bool {
	c.mu.Lock()
	var owner string
	ok := false
	for _, s := range c.sessions {
		if subtle.ConstantTimeCompare([]byte(token), []byte(s.token)) == 1 && time.Now().Before(s.expires) {
			owner, ok = s.owner, true
		}
	}
	c.mu.Unlock()
	return ok && c.Live(owner)
}

// cookieName holds the listener's port: browsers share a host's cookies
// across ports, and the local listener and the phone page can both be on
// 127.0.0.1.
func cookieName(r *http.Request) string {
	addr, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	port := "0"
	if addr != nil {
		_, port, _ = net.SplitHostPort(addr.String())
	}
	return "pitwall-" + port
}

// Bootstrap answers a session with the page's WebSocket and hello token.
// The URL is relative, so it is right behind tailscale serve too.
func (c *Client) Bootstrap(w http.ResponseWriter, r *http.Request) {
	s, ok := c.session(r)
	if !ok {
		noSession(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]any{"url": "/ws", "token": s.token, "protocol": Protocol})
}

// Bundle serves the bundle's files to a session.
func (c *Client) Bundle() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := c.session(r); !ok {
			noSession(w)
			return
		}
		serveFile(w, r, c.Dir)
	})
}

func noSession(w http.ResponseWriter) {
	http.Error(w, "this browser has no pitwall session: run pitwall web open, or open the full client from the phone page", http.StatusUnauthorized)
}

// types are the bundle's content types, by extension. why: Windows takes
// mime's from the registry, where .js is text/plain on some machines.
var types = map[string]string{
	".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8", ".mjs": "text/javascript; charset=utf-8",
	".css": "text/css; charset=utf-8", ".json": "application/json", ".map": "application/json",
	".webmanifest": "application/manifest+json", ".svg": "image/svg+xml", ".png": "image/png", ".ico": "image/x-icon",
	".webp": "image/webp", ".woff2": "font/woff2", ".wasm": "application/wasm", ".txt": "text/plain; charset=utf-8",
}

var hostRe = regexp.MustCompile(`^[A-Za-z0-9.-]+(:[0-9]+)?$|^\[[0-9A-Fa-f:.]+\](:[0-9]+)?$`)

// CSP is the bundle's Content-Security-Policy: its own scripts, styles,
// images and fonts only, no inline script or style, no eval, no frames,
// and connections to its own origin, the WebSocket on host included.
func CSP(host string) string {
	connect := "'self'"
	if hostRe.MatchString(host) { // why: older Safari does not count ws: as 'self'
		connect += " ws://" + host + " wss://" + host
	}
	return "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; font-src 'self'; " +
		"connect-src " + connect + "; manifest-src 'self'; worker-src 'self'; object-src 'none'; base-uri 'none'; " +
		"form-action 'none'; frame-ancestors 'none'"
}

// serveFile serves the file at r's path inside dir: a folder's
// index.html, never a listing, a dotfile or anything outside dir, through
// a symlink either. Everything else is 404.
func serveFile(w http.ResponseWriter, r *http.Request, dir string) {
	h := w.Header()
	h.Set("Content-Security-Policy", CSP(r.Host))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-cache")
	name := path.Clean("/" + r.URL.Path)[1:]
	if name == "" || strings.HasSuffix(r.URL.Path, "/") {
		name = path.Join(name, "index.html")
	}
	if dir == "" || strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, ".") || strings.Contains(name, "/.") {
		http.NotFound(w, r)
		return
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		log.Printf("web: bundle: %q", err)
		http.NotFound(w, r)
		return
	}
	defer root.Close()
	f, err := root.Open(name) // why: os.Root refuses .. and symlinks that leave dir
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	ctype := types[filepath.Ext(name)]
	if ctype == "" {
		ctype = mime.TypeByExtension(filepath.Ext(name))
	}
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	h.Set("Content-Type", ctype)
	http.ServeContent(w, r, "", st.ModTime(), f)
}

// open is the code file NewCode writes and the local listener redeems.
type open struct {
	Hash    string    `json:"hash"` // hex SHA-256 of the code
	Expires time.Time `json:"expires"`
}

// OpenPath is the code file, next to web.json at webPath.
func OpenPath(webPath string) string { return filepath.Join(filepath.Dir(webPath), "web-open.json") }

// NewCode writes a code to path that opens one browser session within
// OpenTTL, and returns it. A new code replaces one not yet used.
func NewCode(path string, now time.Time) (string, error) {
	code := newToken()
	data, err := json.Marshal(open{Hash: hashHex(code), Expires: now.Add(OpenTTL)})
	if err != nil {
		return "", err
	}
	return code, writeFile(path, data)
}

var errBadCode = errors.New("bad code")

// redeem uses the code in path, once: used or expired, it goes.
func redeem(path, code string, now time.Time) error {
	var o open
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &o) != nil {
		return errBadCode
	}
	if subtle.ConstantTimeCompare([]byte(hashHex(code)), []byte(o.Hash)) != 1 {
		return errBadCode
	}
	// why: only the request whose remove succeeds gets the session.
	if err := os.Remove(path); err != nil {
		return errBadCode
	}
	if now.After(o.Expires) {
		return errBadCode
	}
	return nil
}
