// Package acp drives agents over ACP v1's newline-delimited JSON-RPC stdio
// transport. The wire contract is schema-v1.28.0; see docs/acp.md.
package acp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/quanticstudios/pitwall/internal/childproc"
	"github.com/quanticstudios/pitwall/internal/session"
)

// Provider starts a configured ACP executable without a shell.
type Provider struct {
	Command string
	Args    []string
	Env     []string
}

var _ session.Provider = Provider{}
var _ session.Driver = (*driver)(nil)

func (p Provider) Start(ctx context.Context, cfg session.Config, from session.Origin, emit func(session.Event)) (session.Driver, error) {
	if from.Fork || from.At != "" {
		return nil, fmt.Errorf("ACP fork and rewind: %w", session.ErrUnsupported)
	}
	if !filepath.IsAbs(cfg.Cwd) {
		return nil, fmt.Errorf("ACP cwd must be absolute")
	}
	if p.Command == "" {
		return nil, fmt.Errorf("ACP command is empty")
	}
	procCtx, procCancel := context.WithCancel(context.Background())
	c := exec.CommandContext(procCtx, p.Command, p.Args...)
	childproc.Bound(c)
	c.Dir = cfg.Cwd
	c.Env = append(append(os.Environ(), p.Env...), cfg.Env...)
	c.WaitDelay = waitDelay
	stdin, err := c.StdinPipe()
	if err != nil {
		procCancel()
		return nil, err
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		stdin.Close()
		procCancel()
		return nil, err
	}
	c.Stdout = pw
	d := &driver{cmd: c, cancel: procCancel, stdin: stdin, emit: emit, stderr: &tail{}, wake: make(chan struct{}, 1), eof: make(chan struct{}), exited: make(chan struct{}), done: make(chan struct{}), pending: map[string]chan rpcMessage{}, s: newStream()}
	c.Stderr = d.stderr
	err = ctx.Err()
	if err == nil {
		err = c.Start()
	}
	pw.Close()
	if err != nil {
		pr.Close()
		stdin.Close()
		procCancel()
		return nil, err
	}
	go d.emitLoop()
	go d.read(pr)
	go d.wait(pr)
	// A silent or unresponsive executable must not hold the daemon forever.
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	fail := func(err error) (session.Driver, error) {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), waitDelay)
		defer closeCancel()
		d.Close(closeCtx)
		return nil, err
	}
	var init struct {
		ProtocolVersion   int `json:"protocolVersion"`
		AgentCapabilities struct {
			LoadSession        bool `json:"loadSession"`
			PromptCapabilities struct {
				Image bool `json:"image"`
			} `json:"promptCapabilities"`
		} `json:"agentCapabilities"`
		AgentInfo struct {
			Version string `json:"version"`
		} `json:"agentInfo"`
	}
	if err = d.call(ctx, "initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": clientCapabilities, "clientInfo": map[string]string{"name": "pitwall", "version": "1"}}, &init); err != nil {
		return fail(err)
	}
	if init.ProtocolVersion != 1 {
		return fail(fmt.Errorf("ACP protocol version %d: %w", init.ProtocolVersion, session.ErrUnsupported))
	}
	if from.Resume != "" && !init.AgentCapabilities.LoadSession {
		return fail(fmt.Errorf("ACP agent does not support session/load: %w", session.ErrUnsupported))
	}
	params := map[string]any{"cwd": cfg.Cwd, "mcpServers": []any{}}
	method := "session/new"
	if from.Resume != "" {
		method = "session/load"
		params["sessionId"] = from.Resume
	}
	var setup struct {
		SessionID     string         `json:"sessionId"`
		Modes         *modeState     `json:"modes"`
		ConfigOptions []configOption `json:"configOptions"`
	}
	if err = d.call(ctx, method, params, &setup); err != nil {
		return fail(err)
	}
	if from.Resume != "" {
		setup.SessionID = from.Resume
	}
	if setup.SessionID == "" {
		return fail(fmt.Errorf("ACP session/new returned an empty sessionId"))
	}
	d.mu.Lock()
	if d.dead || d.closing {
		d.mu.Unlock()
		return fail(session.ErrClosed)
	}
	d.s.id = setup.SessionID
	d.s.images = init.AgentCapabilities.PromptCapabilities.Image
	for _, e := range d.s.configure(setup.Modes, setup.ConfigOptions) {
		d.post(e)
	}
	caps := []string{"non-resumable"}
	if init.AgentCapabilities.LoadSession {
		caps = []string{"session/load"}
	}
	d.post(session.Event{Type: session.EventResumeID, ResumeID: setup.SessionID, Version: init.AgentInfo.Version, Capabilities: caps})
	if !init.AgentCapabilities.LoadSession {
		d.post(session.Event{Type: session.EventContent, ItemID: "acp-resume", Kind: session.KindSystem, Text: "This ACP agent does not support session/load. This session is not resumable."})
	}
	d.post(session.Event{Type: session.EventState, State: session.StateReady})
	d.mu.Unlock()
	// Saved settings are best-effort: a model or mode the agent no longer
	// offers must not keep the chat from starting.
	var notices []session.Event
	for _, s := range []struct {
		name, value string
		set         session.Settings
	}{{"model", cfg.Model, session.Settings{Model: cfg.Model}}, {"effort", cfg.Effort, session.Settings{Effort: cfg.Effort}}, {"mode", cfg.Mode, session.Settings{Mode: cfg.Mode}}} {
		if s.value == "" {
			continue
		}
		var pe *session.ProviderError
		if err = d.Set(ctx, s.set); errors.Is(err, session.ErrUnsupported) || errors.As(err, &pe) {
			notices = append(notices, session.Event{Type: session.EventContent, Kind: session.KindSystem,
				ItemID: fmt.Sprintf("acp-setting-%s-%d", s.name, time.Now().UnixNano()),
				Text:   fmt.Sprintf("The saved %s %s was not applied: %v", s.name, s.value, err)})
		} else if err != nil {
			return fail(err)
		}
	}
	if len(notices) > 0 {
		d.mu.Lock()
		for _, e := range append(notices, d.s.configure(nil, nil)...) {
			d.post(e)
		}
		d.mu.Unlock()
	}
	return d, nil
}

// clientCapabilities are what pitwall answers besides prompts and
// permissions: form elicitations, which claude-agent-acp needs before it
// offers AskUserQuestion at all.
var clientCapabilities = map[string]any{"elicitation": map[string]any{"form": map[string]any{}}}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// prepareRequest allocates a JSON-RPC id and encodes the request with mu held.
func (d *driver) prepareRequest(method string, params any, ch chan rpcMessage) (string, []byte, error) {
	if d.dead || d.closing {
		return "", nil, session.ErrClosed
	}
	d.next++
	id := strconv.Itoa(d.next)
	b, err := encodeLine(map[string]any{"jsonrpc": "2.0", "id": d.next, "method": method, "params": params})
	if err != nil {
		return "", nil, err
	}
	if ch != nil {
		d.pending[id] = ch
	}
	return id, b, nil
}

func (d *driver) call(ctx context.Context, method string, params, result any) error {
	ch := make(chan rpcMessage, 1)
	d.mu.Lock()
	id, data, err := d.prepareRequest(method, params, ch)
	d.mu.Unlock()
	if err != nil {
		return err
	}
	defer func() { d.mu.Lock(); delete(d.pending, id); d.mu.Unlock() }()
	if err := d.writeBytes(data); err != nil {
		return err
	}
	select {
	case r, ok := <-ch:
		if !ok {
			return session.ErrClosed
		}
		if r.Error != nil {
			return &session.ProviderError{Op: method, Message: r.Error.Message}
		}
		return json.Unmarshal(r.Result, result)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *driver) Send(ctx context.Context, m session.Message) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	d.mu.Lock()
	if d.dead || d.closing {
		d.mu.Unlock()
		return "", session.ErrClosed
	}
	if d.s.turn != "" {
		d.mu.Unlock()
		return "", session.ErrBusy
	}
	if len(m.Images) > 0 && !d.s.images {
		d.mu.Unlock()
		return "", session.ErrUnsupported
	}
	content := []map[string]any{}
	if m.Text != "" {
		content = append(content, map[string]any{"type": "text", "text": m.Text})
	}
	for _, img := range m.Images {
		content = append(content, map[string]any{"type": "image", "mimeType": img.MimeType, "data": base64.StdEncoding.EncodeToString(img.Data)})
	}
	id, data, err := d.prepareRequest("session/prompt", map[string]any{"sessionId": d.s.id, "prompt": content}, nil)
	if err != nil {
		d.mu.Unlock()
		return "", err
	}
	if m.ID == "" {
		m.ID = "acp-turn-" + id
	}
	d.s.turn, d.s.prompt = m.ID, id
	d.s.chunk = ""
	d.s.cancelled = false
	d.s.tools = map[string]*session.ToolCall{}
	d.s.kinds = map[string]string{}
	d.sending++
	pos := len(d.queue)
	d.mu.Unlock()

	err = d.writeBytes(data)

	d.mu.Lock()
	d.sending--
	if err != nil {
		if d.s.turn == m.ID {
			d.s.turn, d.s.prompt = "", ""
		}
		d.notifyWake()
		d.mu.Unlock()
		return "", err
	}
	ev := session.Event{Type: session.EventTurnStarted, TurnID: m.ID, CreatedAt: session.Stamp(time.Now())}
	d.queue = slices.Insert(d.queue, min(pos, len(d.queue)), ev)
	d.notifyWake()
	d.mu.Unlock()
	return m.ID, nil
}

func (d *driver) Steer(ctx context.Context, _ session.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dead || d.closing {
		return session.ErrClosed
	}
	if d.s.turn == "" {
		return session.ErrIdle
	}
	return session.ErrUnsupported
}

func (d *driver) Interrupt(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	if d.dead || d.closing {
		d.mu.Unlock()
		return session.ErrClosed
	}
	if d.s.turn == "" {
		d.mu.Unlock()
		return nil
	}
	d.queueWrite(map[string]any{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]any{"sessionId": d.s.id}})
	d.s.cancelled = true
	d.cancelRequests()
	d.mu.Unlock()
	return d.flushWrites()
}

func (d *driver) Approve(ctx context.Context, id string, decision session.Decision) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	if d.dead || d.closing {
		d.mu.Unlock()
		return session.ErrClosed
	}
	p, ok := d.s.permissions[id]
	if !ok {
		d.mu.Unlock()
		return &session.ProviderError{Op: "approve", Message: "request is no longer pending"}
	}
	if len(decision.Input) > 0 || decision.Reason != "" {
		d.mu.Unlock()
		return session.ErrUnsupported
	}
	kind := "reject_once"
	if decision.Allow {
		kind = "allow_once"
	}
	if decision.ForSession {
		if decision.Allow {
			kind = "allow_always"
		} else {
			kind = "reject_always"
		}
	}
	var data []byte
	var found bool
	for _, o := range p.Options {
		if o.Kind == kind {
			var err error
			data, err = encodeLine(map[string]any{"jsonrpc": "2.0", "id": p.ID, "result": map[string]any{"outcome": map[string]string{"outcome": "selected", "optionId": o.OptionID}}})
			if err != nil {
				d.mu.Unlock()
				return err
			}
			delete(d.s.permissions, id)
			found = true
			break
		}
	}
	if !found {
		d.mu.Unlock()
		return fmt.Errorf("ACP permission option %s: %w", kind, session.ErrUnsupported)
	}
	d.sending++
	pos := len(d.queue)
	d.mu.Unlock()
	if err := d.writeBytes(data); err != nil {
		d.mu.Lock()
		d.sending--
		if !d.dead && !d.closing {
			d.s.permissions[id] = p
		}
		d.notifyWake()
		d.mu.Unlock()
		return err
	}
	d.mu.Lock()
	d.sending--
	ev := session.Event{Type: session.EventRequestResolved, RequestID: id, CreatedAt: session.Stamp(time.Now())}
	d.queue = slices.Insert(d.queue, min(pos, len(d.queue)), ev)
	d.notifyWake()
	d.mu.Unlock()
	return nil
}

// Answer accepts a form elicitation with the answered fields; with none it
// declines, which claude-agent-acp tells the model as skipped questions.
func (d *driver) Answer(ctx context.Context, id string, answers map[string][]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	if d.dead || d.closing {
		d.mu.Unlock()
		return session.ErrClosed
	}
	in, ok := d.s.inputs[id]
	if !ok {
		d.mu.Unlock()
		return &session.ProviderError{Op: "answer", Message: "request is no longer pending"}
	}
	result := map[string]any{"action": "decline"}
	if content := in.content(answers); len(content) > 0 {
		result = map[string]any{"action": "accept", "content": content}
	}
	data, err := encodeLine(map[string]any{"jsonrpc": "2.0", "id": in.ID, "result": result})
	if err != nil {
		d.mu.Unlock()
		return err
	}
	delete(d.s.inputs, id)
	d.sending++
	pos := len(d.queue)
	d.mu.Unlock()
	if err := d.writeBytes(data); err != nil {
		d.mu.Lock()
		d.sending--
		if !d.dead && !d.closing {
			d.s.inputs[id] = in
		}
		d.notifyWake()
		d.mu.Unlock()
		return err
	}
	d.mu.Lock()
	d.sending--
	ev := session.Event{Type: session.EventInputResolved, RequestID: id, CreatedAt: session.Stamp(time.Now())}
	d.queue = slices.Insert(d.queue, min(pos, len(d.queue)), ev)
	d.notifyWake()
	d.mu.Unlock()
	return nil
}

// Set sends session/set_mode when the agent advertised modes, and
// session/set_config_option for a mode, model or effort option it
// advertised; ErrUnsupported, before sending anything, for one it did not.
func (d *driver) Set(ctx context.Context, s session.Settings) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	if d.dead || d.closing {
		d.mu.Unlock()
		return session.ErrClosed
	}
	calls, err := d.s.settings(s)
	d.mu.Unlock()
	if err != nil {
		return err
	}
	for _, c := range calls {
		var r struct {
			ConfigOptions []configOption `json:"configOptions"`
		}
		if err := d.call(ctx, c.method, c.params, &r); err != nil {
			return err
		}
		d.mu.Lock()
		if c.method == "session/set_mode" {
			d.s.mode = s.Mode
		}
		if r.ConfigOptions != nil {
			for _, e := range d.s.configure(nil, r.ConfigOptions) {
				d.post(e)
			}
		}
		d.mu.Unlock()
	}
	return nil
}

func (d *driver) queueReply(id json.RawMessage, result any) {
	d.queueWrite(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

// cancelRequests answers every open permission and elicitation with the
// cancelled outcome ACP requires once a turn is cancelled or over. d.mu is held.
func (d *driver) cancelRequests() {
	for id, p := range d.s.permissions {
		d.queueReply(p.ID, map[string]any{"outcome": map[string]string{"outcome": "cancelled"}})
		delete(d.s.permissions, id)
		d.post(session.Event{Type: session.EventRequestResolved, RequestID: id})
	}
	for id, in := range d.s.inputs {
		d.queueReply(in.ID, map[string]string{"action": "cancel"})
		delete(d.s.inputs, id)
		d.post(session.Event{Type: session.EventInputResolved, RequestID: id})
	}
}
