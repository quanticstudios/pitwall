// Package codex drives Codex through `codex app-server`: JSON-RPC over
// stdio, one object per line, one server per chat. docs/managed-sessions.md
// (#codex) is the design; method names are from the schema codex-cli
// 0.166.0 writes with `codex app-server generate-json-schema`.
package codex

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/session"
)

// now is the clock behind event times; tests fix it.
var now = time.Now

// Restarts within a minute that a crashed server gets before Send refuses.
const maxRestarts = 3

// Provider starts `codex app-server` from PATH, or PITWALL_CODEX_COMMAND.
type Provider struct{}

func (Provider) Start(ctx context.Context, cfg session.Config, from session.Origin, emit func(session.Event)) (session.Driver, error) {
	if from.At != "" && !from.Fork || from.Fork && from.Resume == "" {
		return nil, session.ErrUnsupported // thread/fork cuts a copy; nothing cuts a thread in place
	}
	d := &driver{cfg: cfg, events: make(chan session.Event, 64), emitted: make(chan struct{}), requests: map[string]request{}}
	go func() {
		defer close(d.emitted)
		for e := range d.events {
			emit(e)
		}
	}()
	d.cmdMu.Lock()
	err := d.start(ctx, from)
	d.cmdMu.Unlock()
	if err != nil {
		d.Close(context.Background())
		return nil, err
	}
	return d, nil
}

type driver struct {
	cfg     session.Config
	events  chan session.Event
	emitted chan struct{} // closed when the last event reached emit

	cmdMu    sync.Mutex  // one start, send or close at a time
	restarts []time.Time // under cmdMu

	mu      sync.Mutex
	p       *proc // nil while no server runs
	thread  string
	model   string
	turn    string // the running turn's id
	closing bool

	// What the stream has shown so far; only the reading goroutine of the
	// running server touches these.
	tools    map[string]*session.ToolCall
	phases   map[string]string
	streamed map[string]bool
	unknown  map[string]bool

	requests map[string]request // under mu: open server requests by the request id pitwall gave them
}

// proc is one app-server process.
type proc struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stderr   *tail
	wmu      sync.Mutex
	next     int64            // under driver.mu
	calls    map[string]*call // under driver.mu; nil once the process ended
	stopping bool             // under driver.mu: pitwall ended it
	done     chan struct{}
}

type call struct {
	method string
	reply  chan reply
}

type reply struct {
	result json.RawMessage
	err    error
}

// message is a JSON-RPC message either way. aide sends no "jsonrpc" field
// and the server does not need one.
type message struct {
	ID     any    `json:"id,omitempty"`
	Method string `json:"method,omitempty"`
	Params any    `json:"params,omitempty"`
	Result any    `json:"result,omitempty"`
	Error  any    `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (d *driver) post(e session.Event) {
	if e.CreatedAt == "" {
		e.CreatedAt = session.Stamp(now())
	}
	d.events <- e
}

// start runs a server and opens the thread: a new one, a resumed one, or a
// fork. The caller holds cmdMu.
func (d *driver) start(ctx context.Context, from session.Origin) error {
	cmd := exec.Command(cmp.Or(os.Getenv("PITWALL_CODEX_COMMAND"), "codex"), "app-server")
	cmd.Dir = d.cfg.Cwd
	cmd.Env = append(os.Environ(), d.cfg.Env...)
	p := &proc{cmd: cmd, stderr: &tail{}, calls: map[string]*call{}, done: make(chan struct{})}
	cmd.Stderr = p.stderr
	var err error
	if p.stdin, err = cmd.StdinPipe(); err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	d.mu.Lock()
	if d.closing {
		d.mu.Unlock()
		cmd.Process.Kill()
		cmd.Wait()
		return session.ErrClosed
	}
	d.p = p
	d.mu.Unlock()
	d.tools, d.phases, d.streamed, d.unknown = map[string]*session.ToolCall{}, map[string]string{}, map[string]bool{}, map[string]bool{}
	d.post(session.Event{Type: session.EventStarted})
	go d.read(p, stdout)

	if err := d.open(ctx, p, from); err != nil {
		kill, cancel := context.WithCancel(context.Background())
		cancel()
		d.stop(kill, p)
		return err
	}
	return nil
}

func (d *driver) open(ctx context.Context, p *proc, from session.Origin) error {
	res, err := d.call(ctx, p, "initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "pitwall", "title": "pitwall", "version": "0"},
		"capabilities": map[string]bool{"experimentalApi": true},
	})
	if err != nil {
		return err
	}
	var init struct{ UserAgent string }
	json.Unmarshal(res, &init)
	if err := p.write(message{Method: "initialized"}); err != nil {
		return err
	}

	params := map[string]any{"approvalPolicy": "on-request", "sandbox": "workspace-write"}
	switch d.cfg.Mode {
	case "bypassPermissions":
		params["approvalPolicy"] = "never"
	case "plan":
		params["sandbox"] = "read-only"
	}
	if d.cfg.Cwd != "" {
		params["cwd"] = d.cfg.Cwd
	}
	if d.cfg.Model != "" {
		params["model"] = d.cfg.Model
	}
	method := "thread/start"
	switch {
	case from.Fork:
		// lastTurnId keeps the turns through it; the new thread's id
		// replaces the resume id below.
		method = "thread/fork"
		params["threadId"] = from.Resume
		if from.At != "" {
			params["lastTurnId"] = from.At
		}
	case from.Resume != "":
		method = "thread/resume"
		params["threadId"] = from.Resume
	}
	if res, err = d.call(ctx, p, method, params); err != nil {
		return err
	}
	var r struct {
		Thread struct{ ID string }
		Model  string
	}
	if json.Unmarshal(res, &r); r.Thread.ID == "" {
		return &session.ProviderError{Op: method, Message: "the response has no thread id"}
	}
	d.mu.Lock()
	d.thread, d.model = r.Thread.ID, r.Model
	d.mu.Unlock()
	d.post(session.Event{Type: session.EventResumeID, ResumeID: r.Thread.ID, Version: version(init.UserAgent)})
	d.post(session.Event{Type: session.EventState, State: session.StateReady})
	return nil
}

// version is the CLI version in an app-server user agent,
// "codex_cli_rs/0.166.0 (Linux; x86_64) ...".
func version(userAgent string) string {
	_, v, _ := strings.Cut(userAgent, "/")
	v, _, _ = strings.Cut(v, " ")
	return v
}

// call sends a request and waits for its response.
func (d *driver) call(ctx context.Context, p *proc, method string, params any) (json.RawMessage, error) {
	d.mu.Lock()
	if p.calls == nil {
		d.mu.Unlock()
		return nil, session.ErrClosed
	}
	p.next++
	id := p.next
	c := &call{method: method, reply: make(chan reply, 1)}
	key := strconv.FormatInt(id, 10)
	p.calls[key] = c
	d.mu.Unlock()
	forget := func() {
		d.mu.Lock()
		delete(p.calls, key)
		d.mu.Unlock()
	}
	if err := p.write(message{ID: id, Method: method, Params: params}); err != nil {
		forget()
		return nil, err
	}
	select {
	case r := <-c.reply:
		return r.result, r.err
	case <-ctx.Done():
		forget()
		return nil, ctx.Err()
	}
}

func (p *proc) write(m message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	p.wmu.Lock()
	defer p.wmu.Unlock()
	_, err = p.stdin.Write(append(b, '\n'))
	return err
}

// stop ends p: stdin closed, then killed when ctx ends.
func (d *driver) stop(ctx context.Context, p *proc) {
	d.mu.Lock()
	p.stopping = true
	d.mu.Unlock()
	p.stdin.Close()
	select {
	case <-p.done:
	case <-ctx.Done():
		p.cmd.Process.Kill()
		<-p.done
	}
}

// read handles p's output until it exits, then reports the exit. A crash
// fails the open turn and leaves no server; the next Send starts one.
func (d *driver) read(p *proc, stdout io.Reader) {
	r := bufio.NewReader(stdout)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			d.handle(p, line)
		}
		if err != nil {
			break
		}
	}
	p.cmd.Wait()
	code := p.cmd.ProcessState.ExitCode() // -1 on a signal

	d.mu.Lock()
	calls, stopping := p.calls, p.stopping
	p.calls = nil
	d.mu.Unlock()
	msg := fmt.Sprintf("Codex app-server exited (code %d)", code)
	if t := strings.TrimSpace(p.stderr.String()); t != "" {
		msg += ": " + t
	}
	for _, c := range calls {
		c.reply <- reply{err: fmt.Errorf("%w: %s", session.ErrClosed, msg)}
	}
	exited := session.Event{Type: session.EventExited, ExitCode: &code}
	if stopping && code == -1 {
		exited.ExitCode = nil
	}
	if !stopping {
		d.post(session.Event{Type: session.EventError, Message: msg})
	}
	d.post(exited)

	d.mu.Lock()
	d.p, d.turn = nil, ""
	clear(d.requests) // session.exited resolves them in the snapshot
	d.mu.Unlock()
	close(p.done)
}

func (d *driver) handle(p *proc, line []byte) {
	var m struct {
		ID     json.RawMessage
		Method string
		Params json.RawMessage
		Result json.RawMessage
		Error  *rpcError
	}
	if json.Unmarshal(line, &m) != nil {
		return
	}
	switch {
	case m.Method == "" && m.ID != nil:
		d.mu.Lock()
		c := p.calls[string(m.ID)]
		delete(p.calls, string(m.ID))
		if c != nil && c.method == "turn/start" && m.Error == nil {
			// The turn runs from here, before turn/started arrives, so a
			// second Send already sees it.
			var r struct{ Turn struct{ ID string } }
			json.Unmarshal(m.Result, &r)
			d.turn = r.Turn.ID
		}
		d.mu.Unlock()
		if c == nil {
			return
		}
		if m.Error != nil {
			c.reply <- reply{err: &session.ProviderError{Op: c.method, Message: m.Error.Message}}
			return
		}
		c.reply <- reply{result: m.Result}
	case m.ID != nil:
		d.request(p, m.ID, m.Method, m.Params)
	default:
		d.notify(m.Method, m.Params)
	}
}

// tail keeps the last 4 KB written to it.
type tail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tail) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, b...)
	if n := len(t.buf) - 4096; n > 0 {
		t.buf = t.buf[n:]
	}
	return len(b), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

func (d *driver) Send(ctx context.Context, m session.Message) (string, error) {
	d.cmdMu.Lock()
	defer d.cmdMu.Unlock()
	d.mu.Lock()
	p := d.p
	ended := p != nil && p.calls == nil
	d.mu.Unlock()
	if ended {
		<-p.done // its exit is being reported; then no server runs
	}
	d.mu.Lock()
	p, closing, busy := d.p, d.closing, d.turn != ""
	d.mu.Unlock()
	switch {
	case closing:
		return "", session.ErrClosed
	case busy:
		return "", session.ErrBusy
	case p == nil:
		if err := d.restart(ctx); err != nil {
			return "", err
		}
		d.mu.Lock()
		p = d.p
		d.mu.Unlock()
	}

	d.mu.Lock()
	params := map[string]any{"threadId": d.thread, "input": input(m)}
	d.mu.Unlock()
	if m.ID != "" {
		params["clientUserMessageId"] = m.ID
	}
	if d.cfg.Effort != "" {
		params["effort"] = d.cfg.Effort // thread/start has no effort field
	}
	res, err := d.call(ctx, p, "turn/start", params)
	if err != nil {
		return "", err
	}
	var r struct{ Turn struct{ ID string } }
	json.Unmarshal(res, &r)
	return r.Turn.ID, nil
}

// input is m as turn/start and turn/steer take it: a skill item first, as
// aide's buildCodexTurnInput puts it.
func input(m session.Message) []map[string]string {
	var in []map[string]string
	if m.Skill != nil && m.Skill.Path != "" {
		in = append(in, map[string]string{"type": "skill", "name": m.Skill.Name, "path": m.Skill.Path})
	}
	in = append(in, map[string]string{"type": "text", "text": m.Text})
	for _, im := range m.Images {
		in = append(in, map[string]string{"type": "image", "url": "data:" + im.MimeType + ";base64," + base64.StdEncoding.EncodeToString(im.Data)})
	}
	return in
}

// restart starts a server for the thread a crashed one had open.
func (d *driver) restart(ctx context.Context) error {
	t := now()
	recent := d.restarts[:0]
	for _, at := range d.restarts {
		if t.Sub(at) < time.Minute {
			recent = append(recent, at)
		}
	}
	d.restarts = recent
	if len(recent) >= maxRestarts {
		return &session.ProviderError{Op: "restart", Message: fmt.Sprintf("Codex app-server crashed %d times in the last minute", len(recent))}
	}
	d.restarts = append(d.restarts, t)
	d.mu.Lock()
	thread := d.thread
	d.mu.Unlock()
	return d.start(ctx, session.Origin{Resume: thread})
}

func (d *driver) Steer(ctx context.Context, m session.Message) error {
	if m.Skill != nil {
		return session.ErrUnsupported // aide: provider skills cannot steer an active Codex turn
	}
	d.cmdMu.Lock()
	defer d.cmdMu.Unlock()
	return d.steer(ctx, m)
}

// steer adds m to the running turn. The caller holds cmdMu.
func (d *driver) steer(ctx context.Context, m session.Message) error {
	d.mu.Lock()
	p, thread, turn, closing := d.p, d.thread, d.turn, d.closing
	d.mu.Unlock()
	switch {
	case closing:
		return session.ErrClosed
	case p == nil || turn == "":
		return session.ErrIdle
	}
	params := map[string]any{"threadId": thread, "expectedTurnId": turn, "input": input(m)}
	if m.ID != "" {
		params["clientUserMessageId"] = m.ID
	}
	_, err := d.call(ctx, p, "turn/steer", params)
	return err
}

// Interrupt asks the server to stop the turn; turn/completed with status
// interrupted ends it.
func (d *driver) Interrupt(ctx context.Context) error {
	d.cmdMu.Lock()
	defer d.cmdMu.Unlock()
	d.mu.Lock()
	p, thread, turn := d.p, d.thread, d.turn
	d.mu.Unlock()
	if p == nil || turn == "" {
		return nil
	}
	_, err := d.call(ctx, p, "turn/interrupt", map[string]string{"threadId": thread, "turnId": turn})
	return err
}

// Set (thread/settings/update) is not wired yet.
func (d *driver) Set(context.Context, session.Settings) error { return session.ErrUnsupported }

func (d *driver) Close(ctx context.Context) error {
	d.mu.Lock()
	if d.closing {
		d.mu.Unlock()
		return nil
	}
	d.closing = true
	p := d.p
	d.mu.Unlock()
	if p != nil {
		d.stop(ctx, p)
	}
	// A command in flight fails once its server is gone; wait it out so no
	// event follows the close of the channel.
	d.cmdMu.Lock()
	defer d.cmdMu.Unlock()
	close(d.events)
	<-d.emitted
	return nil
}

func logUnknown(d *driver, method string) {
	if !d.unknown[method] {
		d.unknown[method] = true
		log.Printf("codex: chat %s: unhandled notification %s", d.cfg.ID, method)
	}
}
