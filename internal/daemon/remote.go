package daemon

import (
	"context"
	"crypto/tls"
	"errors"
	"log"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/input"
	"github.com/quanticstudios/pitwall/internal/logs"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/vt"
	"github.com/quanticstudios/pitwall/internal/web"
)

// remoteCheck is how often a listener's settings are read again.
const remoteCheck = 2 * time.Second

// remoteFails keeps a phone page that cannot start to a log line a minute.
var remoteFails = logs.Limiter{Every: time.Minute}

// phone is what the phone page serves: [remote], and [web] bundle, the
// web client it serves to paired devices.
type phone struct {
	config.RemoteSettings
	Bundle string
}

// remoteLoop serves the phone page while [remote] enables it.
func (d *Daemon) remoteLoop(ctx context.Context) {
	if d.o.Remote == nil {
		return
	}
	get := func() phone {
		p := phone{RemoteSettings: d.o.Remote()}
		if d.o.Web != nil {
			p.Bundle = d.o.Web().Bundle
		}
		return p
	}
	watchListener(ctx, get, func(p phone) bool { return p.Enabled }, d.serveRemote)
}

// watchListener runs serve while on(get()) holds, and serves anew when the
// settings change, so turning a listener on needs no restart. serve
// returns what stops it, or nil after a failure.
func watchListener[S comparable](ctx context.Context, get func() S, on func(S) bool, serve func(S) (stop func())) {
	var cur S
	var stop func() // nil while not serving
	defer func() {
		if stop != nil {
			stop()
		}
	}()
	t := time.NewTicker(remoteCheck)
	defer t.Stop()
	for {
		s := get()
		if s != cur && stop != nil {
			stop()
			stop = nil
		}
		cur = s
		if on(s) && stop == nil {
			// why: retried at each check, since a daemon handing off holds
			// the port until it exits.
			stop = serve(s)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// serveRemote starts the phone page on s.Listen, with the web client in
// s.Bundle when set, and the pushes to the phones that turned them on,
// and returns what stops them, or nil after a failure, which is logged
// once a minute.
func (d *Daemon) serveRemote(s phone) (stop func()) {
	ln, err := net.Listen("tcp", s.Listen)
	if err != nil {
		if ok, _ := remoteFails.Allow("listen", time.Now()); ok {
			log.Printf("remote: %q", err)
		}
		return nil
	}
	scheme := "http"
	if s.TLS {
		cert, fp, err := remote.Cert(d.o.RemoteDir)
		if err != nil {
			ln.Close()
			if ok, _ := remoteFails.Allow("cert", time.Now()); ok {
				log.Printf("remote: certificate: %q", err)
			}
			return nil
		}
		ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
		scheme = "https"
		log.Printf("remote: certificate SHA-256 %s", fp)
	}
	rs := d.remoteServer()
	var ws *web.Server
	if s.Bundle != "" {
		rs.Web = &web.Client{Dir: s.Bundle, Live: func(id string) bool { return remote.Paired(d.o.RemoteDir, id) }}
		ws = &web.Server{Version: Build, Client: rs.Web}
		rs.WS = ws.WebSocket()
	}
	srv := &http.Server{Handler: rs.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	ctx, cancel := context.WithCancel(context.Background())
	go d.pushLoop(ctx, &remote.Pusher{Dir: d.o.RemoteDir})
	log.Printf("remote: serving %s://%s for paired devices", scheme, ln.Addr())
	return func() {
		cancel()
		if ws != nil {
			ws.Close()
		}
		srv.Close()
		log.Printf("remote: stopped serving %s", ln.Addr())
	}
}

// remoteServer is the phone page over this daemon's state and panes.
func (d *Daemon) remoteServer() *remote.Server {
	return &remote.Server{
		Dir:         d.o.RemoteDir,
		Items:       d.remoteItems,
		Answer:      d.remoteAnswer,
		Reply:       d.remoteReply,
		Approve:     d.remoteApprove,
		AnswerInput: d.remoteInput,
	}
}

// remoteItems is what the phone page lists, with each chat's pending
// requests read from its loop once d.mu is free.
func (d *Daemon) remoteItems() []remote.Item {
	d.mu.Lock()
	items := remote.Items(d.st)
	chats := make([]*chat, len(items))
	for i, it := range items {
		chats[i] = d.chats[it.Pane]
	}
	d.mu.Unlock()
	for i, c := range chats {
		if c != nil {
			c.run(func() { items[i].Requests = remote.Requests(c.snap, logs.Redact) })
		}
	}
	return items
}

// remoteApprove and remoteInput send the phone's answer to a chat as the
// GUI's go, so the first answer wins and every client sees it resolved.
func (d *Daemon) remoteApprove(chat, request, decision, reason string) error {
	return chatRefusal(d.chatRequest(context.Background(), proto.ChatApprove{Chat: chat, Request: request, Decision: decision, Reason: reason}))
}

func (d *Daemon) remoteInput(chat, request string, answers map[string][]string) error {
	return chatRefusal(d.chatRequest(context.Background(), proto.ChatAnswer{Chat: chat, Request: request, Answers: answers}))
}

// chatRefusal is the error in chatRequest's reply, nil when it worked.
func chatRefusal(reply any, _ bool) error {
	if r, ok := reply.(proto.ChatResult); ok && r.Code != "" {
		return errors.New(r.Message)
	}
	return nil
}

// waiting returns pane, its activity and the command its hook asked to run,
// "" for none known, while that activity is still the one of UpdatedAt at,
// which the phone showed: an answer never lands on a prompt the phone has not
// shown.
func (d *Daemon) waiting(pane string, at int64) (Pane, model.Activity, string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	p, i := d.panes[pane], d.activityIndex(pane)
	if p == nil || i < 0 || d.inputs[pane] == nil {
		return nil, model.Activity{}, "", errors.New("that pane no longer needs you")
	}
	if a := d.st.Activities[i]; a.UpdatedAt.UnixNano() == at {
		return p, a, d.askedOf(a), nil
	}
	return nil, model.Activity{}, "", errors.New("that pane has moved on; refresh to see what it asks now")
}

// askedOf is the command a's hook asked to run, "" for none known. Callers hold
// d.mu.
func (d *Daemon) askedOf(a model.Activity) string {
	if c := d.live.asked[a.PaneID]; c.at.Equal(a.UpdatedAt) {
		return c.command
	}
	return ""
}

// bindAsked pairs pane's new activity next with command, from a permission
// request when asks is set. A later report of the same prompt, such as its
// Notification, keeps the command prev had. Callers hold d.mu.
func (d *Daemon) bindAsked(pane string, prev *model.Activity, next model.Activity, command string, asks bool) {
	if !asks && prev != nil && prev.State == model.StatePendingApproval && next.State == model.StatePendingApproval {
		command = d.askedOf(*prev)
	}
	delete(d.live.asked, pane)
	if command == "" {
		return
	}
	if d.live.asked == nil {
		d.live.asked = map[string]askedCommand{}
	}
	d.live.asked[pane] = askedCommand{command, next.UpdatedAt}
}

// sameCommand refuses a permission prompt on g's screen that does not show
// asked, the command a's hook reported: an agent can draw its next prompt
// before that prompt's hook arrives. A prompt with no known command passes.
func sameCommand(a model.Activity, asked string, g vt.Grid) error {
	if a.State == model.StatePendingApproval && asked != "" && !agent.CommandShown(a.Provider, g, asked) {
		return errors.New("pitwall could not confirm the prompt on screen is the one the row showed; answer it in its tab")
	}
	return nil
}

// remoteAnswer presses Allow or Deny on pane's permission prompt, for the
// phone page and for a GUI's proto.Answer.
func (d *Daemon) remoteAnswer(pane string, at int64, allow bool) error {
	p, a, asked, err := d.waiting(pane, at)
	if err != nil {
		return err
	}
	k, known := remote.AnswerKey(a.Provider, allow)
	if a.State != model.StatePendingApproval || !known {
		return errors.New("that pane is not asking permission")
	}
	// why: a hook can leave the state behind the screen; the key goes only to a permission prompt on it, as 1 picks the first option of a plan or question too.
	g := p.Snapshot()
	if agent.State(g) != model.StatePendingApproval {
		return errors.New("the pane shows no prompt to answer")
	}
	if err := sameCommand(a, asked, g); err != nil {
		return err
	}
	d.noteUserKey(pane, nil)
	return d.handle(context.Background(), proto.Input{Pane: pane, Data: press(k, p.Modes())})
}

// answerNotice shows a window why its answer pressed nothing, as a GUI shows no
// request's error, and takes that back once one lands. The phone's page gets the
// error itself.
func (d *Daemon) answerNotice(err error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err != nil {
		d.st.Notice = answerRefused + err.Error()
		d.changed()
	} else if strings.HasPrefix(d.st.Notice, answerRefused) {
		d.st.Notice = ""
		d.changed()
	}
	return err
}

const answerRefused = "Not answered: "

// pressOffer presses the key of label, an answer a GUI's row showed, on
// pane's form. It reads the screen first, so a stale button cannot answer
// another form or type into a prompt.
func (d *Daemon) pressOffer(pane string, at int64, label string) error {
	p, a, asked, err := d.waiting(pane, at)
	if err != nil {
		return err
	}
	g := p.Snapshot()
	if err := sameCommand(a, asked, g); err != nil {
		return err
	}
	for _, o := range agent.Offers(a, g) {
		if o.Label == label {
			d.noteUserKey(pane, nil)
			return d.handle(context.Background(), proto.Input{Pane: pane, Data: press(key.Name(o.Key), p.Modes())})
		}
	}
	return errors.New("the pane no longer offers " + label + "; answer it in its tab")
}

// checkForms keeps each waiting agent's Buttons and Menu to what its screen
// shows now. A hook fires before its form is drawn, so a row's buttons come
// at most a poll after it.
func (d *Daemon) checkForms() {
	type check struct {
		p Pane
		a model.Activity
	}
	var checks []check
	d.mu.Lock()
	for _, a := range d.st.Activities {
		waits := a.State == model.StatePendingApproval || a.State == model.StatePlanReady || a.State == model.StateAwaitingInput
		if p := d.panes[a.PaneID]; p != nil && d.inputs[a.PaneID] != nil && (waits || len(a.Buttons) > 0 || a.Menu) {
			checks = append(checks, check{p, a})
		}
	}
	d.mu.Unlock()
	for _, c := range checks {
		g := c.p.Snapshot()
		var buttons []string
		for _, o := range agent.Offers(c.a, g) {
			buttons = append(buttons, o.Label)
		}
		menu := c.a.State == model.StateAwaitingInput && (formAnnounced(c.a) || agent.Choosing(g))
		d.mu.Lock()
		// why: the screen was read outside d.mu; a newer activity waits for its own read.
		if i := d.activityIndex(c.a.PaneID); i >= 0 && d.st.Activities[i].UpdatedAt.Equal(c.a.UpdatedAt) {
			if a := &d.st.Activities[i]; !slices.Equal(a.Buttons, buttons) || a.Menu != menu {
				a.Buttons, a.Menu = buttons, menu
				d.changed()
			}
		}
		d.mu.Unlock()
	}
}

// replyDelay separates a reply's paste from its Enter.
// why: a TUI may read an Enter that arrives in the same read as a paste as part of it.
var replyDelay = newTiming(100 * time.Millisecond)

// remoteReply pastes text into pane, cleaned of control characters, and
// presses Enter.
func (d *Daemon) remoteReply(pane string, at int64, text string) error {
	p, a, _, err := d.waiting(pane, at)
	if err != nil {
		return err
	}
	if !remote.Replies(a.State) {
		return errors.New("that pane is not waiting for a reply")
	}
	g := p.Snapshot()
	d.mu.Lock()
	drafted := d.drafted(pane, a.Provider, g)
	d.mu.Unlock()
	if drafted {
		return errors.New("you have a draft in that agent's input box: send or clear it first")
	}
	if formHolds(a, g) {
		return errors.New("that pane shows a form; answer it in its tab")
	}
	d.noteUserKey(pane, nil)
	if err := d.typeLine(pane, p, text, "the reply is empty"); err != nil {
		return err
	}
	d.recordPrompt(pane)
	return nil
}

// typeLine pastes text into pane p, cleaned of control characters, and
// presses Enter; empty is the error for text that cleans to nothing.
func (d *Daemon) typeLine(pane string, p Pane, text, empty string) error {
	m := p.Modes()
	text = remote.Clean(text)
	if !m.BracketedPaste {
		text = strings.ReplaceAll(text, "\n", " ") // a bare newline would send the first line alone
	}
	if text == "" {
		return errors.New(empty)
	}
	ctx := context.Background()
	if err := d.handle(ctx, proto.Input{Pane: pane, Data: input.Paste(text, m)}); err != nil {
		return err
	}
	return d.pressEnter(pane, p, text)
}

// pressEnter presses Enter on what was just pasted into pane p, after
// replyDelay, unless a form came up since.
func (d *Daemon) pressEnter(pane string, p Pane, pasted string) error {
	time.Sleep(replyDelay.get())
	// why: Enter would answer the form, whatever was pasted.
	if agent.FormUp(p.Snapshot(), pasted) {
		return errors.New("a form came up; what was typed stays in the agent's input")
	}
	return d.handle(context.Background(), proto.Input{Pane: pane, Data: press(key.NameReturn, p.Modes())})
}

// formDraw is how long after its hook a form may take to reach the screen.
const formDraw = time.Second

// formHolds reports whether typed text must wait for a's agent, whose screen
// is g: a form shows, whatever a's state says, or a hook just announced one.
func formHolds(a model.Activity, g vt.Grid) bool {
	return formAnnounced(a) || agent.FormUp(g, "")
}

func formAnnounced(a model.Activity) bool {
	return a.State == model.StateAwaitingInput && a.Menu && time.Since(a.UpdatedAt) < formDraw
}

// typeLetter pastes each image's path into pane p, then text, and presses
// Enter. Images with no text go in with no empty paste.
func (d *Daemon) typeLetter(pane string, p Pane, images []string, text string) error {
	m := p.Modes()
	ctx := context.Background()
	for _, img := range images {
		if err := d.handle(ctx, proto.Input{Pane: pane, Data: input.PastePaths([]string{img}, m)}); err != nil {
			return err
		}
	}
	if len(images) > 0 && remote.Clean(text) == "" {
		return d.pressEnter(pane, p, "")
	}
	return d.typeLine(pane, p, text, "the message is empty")
}

// press is what pressing k alone sends to a pane in modes m, as the GUI
// would send it.
func press(k key.Name, m vt.Modes) []byte {
	if b := input.Key(key.Event{Name: k, State: key.Press}, m); b != nil {
		return b
	}
	return input.Text(strings.ToLower(string(k)))
}
