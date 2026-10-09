package daemon

import (
	"context"
	"crypto/tls"
	"errors"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/input"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// remoteCheck is how often remoteLoop rereads [remote].
const remoteCheck = 2 * time.Second

// remoteLoop serves the phone page while [remote] enables it, and serves
// it anew when the settings change, so turning it on needs no restart.
func (d *Daemon) remoteLoop(ctx context.Context) {
	if d.o.Remote == nil {
		return
	}
	var cur config.RemoteSettings
	stop := func() {}
	defer func() { stop() }()
	t := time.NewTicker(remoteCheck)
	defer t.Stop()
	for {
		if s := d.o.Remote(); s != cur {
			stop()
			stop, cur = func() {}, s
			if s.Enabled {
				stop = d.serveRemote(s)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// serveRemote starts the phone page on s.Listen and returns what stops
// it. A failure is logged, and waits for the settings to change.
func (d *Daemon) serveRemote(s config.RemoteSettings) (stop func()) {
	ln, err := net.Listen("tcp", s.Listen)
	if err != nil {
		log.Printf("remote: %q", err)
		return func() {}
	}
	scheme := "http"
	if s.TLS {
		cert, fp, err := remote.Cert(d.o.RemoteDir)
		if err != nil {
			ln.Close()
			log.Printf("remote: certificate: %q", err)
			return func() {}
		}
		ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
		scheme = "https"
		log.Printf("remote: certificate SHA-256 %s", fp)
	}
	srv := &http.Server{Handler: d.remoteServer().Handler(), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	log.Printf("remote: serving %s://%s for paired devices", scheme, ln.Addr())
	return func() {
		srv.Close()
		log.Printf("remote: stopped serving %s", ln.Addr())
	}
}

// remoteServer is the phone page over this daemon's state and panes.
func (d *Daemon) remoteServer() *remote.Server {
	return &remote.Server{
		Dir: d.o.RemoteDir,
		Items: func() []remote.Item {
			d.mu.Lock()
			defer d.mu.Unlock()
			return remote.Items(d.st)
		},
		Answer: d.remoteAnswer,
		Reply:  d.remoteReply,
	}
}

// waiting returns pane and its activity while that activity is still the
// one of UpdatedAt at, which the phone showed: an answer never lands on a
// prompt the phone has not shown.
func (d *Daemon) waiting(pane string, at int64) (Pane, model.Activity, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	p, i := d.panes[pane], d.activityIndex(pane)
	if p == nil || i < 0 || d.inputs[pane] == nil {
		return nil, model.Activity{}, errors.New("that pane no longer needs you")
	}
	if a := d.st.Activities[i]; a.UpdatedAt.UnixNano() == at {
		return p, a, nil
	}
	return nil, model.Activity{}, errors.New("that pane has moved on; refresh to see what it asks now")
}

// remoteAnswer presses Allow or Deny on pane's permission prompt, for the
// phone page and for a GUI's proto.Answer.
func (d *Daemon) remoteAnswer(pane string, at int64, allow bool) error {
	p, a, err := d.waiting(pane, at)
	if err != nil {
		return err
	}
	k, known := remote.AnswerKey(a.Provider, allow)
	if a.State != model.StatePendingApproval || !known {
		return errors.New("that pane is not asking permission")
	}
	// why: a hook can leave the state behind the screen; the key goes only to a prompt on it.
	if agent.ReadScreen(p.Snapshot()) != agent.ScreenForm {
		return errors.New("the pane shows no prompt to answer")
	}
	return d.handle(context.Background(), proto.Input{Pane: pane, Data: press(k, p.Modes())})
}

// replyDelay separates a reply's paste from its Enter.
// why: a TUI may read an Enter that arrives in the same read as a paste as part of it.
var replyDelay = 100 * time.Millisecond

// remoteReply pastes text into pane, cleaned of control characters, and
// presses Enter.
func (d *Daemon) remoteReply(pane string, at int64, text string) error {
	p, a, err := d.waiting(pane, at)
	if err != nil {
		return err
	}
	if !remote.Replies(a.State) {
		return errors.New("that pane is not waiting for a reply")
	}
	m := p.Modes()
	text = remote.Clean(text)
	if !m.BracketedPaste {
		text = strings.ReplaceAll(text, "\n", " ") // a bare newline would send the first line alone
	}
	if text == "" {
		return errors.New("the reply is empty")
	}
	ctx := context.Background()
	if err := d.handle(ctx, proto.Input{Pane: pane, Data: input.Paste(text, m)}); err != nil {
		return err
	}
	time.Sleep(replyDelay)
	return d.handle(ctx, proto.Input{Pane: pane, Data: press(key.NameReturn, m)})
}

// press is what pressing k alone sends to a pane in modes m, as the GUI
// would send it.
func press(k key.Name, m vt.Modes) []byte {
	if b := input.Key(key.Event{Name: k, State: key.Press}, m); b != nil {
		return b
	}
	return input.Text(strings.ToLower(string(k)))
}
