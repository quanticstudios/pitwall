// Package opencode drives OpenCode's HTTP server and SSE stream. The wire
// contract is the v1.19.4 SDK's v2 generated types. See README.md for sources.
package opencode

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quanticstudios/pitwall/internal/session"
)

// Provider starts one OpenCode server per chat. URL attaches to an existing
// server instead, which the driver never shuts down. Client defaults to a
// standard HTTP client without a timeout because /event is a long-lived SSE.
type Provider struct {
	URL    string
	Client *http.Client
}

var _ session.Provider = Provider{}
var _ session.Driver = (*driver)(nil)

func (p Provider) Start(ctx context.Context, cfg session.Config, from session.Origin, emit func(session.Event)) (session.Driver, error) {
	if from.At != "" || from.Fork {
		return nil, session.ErrUnsupported
	}
	if err := validSettings(session.Settings{Model: cfg.Model, Effort: cfg.Effort, Mode: cfg.Mode}); err != nil {
		return nil, err
	}
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	stop := func(context.Context) {}
	password := ""
	if p.URL == "" {
		var err error
		p.URL, password, stop, err = serve(startup, cfg)
		if err != nil {
			return nil, err
		}
	}
	life, end := context.WithCancel(context.Background())
	d := &driver{url: strings.TrimRight(p.URL, "/"), cwd: cfg.Cwd, password: password, client: p.Client,
		ctx: life, cancel: end, stop: stop, emit: emit, done: make(chan struct{}),
		shutdown: make(chan struct{}),
		wake:     make(chan struct{}, 1), settings: session.Settings{Model: cfg.Model, Effort: cfg.Effort, Mode: cfg.Mode},
		s: newStream(from.Resume)}
	if d.client == nil {
		d.client = &http.Client{}
	}
	fail := func(err error) (session.Driver, error) { end(); stop(startup); return nil, err }
	var health struct {
		Healthy bool   `json:"healthy"`
		Version string `json:"version"`
	}
	if err := d.request(startup, "GET", "/global/health", nil, &health); err != nil {
		return fail(err)
	}
	if !health.Healthy {
		return fail(fmt.Errorf("OpenCode server is unhealthy"))
	}
	var info struct {
		ID string `json:"id"`
	}
	op, path, body := "GET", "/session/"+url.PathEscape(from.Resume), any(nil)
	if from.Resume == "" {
		op, path = "POST", "/session"
		// Managed calls require a person's answer, regardless of local defaults.
		body = map[string]any{"permission": []map[string]string{{"permission": "*", "pattern": "*", "action": "ask"}}}
	}
	if err := d.request(startup, op, path, body, &info); err != nil {
		return fail(err)
	}
	if info.ID == "" || from.Resume != "" && info.ID != from.Resume {
		return fail(fmt.Errorf("OpenCode returned an invalid session id"))
	}
	d.s.id = info.ID
	// Reassert managed permissions when reattaching too.
	if from.Resume != "" {
		if err := d.request(startup, "PATCH", "/session/"+url.PathEscape(info.ID), map[string]any{
			"permission": []map[string]string{{"permission": "*", "pattern": "*", "action": "ask"}}}, nil); err != nil {
			return fail(err)
		}
	}
	req, err := http.NewRequestWithContext(life, "GET", d.endpoint("/event"), nil)
	if err != nil {
		return fail(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	if d.password != "" {
		req.SetBasicAuth("opencode", d.password)
	}
	// Bound the handshake but keep the established event stream alive.
	stopHandshake := context.AfterFunc(startup, end)
	res, err := d.client.Do(req)
	stopHandshake()
	if err == nil && startup.Err() != nil {
		res.Body.Close()
		return fail(startup.Err())
	}
	if err != nil {
		return fail(err)
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return fail(&session.ProviderError{Op: "GET /event", Message: res.Status})
	}
	d.post(session.Event{Type: session.EventStarted})
	d.post(session.Event{Type: session.EventResumeID, ResumeID: info.ID, Version: health.Version})
	d.post(session.Event{Type: session.EventState, State: session.StateReady})
	go d.emitLoop()
	go d.read(res.Body)
	return d, nil
}

type driver struct {
	url, cwd, password string
	client             *http.Client
	ctx                context.Context
	cancel             context.CancelFunc
	stop               func(context.Context)
	emit               func(session.Event)
	done, wake         chan struct{}
	shutdown           chan struct{} // Close finished aborting and stopping the server
	mu                 sync.Mutex
	s                  *stream
	settings           session.Settings
	queue              []session.Event
	dead               bool
	closing            atomic.Bool
}

func (d *driver) endpoint(path string) string {
	return d.url + path + "?" + url.Values{"directory": {d.cwd}}.Encode()
}

func (d *driver) request(ctx context.Context, method, path string, body, out any) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	unhook := context.AfterFunc(d.ctx, cancel)
	defer unhook()
	return d.exchange(ctx, method, path, body, out)
}

// exchange also serves Close's abort after the driver's lifetime is canceled.
func (d *driver) exchange(ctx context.Context, method, path string, body, out any) error {
	var b bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&b).Encode(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, d.endpoint(path), &b)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if d.password != "" {
		req.SetBasicAuth("opencode", d.password)
	}
	res, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return &session.ProviderError{Op: method + " " + path, Message: res.Status}
	}
	if out != nil {
		return json.NewDecoder(res.Body).Decode(out)
	}
	return nil
}

// post is called with mu held, except before the goroutines start.
func (d *driver) post(e session.Event) {
	e.CreatedAt = session.Stamp(time.Now())
	d.queue = append(d.queue, e)
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

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

// read dispatches whole SSE records, including multiline data. A lost stream
// ends the driver rather than silently losing calls or replaying partial text.
func (d *driver) read(body io.ReadCloser) {
	defer body.Close()
	s := bufio.NewScanner(body)
	s.Buffer(make([]byte, 4096), 8<<20)
	var data []string
	var wireErr error
	for s.Scan() {
		line := s.Text()
		if line == "" && len(data) > 0 {
			d.mu.Lock()
			wireErr = d.s.line([]byte(strings.Join(data, "\n")), d.post)
			d.mu.Unlock()
			data = nil
			if wireErr != nil {
				break
			}
		} else if v, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimPrefix(v, " "))
		}
	}
	d.cancel()
	if d.closing.Load() {
		<-d.shutdown
	} else {
		d.stop(context.Background())
	}
	d.mu.Lock()
	if !d.closing.Load() {
		if wireErr == nil {
			wireErr = s.Err()
		}
		if wireErr == nil {
			wireErr = io.EOF
		}
		d.post(session.Event{Type: session.EventError, Message: "OpenCode event stream ended: " + wireErr.Error()})
	}
	state := session.TurnInterrupted
	if !d.closing.Load() {
		state = session.TurnFailed
	}
	d.s.complete(state, "OpenCode event stream ended", d.post)
	d.post(session.Event{Type: session.EventExited})
	d.dead = true
	d.mu.Unlock()
}

func validSettings(s session.Settings) error {
	if s.Mode != "" && s.Mode != "default" && s.Mode != "plan" {
		return session.ErrUnsupported
	}
	if s.Model != "" {
		p, m, ok := strings.Cut(s.Model, "/")
		if !ok || p == "" || m == "" {
			return fmt.Errorf("OpenCode model must be provider/model")
		}
	}
	return nil
}

func (d *driver) Send(ctx context.Context, m session.Message) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dead || d.closing.Load() {
		return "", session.ErrClosed
	}
	if d.s.turn != "" {
		return "", session.ErrBusy
	}
	if m.ID == "" {
		m.ID = "turn_" + rand.Text()
	}
	parts := []map[string]any{}
	if m.Text != "" {
		parts = append(parts, map[string]any{"type": "text", "text": m.Text})
	}
	for _, img := range m.Images {
		parts = append(parts, map[string]any{"type": "file", "mime": img.MimeType, "filename": img.Name,
			"url": "data:" + img.MimeType + ";base64," + base64.StdEncoding.EncodeToString(img.Data)})
	}
	body := map[string]any{"parts": parts}
	// Pitwall item IDs are not OpenCode's validated msg_* IDs. OpenCode
	// allocates the wire message ID; the local ID remains the turn ID.
	if d.settings.Model != "" {
		p, m, _ := strings.Cut(d.settings.Model, "/")
		body["model"] = map[string]string{"providerID": p, "modelID": m}
	}
	if d.settings.Effort != "" {
		body["variant"] = d.settings.Effort
	}
	if d.settings.Mode != "" {
		agent := "build"
		if d.settings.Mode == "plan" {
			agent = "plan"
		}
		body["agent"] = agent
	}
	if err := d.request(ctx, "POST", "/session/"+url.PathEscape(d.s.id)+"/prompt_async", body, nil); err != nil {
		return "", err
	}
	d.s.begin(m.ID)
	d.post(session.Event{Type: session.EventTurnStarted, TurnID: m.ID})
	return m.ID, nil
}

func (d *driver) Steer(ctx context.Context, _ session.Message) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dead || d.closing.Load() {
		return session.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.s.turn == "" {
		return session.ErrIdle
	}
	return session.ErrUnsupported
}

func (d *driver) Interrupt(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dead || d.closing.Load() {
		return session.ErrClosed
	}
	if d.s.turn == "" {
		return nil
	}
	// Mark before the request, so the idle status cannot win the race.
	d.s.interrupted = true
	if err := d.request(ctx, "POST", "/session/"+url.PathEscape(d.s.id)+"/abort", nil, nil); err != nil {
		d.s.interrupted = false
		return err
	}
	return nil
}

func (d *driver) Approve(ctx context.Context, id string, decision session.Decision) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dead || d.closing.Load() {
		return session.ErrClosed
	}
	if _, ok := d.s.permissions[id]; !ok {
		return &session.ProviderError{Op: "approve", Message: "unknown or resolved request"}
	}
	if len(decision.Input) > 0 {
		return session.ErrUnsupported
	}
	reply := "reject"
	if decision.Allow {
		reply = "once"
		if decision.ForSession {
			reply = "always"
		}
	}
	if err := d.request(ctx, "POST", "/permission/"+url.PathEscape(id)+"/reply", map[string]string{"reply": reply, "message": decision.Reason}, nil); err != nil {
		return err
	}
	delete(d.s.permissions, id)
	d.post(session.Event{Type: session.EventRequestResolved, RequestID: id})
	return nil
}

func (d *driver) Answer(ctx context.Context, id string, answers map[string][]string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dead || d.closing.Load() {
		return session.ErrClosed
	}
	questions, ok := d.s.questions[id]
	if !ok {
		return &session.ProviderError{Op: "answer", Message: "unknown or resolved request"}
	}
	ordered := make([][]string, len(questions))
	if len(answers) != len(questions) {
		return fmt.Errorf("answer every OpenCode question")
	}
	for i, q := range questions {
		var found bool
		ordered[i], found = answers[q.ID]
		if !found {
			return fmt.Errorf("missing answer for %s", q.ID)
		}
	}
	if err := d.request(ctx, "POST", "/question/"+url.PathEscape(id)+"/reply", map[string]any{"answers": ordered}, nil); err != nil {
		return err
	}
	delete(d.s.questions, id)
	d.post(session.Event{Type: session.EventInputResolved, RequestID: id})
	return nil
}

func (d *driver) Set(ctx context.Context, s session.Settings) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dead || d.closing.Load() {
		return session.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validSettings(s); err != nil {
		return err
	}
	if s.Model != "" {
		d.settings.Model = s.Model
	}
	if s.Effort != "" {
		d.settings.Effort = s.Effort
	}
	if s.Mode != "" {
		d.settings.Mode = s.Mode
	}
	return nil
}

func (d *driver) Close(ctx context.Context) error {
	if !d.closing.Swap(true) {
		// Release an in-flight command before taking mu. Abort through an
		// independent request, then stop the owned server politely.
		d.cancel()
		abort, cancel := context.WithTimeout(ctx, 2*time.Second)
		d.exchange(abort, "POST", "/session/"+url.PathEscape(d.s.id)+"/abort", nil, nil)
		cancel()
		d.stop(ctx)
		close(d.shutdown)
	}
	<-d.done
	return nil
}
