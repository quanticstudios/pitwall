package daemon

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"log"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
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

// phoneListen is the comparable subset of [remote] that drives the local
// TCP listener; changes under [remote.tailscale] do not disturb it.
type phoneListen struct {
	Enabled bool
	Listen  string
	TLS     bool
	URL     string
	Bundle  string
}

// remoteLoop serves the phone page while [remote] enables it.
func (d *Daemon) remoteLoop(ctx context.Context) {
	if d.o.Remote == nil {
		return
	}
	get := func() phoneListen {
		r := d.o.Remote()
		p := phoneListen{Enabled: r.Enabled, Listen: r.Listen, TLS: r.TLS, URL: r.URL}
		if d.o.Web != nil {
			p.Bundle = d.o.Web().Bundle
		}
		return p
	}
	watchListener(ctx, get, func(p phoneListen) bool { return p.Enabled }, func(p phoneListen) func() {
		return d.serveRemote(phone{
			RemoteSettings: config.RemoteSettings{Enabled: p.Enabled, Listen: p.Listen, TLS: p.TLS, URL: p.URL},
			Bundle:         p.Bundle,
		})
	})
}

// acquirePush starts the shared pushLoop if it is not running yet, and returns
// a release function that stops it when the last remote listener closes.
func (d *Daemon) acquirePush() func() {
	d.pushMu.Lock()
	d.pushRefs++
	if d.pushRefs == 1 {
		ctx, cancel := context.WithCancel(context.Background())
		d.pushCancel = cancel
		go d.pushLoop(ctx, &remote.Pusher{Dir: d.o.RemoteDir})
	}
	d.pushMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			d.pushMu.Lock()
			d.pushRefs--
			if d.pushRefs == 0 && d.pushCancel != nil {
				d.pushCancel()
				d.pushCancel = nil
			}
			d.pushMu.Unlock()
		})
	}
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
	releasePush := d.acquirePush()
	log.Printf("remote: serving %s://%s for paired devices", scheme, ln.Addr())
	return func() {
		releasePush()
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
		Version:     Build,
		Items:       d.remoteItems,
		Answer:      d.remoteAnswer,
		Reply:       d.remoteReply,
		Approve:     d.remoteApprove,
		AnswerInput: d.remoteInput,
		Tabs:        d.phoneTabs,
		Changed:     d.phoneChanged,
		ChatEntries: d.phoneChatEntries,
		Send:        d.phoneSend,
		Command:     d.phoneCommand,
		Queue:       d.phoneQueue,
		OpenDesktop: d.phoneOpenDesktop,
		Question:    d.phoneQuestion,
		Upload:      d.phoneUpload,
		SendImage:   d.phoneSendImage,
		Icons:       &d.phoneIcons,
		Folder:      d.phoneFolder,
	}
}

// phoneFolder is a listed tab's working directory, its first agent's or
// the one it started in, and its repo, or its folder outside one.
func (d *Daemon) phoneFolder(tab string) (cwd, root string, ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	i := slices.IndexFunc(d.st.Workspaces, func(w model.Workspace) bool { return w.ID == tab })
	if i < 0 {
		return "", "", false
	}
	w := d.st.Workspaces[i]
	root = cmp.Or(w.RepoRoot, w.Path)
	if w.Detached || w.Monitor || root == "" {
		return "", "", false
	}
	cwd = w.Path
	if aps := d.st.AgentPanes(w); len(aps) > 0 && aps[0].Pane.Cwd != "" {
		cwd = aps[0].Pane.Cwd
	}
	return cwd, root, true
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
	// A late hook can leave an approval card visible after its prompt closes.
	g := p.Snapshot()
	if agent.StateOf(a.Provider, g) != model.StatePendingApproval {
		return errors.New("the pane shows no permission prompt to answer")
	}
	if allow {
		if err := sameCommand(a, asked, g); err != nil {
			return err
		}
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
		menu := c.a.State == model.StateAwaitingInput && (formAnnounced(c.a) || agent.Choosing(c.a.Provider, g))
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

// imageWait is how long a Claude Code pane gets to show a pasted image's
// token before the next image's path goes in.
var imageWait = newTiming(3 * time.Second)

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
	if formHolds(a.Provider, a, g) {
		return errors.New("that pane shows a form; answer it in its tab")
	}
	d.noteUserKey(pane, nil)
	if err := d.typeLine(pane, p, text, "the reply is empty"); err != nil {
		return err
	}
	d.recordPrompt(pane)
	return nil
}

// typeLine pastes text into pane p, cleaned of control characters, then
// each of quoted as a paste of its own under a blank line, and presses
// Enter; empty is the error for text that cleans to nothing.
func (d *Daemon) typeLine(pane string, p Pane, text, empty string, quoted ...string) error {
	m := p.Modes()
	clean := func(s string) string {
		s = remote.Clean(s)
		if !m.BracketedPaste {
			s = strings.ReplaceAll(s, "\n", " ") // a bare newline would send the first line alone
		}
		return s
	}
	text = clean(text)
	var quotes []string
	for _, q := range quoted {
		if q = clean(q); q != "" {
			quotes = append(quotes, q)
		}
	}
	if text == "" && len(quotes) == 0 {
		return errors.New(empty)
	}
	ctx := context.Background()
	d.mu.Lock()
	prov := d.paneProvider(pane)
	d.mu.Unlock()
	if agent.FormUp(prov, p.Snapshot(), "") {
		return errFormUp
	}
	claude := prov == model.ProviderClaude
	if prov == model.ProviderAntigravity && agySlashLine(text) && len(quotes) == 0 {
		// why: agy's slash menu draws as a command is typed, and an Enter after it drew picks its highlighted row; one in the same write takes the line as typed (agyCommand).
		return d.typeAndEnter(pane, p, append(input.Text(text), press(key.NameReturn, m)...))
	}
	var data []byte
	if text != "" {
		data = input.Paste(text, m)
		if claude {
			data = typeLines(text, m)
		}
	}
	for _, q := range quotes {
		if len(data) > 0 {
			nl := typeLines("\n\n", m)
			if !m.BracketedPaste {
				nl = []byte("  ")
			}
			data = append(data, nl...)
		}
		data = append(data, input.Paste(q, m)...) // why: a real paste, so Claude Code quotes a long one as pasted material
	}
	if err := d.handle(ctx, proto.Input{Pane: pane, Data: data}); err != nil {
		return err
	}
	if strings.HasPrefix(text, "/") {
		// why: Enter in Claude Code's slash menu runs its highlighted match, so "/ux" ran the first command starting with it; Esc closes the menu and keeps the text.
		time.Sleep(replyDelay.get())
		if agent.SlashMenu(p.Snapshot()) {
			if err := d.handle(ctx, proto.Input{Pane: pane, Data: press(key.NameEscape, m)}); err != nil {
				return err
			}
		}
	}
	pasted := text
	if pasted == "" && len(quotes) > 0 {
		pasted = strings.Join(quotes, "\n\n")
	}
	return d.pressEnter(pane, p, pasted)
}

// pasteChunk is the longest paste typeLines sends, in runes. Claude Code
// 2.1 records a paste over 800 characters, or one of four lines or more,
// as <pasted_content>, which its model reads as material the user pasted
// rather than as the user's own words; a rune may count as two of its
// characters.
const pasteChunk = 400

// typeLines is text as Claude Code records it typed: each line, and each
// pasteChunk runes of a longer one, a paste of its own, joined by Ctrl+J,
// its newline. Pastes, not keys, so that a leading ?, an @ or a # opens no
// popup or menu. A leading ! goes in after a space: pasted first, it too
// turns on bash mode.
func typeLines(text string, m vt.Modes) []byte {
	if !m.BracketedPaste {
		return input.Paste(text, m)
	}
	if strings.HasPrefix(text, "!") {
		text = " " + text
	}
	nl := input.Key(key.Event{Name: "J", Modifiers: key.ModCtrl, State: key.Press}, m)
	var b []byte
	for i, line := range strings.Split(text, "\n") {
		if i > 0 {
			b = append(b, nl...)
		}
		for r := []rune(line); len(r) > 0; r = r[min(len(r), pasteChunk):] {
			b = append(b, input.Paste(string(r[:min(len(r), pasteChunk)]), m)...)
		}
	}
	return b
}

// errFormUp is returned when a menu or form on the agent's screen stops
// pitwall from typing or pressing Enter.
var errFormUp = errors.New("a menu is open in the agent's terminal")

// clearBox is the key sequence that clears pasted text from an agent's input
// box when a form appears between pasting and Enter: Ctrl+U for each line,
// Backspace joining earlier lines when bracketed paste kept newlines.
func clearBox(pasted string, m vt.Modes) []byte {
	if pasted == "" {
		return nil
	}
	kill := input.Key(key.Event{Name: "U", Modifiers: key.ModCtrl, State: key.Press}, m)
	bs := press(key.NameDeleteBackward, m)
	lines := 1
	if m.BracketedPaste {
		lines += strings.Count(pasted, "\n")
	}
	var b []byte
	for i := range lines {
		if i > 0 {
			b = append(b, bs...)
		}
		b = append(b, kill...)
	}
	return b
}

// pressEnter presses Enter on what was just pasted into pane p, after
// replyDelay, unless a form came up since. When a form came up, any pasted
// text is cleared from the agent's box and errFormUp is returned so the
// caller can hold the message in the queue.
func (d *Daemon) pressEnter(pane string, p Pane, pasted string) error {
	time.Sleep(replyDelay.get())
	d.mu.Lock()
	prov := d.paneProvider(pane)
	d.mu.Unlock()
	// why: Enter would answer the form, whatever was pasted.
	if agent.FormUp(prov, p.Snapshot(), pasted) {
		if b := clearBox(pasted, p.Modes()); len(b) > 0 {
			_ = d.handle(context.Background(), proto.Input{Pane: pane, Data: b})
		}
		return errFormUp
	}
	// User sends record their submission separately; their later hooks must not move the target again.
	d.mu.Lock()
	previous := d.mail.daemonPrompt[pane]
	d.mail.daemonPrompt[pane] = true
	d.mu.Unlock()
	err := d.handle(context.Background(), proto.Input{Pane: pane, Data: d.steerKey(pane, p.Modes())})
	if err != nil {
		d.mu.Lock()
		d.mail.daemonPrompt[pane] = previous
		d.mu.Unlock()
		return err
	}
	d.mu.Lock()
	at, resumed := d.resumed[pane]
	d.mu.Unlock()
	if !resumed || time.Since(at) > resumeLoad {
		return nil
	}
	// why: Claude 2.1.330 drops a Return that comes while a --resume still loads (slow after a reboot), and the text stays in its box to join the user's next message.
	for range enterTries {
		if !d.stillInBox(p, prov) {
			return nil
		}
		if err := d.handle(context.Background(), proto.Input{Pane: pane, Data: d.steerKey(pane, p.Modes())}); err != nil {
			return err
		}
	}
	return nil
}

// enterTries is how many more times pressEnter presses Enter on text the
// agent's box still holds, enterLook apart, within resumeLoad of the pane's
// resume.
const enterTries = 3

// resumeLoad is how long after pitwall resumes a pane its agent may still
// be loading the session and drop a Return.
const resumeLoad = 3 * time.Minute

var enterLook = newTiming(1500 * time.Millisecond)

// stillInBox reports whether agent prov's input box on p's screen still
// holds text enterLook after an Enter; false at once for an agent whose
// box pitwall cannot read.
func (d *Daemon) stillInBox(p Pane, prov model.Provider) bool {
	if !agent.Draft(prov, p.Snapshot()) {
		return false
	}
	for end := time.Now().Add(enterLook.get()); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if !agent.Draft(prov, p.Snapshot()) {
			return false
		}
	}
	return true
}

// agySlashLine reports whether text is one line that starts a slash
// command, which agy's menu would take over.
func agySlashLine(text string) bool {
	return strings.HasPrefix(text, "/") && !strings.ContainsAny(text, "\r\n")
}

// typeAndEnter writes data, a line and its Enter, into pane p as one
// write, recorded as the user's submission as pressEnter records one.
func (d *Daemon) typeAndEnter(pane string, p Pane, data []byte) error {
	d.mu.Lock()
	prov := d.paneProvider(pane)
	d.mu.Unlock()
	if agent.FormUp(prov, p.Snapshot(), "") {
		return errFormUp
	}
	d.mu.Lock()
	previous := d.mail.daemonPrompt[pane]
	d.mail.daemonPrompt[pane] = true
	d.mu.Unlock()
	err := d.handle(context.Background(), proto.Input{Pane: pane, Data: data})
	if err != nil {
		d.mu.Lock()
		d.mail.daemonPrompt[pane] = previous
		d.mu.Unlock()
	}
	return err
}

// formDraw is how long after its hook a form may take to reach the screen.
const formDraw = time.Second

// formHolds reports whether typed text must wait for prov's agent, whose
// screen is g: a form shows for prov, whatever a's state says, or a hook
// just announced one.
func formHolds(prov model.Provider, a model.Activity, g vt.Grid) bool {
	if prov == "" {
		prov = a.Provider
	}
	return formAnnounced(a) || agent.FormUp(prov, g, "")
}

func formAnnounced(a model.Activity) bool {
	return a.State == model.StateAwaitingInput && a.Menu && time.Since(a.UpdatedAt) < formDraw
}

// typeLetter pastes each image's path into pane p, then text, and presses
// Enter. Images with no text go in with no empty paste.
func (d *Daemon) typeLetter(pane string, p Pane, images []string, text string, quoted []string) error {
	m := p.Modes()
	ctx := context.Background()
	d.mu.Lock()
	prov := d.paneProvider(pane)
	d.mu.Unlock()
	if agent.FormUp(prov, p.Snapshot(), "") {
		return errFormUp
	}
	claude := prov == model.ProviderClaude
	for i, img := range images {
		shown := strings.Count(screenText(p.Snapshot()), "[Image #")
		if err := d.handle(ctx, proto.Input{Pane: pane, Data: input.PastePaths([]string{img}, m)}); err != nil {
			return err
		}
		// why: Claude Code numbers a pasted image once it has read the file, so a smaller one pasted next could take this one's number.
		for end := time.Now().Add(imageWait.get()); claude && i < len(images)-1 && time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			if strings.Count(screenText(p.Snapshot()), "[Image #") > shown {
				break
			}
		}
	}
	if len(images) > 0 && remote.Clean(text) == "" && len(quoted) == 0 {
		return d.pressEnter(pane, p, "")
	}
	return d.typeLine(pane, p, text, "the message is empty", quoted...)
}

// press is what pressing k alone sends to a pane in modes m, as the GUI
// would send it.
func press(k key.Name, m vt.Modes) []byte {
	if b := input.Key(key.Event{Name: k, State: key.Press}, m); b != nil {
		return b
	}
	if k == key.NameSpace {
		return input.Text(" ")
	}
	return input.Text(strings.ToLower(string(k)))
}

func confirmApproval(a model.Activity, asked string, g vt.Grid) error {
	if agent.StateOf(a.Provider, g) != model.StatePendingApproval {
		return errors.New("the pane shows no permission prompt to confirm")
	}
	return sameCommand(a, asked, g)
}

func (d *Daemon) phoneOpenDesktop(pane string) error {
	d.mu.Lock()
	var workspace string
	for _, p := range d.st.Panes {
		if p.ID == pane {
			workspace = p.WorkspaceID
			break
		}
	}
	shown := false
	if w := d.workspace(workspace); w != nil {
		for c := range d.clients {
			if c.moves && c.session == w.SessionID {
				shown = true
				break
			}
		}
	}
	d.mu.Unlock()
	if workspace == "" {
		return errors.New("that tab is no longer open")
	}
	if !shown {
		return errors.New("open pitwall on the desktop to answer this prompt")
	}
	return d.focusSession(proto.FocusSession{WorkspaceID: workspace, Pane: pane})
}

// steerKey is the key that sends what was typed into pane: Return, or
// mid-turn in Antigravity CLI Alt+M, which sends it into the running turn
// where Return would queue it for a turn of its own.
// ponytail: agy's /settings can swap the two (Queued Messages: Steer); pitwall reads the default.
func (d *Daemon) steerKey(pane string, m vt.Modes) []byte {
	d.mu.Lock()
	agy := d.paneProvider(pane) == model.ProviderAntigravity && d.paneBusy(pane)
	d.mu.Unlock()
	if agy {
		return input.Key(key.Event{Name: "M", Modifiers: key.ModAlt, State: key.Press}, m)
	}
	return press(key.NameReturn, m)
}
