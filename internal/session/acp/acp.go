// Package acp drives agents over ACP v1's newline-delimited JSON-RPC stdio
// transport. The wire contract is schema-v1.28.0; see docs/acp.md.
package acp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

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
	c := exec.Command(p.Command, p.Args...)
	c.Dir = cfg.Cwd
	c.Env = append(append(os.Environ(), p.Env...), cfg.Env...)
	c.WaitDelay = waitDelay
	stdin, err := c.StdinPipe()
	if err != nil {
		return nil, err
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	c.Stdout = pw
	d := &driver{cmd: c, stdin: stdin, emit: emit, stderr: &tail{}, wake: make(chan struct{}, 1), eof: make(chan struct{}), exited: make(chan struct{}), done: make(chan struct{}), pending: map[string]chan rpcMessage{}, s: newStream()}
	c.Stderr = d.stderr
	err = ctx.Err()
	if err == nil {
		err = c.Start()
	}
	pw.Close()
	if err != nil {
		pr.Close()
		stdin.Close()
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
	if err = d.Set(ctx, session.Settings{Model: cfg.Model, Effort: cfg.Effort, Mode: cfg.Mode}); err != nil {
		return fail(err)
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

// request writes with mu held. Responses either complete a synchronous call
// or, for session/prompt, end the turn in line.
func (d *driver) request(method string, params any, ch chan rpcMessage) (string, error) {
	if d.dead || d.closing {
		return "", session.ErrClosed
	}
	d.next++
	id := strconv.Itoa(d.next)
	if ch != nil {
		d.pending[id] = ch
	}
	err := d.write(map[string]any{"jsonrpc": "2.0", "id": d.next, "method": method, "params": params})
	if err != nil {
		delete(d.pending, id)
	}
	return id, err
}

func (d *driver) call(ctx context.Context, method string, params, result any) error {
	ch := make(chan rpcMessage, 1)
	d.mu.Lock()
	id, err := d.request(method, params, ch)
	d.mu.Unlock()
	if err != nil {
		return err
	}
	defer func() { d.mu.Lock(); delete(d.pending, id); d.mu.Unlock() }()
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
	defer d.mu.Unlock()
	if d.dead || d.closing {
		return "", session.ErrClosed
	}
	if d.s.turn != "" {
		return "", session.ErrBusy
	}
	if len(m.Images) > 0 && !d.s.images {
		return "", session.ErrUnsupported
	}
	content := []map[string]any{}
	if m.Text != "" {
		content = append(content, map[string]any{"type": "text", "text": m.Text})
	}
	for _, img := range m.Images {
		content = append(content, map[string]any{"type": "image", "mimeType": img.MimeType, "data": base64.StdEncoding.EncodeToString(img.Data)})
	}
	id, err := d.request("session/prompt", map[string]any{"sessionId": d.s.id, "prompt": content}, nil)
	if err != nil {
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
	d.post(session.Event{Type: session.EventTurnStarted, TurnID: m.ID})
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
	defer d.mu.Unlock()
	if d.dead || d.closing {
		return session.ErrClosed
	}
	if d.s.turn == "" {
		return nil
	}
	if err := d.write(map[string]any{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]any{"sessionId": d.s.id}}); err != nil {
		return err
	}
	d.s.cancelled = true
	return d.cancelRequests()
}

func (d *driver) Approve(ctx context.Context, id string, decision session.Decision) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dead || d.closing {
		return session.ErrClosed
	}
	p, ok := d.s.permissions[id]
	if !ok {
		return &session.ProviderError{Op: "approve", Message: "request is no longer pending"}
	}
	if len(decision.Input) > 0 || decision.Reason != "" {
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
	for _, o := range p.Options {
		if o.Kind == kind {
			if err := d.reply(p.ID, map[string]any{"outcome": map[string]string{"outcome": "selected", "optionId": o.OptionID}}); err != nil {
				return err
			}
			delete(d.s.permissions, id)
			d.post(session.Event{Type: session.EventRequestResolved, RequestID: id})
			return nil
		}
	}
	return fmt.Errorf("ACP permission option %s: %w", kind, session.ErrUnsupported)
}

// Answer accepts a form elicitation with the answered fields; with none it
// declines, which claude-agent-acp tells the model as skipped questions.
func (d *driver) Answer(ctx context.Context, id string, answers map[string][]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dead || d.closing {
		return session.ErrClosed
	}
	in, ok := d.s.inputs[id]
	if !ok {
		return &session.ProviderError{Op: "answer", Message: "request is no longer pending"}
	}
	result := map[string]any{"action": "decline"}
	if content := in.content(answers); len(content) > 0 {
		result = map[string]any{"action": "accept", "content": content}
	}
	if err := d.reply(in.ID, result); err != nil {
		return err
	}
	delete(d.s.inputs, id)
	d.post(session.Event{Type: session.EventInputResolved, RequestID: id})
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

func (d *driver) reply(id json.RawMessage, result any) error {
	return d.write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

// cancelRequests answers every open permission and elicitation with the
// cancelled outcome ACP requires once a turn is cancelled or over.
func (d *driver) cancelRequests() error {
	for id, p := range d.s.permissions {
		if err := d.reply(p.ID, map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}); err != nil {
			return err
		}
		delete(d.s.permissions, id)
		d.post(session.Event{Type: session.EventRequestResolved, RequestID: id})
	}
	for id, in := range d.s.inputs {
		if err := d.reply(in.ID, map[string]string{"action": "cancel"}); err != nil {
			return err
		}
		delete(d.s.inputs, id)
		d.post(session.Event{Type: session.EventInputResolved, RequestID: id})
	}
	return nil
}
