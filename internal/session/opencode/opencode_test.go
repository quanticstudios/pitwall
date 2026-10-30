package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/eventlog"
)

type call struct {
	Method, Path string
	Body         map[string]any
}

type fakeServer struct {
	*httptest.Server
	t      *testing.T
	mu     sync.Mutex
	calls  []call
	stream chan chan string
	fail   string
}

func fake(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{t: t, stream: make(chan chan string, 10)}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(f.Server.Close)
	return f
}

func (f *fakeServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if got := r.URL.Query().Get("directory"); got != "/workspace/with space" {
		f.t.Errorf("directory = %q", got)
	}
	if r.URL.Path == "/event" {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		stream := make(chan string, 100)
		f.stream <- stream
		for {
			select {
			case b, ok := <-stream:
				if !ok {
					return
				}
				fmt.Fprint(w, b)
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
				return
			}
		}
	}
	var body map[string]any
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&body)
	}
	f.mu.Lock()
	f.calls = append(f.calls, call{r.Method, r.URL.Path, body})
	fail := f.fail
	f.mu.Unlock()
	if fail == r.URL.Path {
		http.Error(w, "refused", 409)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch r.Method + " " + r.URL.Path {
	case "GET /global/health":
		io.WriteString(w, `{"healthy":true,"version":"1.19.4"}`)
	case "POST /session", "GET /session/ses_fake", "PATCH /session/ses_fake":
		io.WriteString(w, `{"id":"ses_fake"}`)
	case "GET /session/missing":
		http.NotFound(w, r)
	case "POST /session/ses_fake/prompt_async":
		w.WriteHeader(204)
	case "POST /session/ses_fake/abort":
		io.WriteString(w, `true`)
	default:
		if strings.HasPrefix(r.URL.Path, "/permission/") || strings.HasPrefix(r.URL.Path, "/question/") {
			io.WriteString(w, `true`)
		} else {
			f.t.Errorf("unexpected endpoint %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}
}

func (f *fakeServer) count(method, path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.Method == method && c.Path == path {
			n++
		}
	}
	return n
}

func start(t *testing.T, f *fakeServer, from session.Origin) (session.Driver, chan session.Event, chan string) {
	t.Helper()
	events := make(chan session.Event, 100)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	d, err := (Provider{URL: f.URL}).Start(ctx, session.Config{Cwd: "/workspace/with space"}, from, func(e session.Event) { events <- e })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := d.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	var stream chan string
	select {
	case stream = <-f.stream:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	return d, events, stream
}

func next(t *testing.T, events <-chan session.Event, typ session.EventType) session.Event {
	t.Helper()
	select {
	case e := <-events:
		if e.Type != typ {
			t.Fatalf("got %s, want %s: %+v", e.Type, typ, e)
		}
		if e.CreatedAt == "" {
			t.Fatal("missing timestamp")
		}
		return e
	case <-time.After(5 * time.Second):
		t.Fatalf("waiting for %s", typ)
	}
	return session.Event{}
}

func ready(t *testing.T, events <-chan session.Event) session.Event {
	t.Helper()
	next(t, events, session.EventStarted)
	id := next(t, events, session.EventResumeID)
	if id.ResumeID != "ses_fake" || id.Version != "1.19.4" {
		t.Fatalf("resume event: %+v", id)
	}
	if e := next(t, events, session.EventState); e.State != session.StateReady {
		t.Fatal(e)
	}
	return id
}

func push(stream chan string, typ string, props any) {
	b, _ := json.Marshal(map[string]any{"type": typ, "properties": props})
	stream <- "data: " + string(b) + "\n\n"
}

func idle(stream chan string) {
	push(stream, "session.status", map[string]any{"sessionID": "ses_fake", "status": map[string]string{"type": "idle"}})
}

func TestTranscript(t *testing.T) {
	f := fake(t)
	d, events, stream := start(t, f, session.Origin{})
	ready(t, events)
	ctx := context.Background()
	if _, err := d.Send(ctx, session.Message{ID: "local-turn", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	next(t, events, session.EventTurnStarted)
	file, err := os.Open("testdata/turn.sse")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	b, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	stream <- string(b)
	want := []struct {
		kind session.ItemKind
		text string
	}{
		{session.KindReasoningText, "Thinking"}, {session.KindAssistantText, "Hel"}, {session.KindAssistantText, "lo"},
	}
	for _, w := range want {
		e := next(t, events, session.EventContent)
		if e.Kind != w.kind || e.Text != w.text {
			t.Fatalf("content: %+v, want %+v", e, w)
		}
	}
	for _, status := range []string{"running", "completed"} {
		e := next(t, events, session.EventContent)
		if e.Tool == nil || e.Tool.Status != status || e.Tool.CallID != "call_1" || e.Tool.Command != "pwd" {
			t.Fatalf("tool: %+v", e)
		}
	}
	usage := next(t, events, session.EventUsage)
	if usage.InputTokens != 12 || usage.OutputTokens != 7 || usage.CachedInputTokens == nil || *usage.CachedInputTokens != 3 {
		t.Fatal(usage)
	}
	completed := next(t, events, session.EventTurnCompleted)
	if completed.State != session.TurnCompleted || completed.TurnID != "local-turn" || completed.Point != "msg_assistant" {
		t.Fatal(completed)
	}
	// Deprecated session.idle events cannot end the next turn or emit twice.
	push(stream, "session.idle", map[string]string{"sessionID": "ses_fake"})
	if _, err := d.Send(ctx, session.Message{Text: "again"}); err != nil {
		t.Fatal(err)
	}
	next(t, events, session.EventTurnStarted)
}

func TestResumeFromSavedConfig(t *testing.T) {
	f := fake(t)
	d, events, stream := start(t, f, session.Origin{})
	id := ready(t, events)
	if _, err := d.Send(context.Background(), session.Message{ID: "first", Text: "remember"}); err != nil {
		t.Fatal(err)
	}
	next(t, events, session.EventTurnStarted)
	idle(stream)
	next(t, events, session.EventTurnCompleted)
	// Exercise the actual persistence seam without implementing the daemon's
	// generic restart loop, which belongs to ENG-2687.
	dir := t.TempDir()
	log, _, _, _, err := eventlog.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := log.WriteConfig(session.Config{Format: 1, Cwd: "/workspace/with space", Resume: id.ResumeID}); err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	log, cfg, _, _, err := eventlog.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	d2, events2, stream2 := start(t, f, session.Origin{Resume: cfg.Resume})
	ready(t, events2)
	if _, err := d2.Send(context.Background(), session.Message{ID: "second", Text: "continue"}); err != nil {
		t.Fatal(err)
	}
	next(t, events2, session.EventTurnStarted)
	idle(stream2)
	if e := next(t, events2, session.EventTurnCompleted); e.TurnID != "second" {
		t.Fatal(e)
	}
	if f.count("POST", "/session") != 1 || f.count("GET", "/session/ses_fake") != 1 || f.count("PATCH", "/session/ses_fake") != 1 {
		t.Fatal("resume created a new session or did not attach")
	}
}

func TestPermissionsAndQuestions(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		decision    session.Decision
	}{
		{"once", "once", session.Decision{Allow: true}},
		{"session", "always", session.Decision{Allow: true, ForSession: true}},
		{"deny", "reject", session.Decision{Reason: "do not run"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fake(t)
			d, events, stream := start(t, f, session.Origin{})
			ready(t, events)
			ctx := context.Background()
			if _, err := d.Send(ctx, session.Message{Text: "run"}); err != nil {
				t.Fatal(err)
			}
			next(t, events, session.EventTurnStarted)
			push(stream, "permission.asked", map[string]any{"id": "per_1", "sessionID": "ses_fake", "permission": "bash", "patterns": []string{"pwd"}, "metadata": map[string]string{"command": "pwd"}})
			e := next(t, events, session.EventRequestOpened)
			if e.RequestType != session.RequestCommand || e.Request.Command != "pwd" {
				t.Fatal(e)
			}
			if err := d.Approve(ctx, "per_1", session.Decision{Allow: true, Input: json.RawMessage(`{}`)}); !errors.Is(err, session.ErrUnsupported) {
				t.Fatal(err)
			}
			f.mu.Lock()
			f.fail = "/permission/per_1/reply"
			f.mu.Unlock()
			var pe *session.ProviderError
			if err := d.Approve(ctx, "per_1", tc.decision); !errors.As(err, &pe) {
				t.Fatalf("refusal = %v", err)
			}
			f.mu.Lock()
			f.fail = ""
			f.mu.Unlock()
			if err := d.Approve(ctx, "per_1", tc.decision); err != nil {
				t.Fatal(err)
			}
			next(t, events, session.EventRequestResolved)
			if err := d.Approve(ctx, "per_1", tc.decision); err == nil {
				t.Fatal("duplicate approved")
			}
			f.mu.Lock()
			got := f.calls[len(f.calls)-1].Body
			f.mu.Unlock()
			if got["reply"] != tc.reply || got["message"] != tc.decision.Reason {
				t.Fatal(got)
			}
			push(stream, "permission.replied", map[string]string{"sessionID": "ses_fake", "requestID": "per_1", "reply": tc.reply})
			push(stream, "question.asked", map[string]any{"sessionID": "ses_fake", "id": "que_1", "questions": []any{
				map[string]any{"header": "Choice", "question": "Which?", "options": []map[string]string{{"label": "A", "description": "first"}}},
				map[string]any{"question": "Why?", "options": []any{}},
			}})
			q := next(t, events, session.EventInputRequested)
			if len(q.Questions) != 2 || q.Questions[0].ID != "0" || q.Questions[0].Options[0].ID != "A" {
				t.Fatal(q)
			}
			if err := d.Answer(ctx, "que_1", map[string][]string{"0": {"A"}}); err == nil {
				t.Fatal("partial answer accepted")
			}
			if err := d.Answer(ctx, "que_1", map[string][]string{"0": {"A"}, "1": {"because"}}); err != nil {
				t.Fatal(err)
			}
			next(t, events, session.EventInputResolved)
			f.mu.Lock()
			got = f.calls[len(f.calls)-1].Body
			f.mu.Unlock()
			if !reflect.DeepEqual(got["answers"], []any{[]any{"A"}, []any{"because"}}) {
				t.Fatal(got)
			}
		})
	}
}

func TestCommandsAndInterrupt(t *testing.T) {
	f := fake(t)
	d, events, stream := start(t, f, session.Origin{})
	ready(t, events)
	ctx := context.Background()
	if err := d.Steer(ctx, session.Message{}); !errors.Is(err, session.ErrIdle) {
		t.Fatal(err)
	}
	if err := d.Interrupt(ctx); err != nil {
		t.Fatal(err)
	}
	if f.count("POST", "/session/ses_fake/abort") != 0 {
		t.Fatal("idle aborted")
	}
	if err := d.Set(ctx, session.Settings{Model: "anthropic/claude", Effort: "high", Mode: "plan"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Set(ctx, session.Settings{Mode: "bypassPermissions"}); !errors.Is(err, session.ErrUnsupported) {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := d.Send(canceled, session.Message{Text: "no"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := d.Send(ctx, session.Message{ID: "turn", Text: "look", Images: []session.Image{{MimeType: "image/png", Name: "pic.png", Data: []byte{1, 2}}}}); err != nil {
		t.Fatal(err)
	}
	next(t, events, session.EventTurnStarted)
	f.mu.Lock()
	body := f.calls[len(f.calls)-1].Body
	f.mu.Unlock()
	if body["agent"] != "plan" || body["variant"] != "high" || !reflect.DeepEqual(body["model"], map[string]any{"providerID": "anthropic", "modelID": "claude"}) {
		t.Fatal(body)
	}
	if body["parts"].([]any)[1].(map[string]any)["url"] != "data:image/png;base64,AQI=" {
		t.Fatal(body)
	}
	if _, err := d.Send(ctx, session.Message{}); !errors.Is(err, session.ErrBusy) {
		t.Fatal(err)
	}
	if err := d.Steer(ctx, session.Message{}); !errors.Is(err, session.ErrUnsupported) {
		t.Fatal(err)
	}
	push(stream, "permission.asked", map[string]any{"sessionID": "ses_fake", "id": "per_pending", "permission": "edit"})
	if e := next(t, events, session.EventRequestOpened); e.RequestType != session.RequestFileChange {
		t.Fatal(e)
	}
	if err := d.Interrupt(ctx); err != nil {
		t.Fatal(err)
	}
	push(stream, "session.status", map[string]any{"sessionID": "ses_fake", "status": map[string]string{"type": "idle"}})
	next(t, events, session.EventRequestResolved)
	if e := next(t, events, session.EventTurnCompleted); e.State != session.TurnInterrupted {
		t.Fatal(e)
	}
	if err := d.Close(ctx); err != nil {
		t.Fatal(err)
	}
	next(t, events, session.EventExited)
	if _, err := d.Send(ctx, session.Message{}); !errors.Is(err, session.ErrClosed) {
		t.Fatal(err)
	}
	select {
	case e := <-events:
		t.Fatalf("event after Close: %+v", e)
	default:
	}
}

func TestStreamFailureAndProviderError(t *testing.T) {
	for _, tc := range []struct {
		name, record string
		state        string
	}{
		{"disconnect", "", session.TurnFailed},
		{"malformed", "data: invalid\n\n", session.TurnFailed},
		{"provider", "data: {\"type\":\"session.error\",\"properties\":{\"sessionID\":\"ses_fake\",\"error\":{\"name\":\"APIError\",\"data\":{\"message\":\"bad model\"}}}}\n\n", session.TurnFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fake(t)
			d, events, stream := start(t, f, session.Origin{})
			ready(t, events)
			if _, err := d.Send(context.Background(), session.Message{ID: "turn", Text: "go"}); err != nil {
				t.Fatal(err)
			}
			next(t, events, session.EventTurnStarted)
			if tc.record != "" {
				stream <- tc.record
			}
			if tc.name == "provider" {
				idle(stream)
			}
			if tc.name != "provider" {
				close(stream)
				next(t, events, session.EventError)
			}
			if e := next(t, events, session.EventTurnCompleted); e.State != tc.state {
				t.Fatal(e)
			}
		})
	}
}

func TestStartRefusals(t *testing.T) {
	f := fake(t)
	for _, from := range []session.Origin{{Fork: true}, {At: "message"}, {Resume: "missing"}} {
		if _, err := (Provider{URL: f.URL}).Start(context.Background(), session.Config{Cwd: "/workspace/with space"}, from, func(session.Event) {}); err == nil {
			t.Fatalf("accepted %+v", from)
		}
	}
	for _, settings := range []session.Settings{{Model: "bare-model"}, {Model: "provider/"}, {Mode: "unknown"}} {
		if err := validSettings(settings); err == nil {
			t.Fatalf("accepted %+v", settings)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Provider{URL: f.URL}).Start(ctx, session.Config{Cwd: "/workspace/with space"}, session.Origin{}, func(session.Event) {}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestSSEMultilineAndFiltering(t *testing.T) {
	f := fake(t)
	d, events, stream := start(t, f, session.Origin{})
	ready(t, events)
	if _, err := d.Send(context.Background(), session.Message{Text: "go"}); err != nil {
		t.Fatal(err)
	}
	next(t, events, session.EventTurnStarted)
	stream <- ": heartbeat\n\ndata: {\"type\":\"session.idle\",\ndata: \"properties\":{\"sessionID\":\"other\"}}\n\n"
	push(stream, "message.updated", map[string]any{"info": map[string]string{"sessionID": "ses_fake", "id": "msg_user", "role": "user"}})
	push(stream, "message.part.updated", map[string]any{"part": map[string]string{"sessionID": "ses_fake", "messageID": "msg_user", "id": "prt_user", "type": "text", "text": "echo"}})
	idle(stream)
	next(t, events, session.EventTurnCompleted)
}

// The helper process exercises the real serve command lifecycle on all CI OSes.
func TestMain(m *testing.M) {
	if os.Getenv("PITWALL_OPENCODE_HELPER") != "1" {
		os.Exit(m.Run())
	}
	if !reflect.DeepEqual(os.Args[1:], []string{"serve", "--hostname=127.0.0.1", "--port=0"}) {
		os.Exit(3)
	}
	if os.Getenv("PITWALL_OPENCODE_HELPER_ACTION") == "exit" {
		os.Exit(4)
	}
	if os.Getenv("PITWALL_OPENCODE_HELPER_ACTION") == "hang" {
		time.Sleep(time.Hour)
		os.Exit(5)
	}
	cwd, _ := os.Getwd()
	if cwd != os.Getenv("PITWALL_OPENCODE_HELPER_CWD") {
		os.Exit(6)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(7)
	}
	fmt.Println("opencode server listening on http://" + l.Addr().String())
	http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, _ := r.BasicAuth()
		if username != "opencode" || password == "" || password != os.Getenv("OPENCODE_SERVER_PASSWORD") {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/global/health":
			io.WriteString(w, `{"healthy":true,"version":"1.19.4"}`)
		case "/session":
			io.WriteString(w, `{"id":"ses_fake"}`)
		case "/session/ses_fake/abort":
			if err := os.WriteFile(filepath.Join(cwd, "aborted"), []byte("aborted"), 0600); err != nil {
				w.WriteHeader(500)
				return
			}
			io.WriteString(w, `true`)
		case "/event":
			w.Header().Set("Content-Type", "text/event-stream")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	os.Exit(0)
}

func TestOwnedServer(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(CommandEnv, exe)
	dir := t.TempDir()
	// Windows Getwd may normalize a path's spelling; use EvalSymlinks on
	// Unix as well because macOS's temp dir often contains /var symlinks.
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := session.Config{Cwd: dir, Env: []string{"PITWALL_OPENCODE_HELPER=1", "PITWALL_OPENCODE_HELPER_CWD=" + dir}}
	events := make(chan session.Event, 100)
	d, err := (Provider{}).Start(context.Background(), cfg, session.Origin{}, func(e session.Event) { events <- e })
	if err != nil {
		t.Fatal(err)
	}
	ready(t, events)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.Close(ctx); err != nil {
		t.Fatal(err)
	}
	next(t, events, session.EventExited)
	if _, err := os.Stat(filepath.Join(dir, "aborted")); err != nil {
		t.Fatal("server stopped before the abort completed:", err)
	}
	for _, action := range []string{"exit", "hang"} {
		cfg.Env = append(cfg.Env, "PITWALL_OPENCODE_HELPER_ACTION="+action)
		timeout := 3 * time.Second
		if action == "hang" {
			timeout = 100 * time.Millisecond
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		_, _, _, err := serve(ctx, cfg)
		cancel()
		if err == nil {
			t.Fatalf("%s unexpectedly ready", action)
		}
		if action == "exit" && !strings.Contains(err.Error(), "exited before readiness") {
			t.Fatal(err)
		}
	}
}
