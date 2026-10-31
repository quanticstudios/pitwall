package daemon

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/logs"
	"github.com/quanticstudios/pitwall/internal/web"
)

// webLoop serves the web protocol while [web] enables it.
func (d *Daemon) webLoop(ctx context.Context) {
	if d.o.Web == nil {
		return
	}
	watchListener(ctx, d.o.Web, func(s config.WebSettings) bool { return s.Enabled }, d.serveWeb)
}

// webFails keeps a web listener that cannot start to a log line a minute.
var webFails = logs.Limiter{Every: time.Minute}

// serveWeb listens on 127.0.0.1, writes web.json for clients to find it,
// and returns what stops it, or nil after a failure, which is logged once
// a minute.
func (d *Daemon) serveWeb(s config.WebSettings) (stop func()) {
	ln, err := web.Listen(s.Port)
	if err != nil {
		if ok, _ := webFails.Allow("listen", time.Now()); ok {
			log.Printf("web: %q", err)
		}
		return nil
	}
	if _, err := web.Publish(d.o.WebPath, ln.Addr(), os.Getpid()); err != nil {
		ln.Close()
		if ok, _ := webFails.Allow("publish", time.Now()); ok {
			log.Printf("web: %q", err)
		}
		return nil
	}
	token := func() (string, error) {
		f, err := web.Read(d.o.WebPath)
		return f.Token, err
	}
	ws := &web.Server{Version: Build, Token: token}
	if s.Bundle != "" {
		// A session lives while web.json keeps the token it started under.
		ws.Client = &web.Client{Dir: s.Bundle, Live: func(owner string) bool {
			t, err := token()
			return err == nil && t == owner
		}}
		ws.OpenPath = web.OpenPath(d.o.WebPath)
	}
	srv := &http.Server{Handler: ws.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	log.Printf("web: serving ws://%s/ws, token in %s", ln.Addr(), d.o.WebPath)
	if ws.Client != nil {
		log.Printf("web: serving the web client in %s at http://%s/", s.Bundle, ln.Addr())
	}
	return func() {
		ws.Close()
		srv.Close()
		log.Printf("web: stopped serving %s", ln.Addr())
	}
}
