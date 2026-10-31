// Package claude drives Claude Code through its stream-json mode: one
// `claude -p` process per chat, user turns written to its stdin as JSON
// lines, and what it prints mapped to session events. docs/managed-sessions.md
// ("Claude driver") is the design and docs/research/claude-stream-json.md
// the protocol as captured.
package claude

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/session"
)

// CommandEnv names the command to run instead of `claude` from PATH.
const CommandEnv = "PITWALL_CLAUDE_COMMAND"

// Provider starts Claude drivers.
type Provider struct{}

var _ session.Provider = Provider{}

// Start runs claude in cfg.Cwd. The CLI prints nothing until the first
// user message, so it reports ready once the process is up.
func (Provider) Start(ctx context.Context, cfg session.Config, from session.Origin, emit func(session.Event)) (session.Driver, error) {
	name := os.Getenv(CommandEnv)
	if name == "" {
		name = "claude"
	}
	id := from.Resume
	if id == "" {
		id = newUUID()
	}
	c := exec.Command(name, args(cfg, from, id)...)
	c.Dir = cfg.Cwd
	c.Env = append(os.Environ(), cfg.Env...)
	stdin, err := c.StdinPipe()
	if err != nil {
		return nil, err
	}
	// An os.Pipe rather than StdoutPipe: Wait closes StdoutPipe as soon as
	// the process exits, which can drop its last lines.
	pr, pw, err := os.Pipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	c.Stdout = pw
	c.WaitDelay = waitDelay
	d := &driver{
		cmd:    c,
		stdin:  stdin,
		emit:   emit,
		stderr: &tail{},
		wake:   make(chan struct{}, 1),
		eof:    make(chan struct{}),
		exited: make(chan struct{}),
		done:   make(chan struct{}),
		s:      newStream(id),
	}
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
	d.mu.Lock()
	d.post(session.Event{Type: session.EventState, State: session.StateReady})
	if from.Resume == "" {
		d.post(session.Event{Type: session.EventResumeID, ResumeID: id})
	}
	d.mu.Unlock()
	go d.emitLoop()
	go d.read(pr)
	go d.wait(pr)
	return d, nil
}

// args is the command line for cfg started from `from`, with id as the
// session id of a new session. --permission-prompt-tool stdio makes the
// CLI ask on stdout (can_use_tool) instead of denying on the spot.
func args(cfg session.Config, from session.Origin, id string) []string {
	a := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--include-partial-messages", "--permission-prompt-tool", "stdio"}
	if from.Resume == "" {
		a = append(a, "--session-id", id)
	} else {
		a = append(a, "--resume", from.Resume)
		if from.At != "" {
			a = append(a, "--resume-session-at", from.At)
		}
		if from.Fork {
			a = append(a, "--fork-session")
		}
	}
	for _, f := range [][2]string{{"--model", cfg.Model}, {"--effort", cfg.Effort}, {"--permission-mode", cfg.Mode}} {
		if f[1] != "" {
			a = append(a, f[0], f[1])
		}
	}
	return a
}

type driver struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	emit   func(session.Event)
	stderr *tail
	wake   chan struct{} // a post since emitLoop last looked
	eof    chan struct{} // closed once stdout is read to its end
	exited chan struct{} // closed once the process is gone and session.exited is queued
	done   chan struct{} // closed once emitLoop has emitted everything

	mu      sync.Mutex
	s       *stream
	queue   []session.Event
	dead    bool // the process is gone; nothing is posted after its session.exited
	closing bool // Close was called
	reqs    int  // control requests the driver sent
}

// post queues e for emitLoop, stamped now. d.mu is held.
func (d *driver) post(e session.Event) {
	e.CreatedAt = session.Stamp(time.Now())
	d.queue = append(d.queue, e)
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// emitLoop calls emit from one goroutine, so emit may call back into the
// driver, until the process is gone and every event is out.
func (d *driver) emitLoop() {
	defer close(d.done)
	for {
		d.mu.Lock()
		q, dead := d.queue, d.dead
		d.queue = nil
		d.mu.Unlock()
		for _, e := range q {
			d.emit(e)
		}
		if len(q) > 0 {
			continue
		}
		if dead {
			return
		}
		<-d.wake
	}
}

// write sends one JSON line to the CLI. d.mu is held.
func (d *driver) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := d.stdin.Write(append(b, '\n')); err != nil {
		return session.ErrClosed
	}
	return nil
}

// waitDelay is how long the driver waits, after the CLI exits, for a
// child it left behind to let go of stdout and stderr.
const waitDelay = 2 * time.Second

// read maps every stdout line to events until stdout ends.
func (d *driver) read(stdout io.Reader) {
	defer close(d.eof)
	r := bufio.NewReader(stdout)
	for {
		b, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(b)) > 0 {
			d.mu.Lock()
			for _, e := range d.s.line(b) {
				d.post(e)
			}
			if reply := d.s.reply; reply != nil {
				d.s.reply = nil
				d.write(reply) // shortcut: a failed reply means the CLI is exiting, and the exit reports it
			}
			d.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// wait reports the CLI's exit once its output is read.
func (d *driver) wait(stdout io.Closer) {
	err := d.cmd.Wait()
	select {
	case <-d.eof:
	case <-time.After(waitDelay):
		stdout.Close()
		<-d.eof
	}
	stdout.Close()
	d.mu.Lock()
	var code *int
	if st := d.cmd.ProcessState; st != nil && st.Exited() {
		code = new(int)
		*code = st.ExitCode()
	}
	if !d.closing {
		msg := "Claude exited"
		if code != nil {
			msg = fmt.Sprintf("Claude exited (code %d)", *code)
		} else if err != nil {
			msg = "Claude exited: " + err.Error()
		}
		if t := d.stderr.String(); t != "" {
			msg += "\n" + t
		}
		d.post(session.Event{Type: session.EventError, Message: msg})
	}
	d.post(session.Event{Type: session.EventExited, ExitCode: code})
	d.dead = true
	d.mu.Unlock()
	close(d.exited)
}

func (d *driver) Send(ctx context.Context, m session.Message) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case d.dead || d.closing:
		return "", session.ErrClosed
	case d.s.turn != "":
		return "", session.ErrBusy
	}
	if m.ID == "" {
		m.ID = newUUID()
	}
	if err := d.write(userLine(m)); err != nil {
		return "", err
	}
	d.s.turn = m.ID
	d.post(session.Event{Type: session.EventTurnStarted, TurnID: m.ID})
	return m.ID, nil
}

// userLine is m as a stream-json user message. The CLI keeps uuid, so it
// names the turn in the transcript too.
func userLine(m session.Message) map[string]any {
	content := []map[string]any{}
	for _, img := range m.Images {
		content = append(content, map[string]any{"type": "image", "source": map[string]any{
			"type": "base64", "media_type": img.MimeType, "data": base64.StdEncoding.EncodeToString(img.Data)}})
	}
	if m.Text != "" {
		content = append(content, map[string]any{"type": "text", "text": m.Text})
	}
	return map[string]any{"type": "user", "uuid": m.ID, "session_id": "", "parent_tool_use_id": nil,
		"message": map[string]any{"role": "user", "content": content}}
}

// Steer is unsupported: the spike never captured a user line written
// mid-turn joining that turn, so the chat loop queues it for the next.
func (d *driver) Steer(context.Context, session.Message) error { return session.ErrUnsupported }

// Interrupt stops the running turn. The CLI withdraws its open asks
// (control_cancel_request) and ends the turn with an aborted result.
func (d *driver) Interrupt(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.s.turn == "" {
		return nil
	}
	return d.request(ctx, map[string]any{"subtype": "interrupt"})
}

// Approve answers a can_use_tool ask. Allowing ExitPlanMode lets Claude
// implement its plan; denying it with a reason asks for another.
func (d *driver) Approve(ctx context.Context, requestID string, dec session.Decision) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	a, err := d.take(ctx, requestID)
	if err != nil {
		return err
	}
	if !dec.Allow {
		msg := dec.Reason
		if msg == "" {
			msg = "The pitwall user denied this."
		}
		return d.respond(requestID, map[string]any{"behavior": "deny", "message": msg})
	}
	input := dec.Input
	if input == nil {
		input = a.input
	}
	r := map[string]any{"behavior": "allow", "updatedInput": input}
	if dec.ForSession {
		// The CLI's own "always allow" rules, kept for this session only.
		// shortcut: no capture of updatedPermissions exists (sdk.d.ts is the
		// spec); an ask with no addRules suggestion is allowed once.
		var rules []map[string]any
		for _, raw := range a.suggestions {
			var sg map[string]any
			if json.Unmarshal(raw, &sg) == nil && sg["type"] == "addRules" {
				sg["destination"] = "session"
				rules = append(rules, sg)
			}
		}
		if rules != nil {
			r["updatedPermissions"] = rules
		}
	}
	return d.respond(requestID, r)
}

// Answer answers AskUserQuestion: its input plus the answers keyed by
// question text, several labels joined as Claude Code joins them.
func (d *driver) Answer(ctx context.Context, requestID string, answers map[string][]string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	a, err := d.take(ctx, requestID)
	if err != nil {
		return err
	}
	in := map[string]any{}
	json.Unmarshal(a.input, &in)
	joined := map[string]string{}
	for q, labels := range answers {
		joined[q] = strings.Join(labels, ", ")
	}
	in["answers"] = joined
	return d.respond(requestID, map[string]any{"behavior": "allow", "updatedInput": in})
}

// take removes the open ask requestID. d.mu is held.
func (d *driver) take(ctx context.Context, requestID string) (ask, error) {
	if err := ctx.Err(); err != nil {
		return ask{}, err
	}
	if d.dead || d.closing {
		return ask{}, session.ErrClosed
	}
	a, ok := d.s.pending[requestID]
	if !ok {
		return ask{}, &session.ProviderError{Op: "answer", Message: "Claude is not waiting on " + requestID}
	}
	delete(d.s.pending, requestID)
	return a, nil
}

// respond writes the success control_response to a CLI request. d.mu is
// held.
func (d *driver) respond(requestID string, response map[string]any) error {
	return d.write(map[string]any{"type": "control_response", "response": map[string]any{
		"subtype": "success", "request_id": requestID, "response": response}})
}

// Set changes the model, effort and permission mode, one control request
// each.
func (d *driver) Set(ctx context.Context, s session.Settings) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range []struct {
		value string
		req   map[string]any
	}{
		{s.Model, map[string]any{"subtype": "set_model", "model": s.Model}},
		{s.Effort, map[string]any{"subtype": "apply_flag_settings", "settings": map[string]any{"effortLevel": s.Effort}}},
		{s.Mode, map[string]any{"subtype": "set_permission_mode", "mode": s.Mode}},
	} {
		if r.value == "" {
			continue
		}
		if err := d.request(ctx, r.req); err != nil {
			return err
		}
	}
	return nil
}

// request writes a control request to the CLI. d.mu is held.
func (d *driver) request(ctx context.Context, req map[string]any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.dead || d.closing {
		return session.ErrClosed
	}
	d.reqs++
	return d.write(map[string]any{"type": "control_request", "request_id": fmt.Sprintf("req_%d", d.reqs), "request": req})
}

// Close closes stdin, which ends the CLI, and kills it when ctx ends
// first. It returns once every event is out.
func (d *driver) Close(ctx context.Context) error {
	d.mu.Lock()
	if !d.closing {
		d.closing = true
		d.stdin.Close()
	}
	d.mu.Unlock()
	select {
	case <-d.exited:
	case <-ctx.Done():
		d.cmd.Process.Kill()
		<-d.exited
	}
	<-d.done
	return nil
}

// tail keeps the last 4 KB written to it, for the exit message.
type tail struct {
	mu sync.Mutex
	b  []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if over := len(t.b) - 4096; over > 0 {
		t.b = t.b[over:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}

// newUUID is a random (version 4) UUID.
func newUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
