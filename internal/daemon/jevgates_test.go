package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/decide"
	"github.com/quanticstudios/pitwall/internal/session/fake"
	"github.com/quanticstudios/pitwall/internal/web"
)

// jevAnswers is a TypeSafe that answers every question with p.
type jevAnswers struct{ p atomic.Uint64 }

func (j *jevAnswers) set(p float64) { j.p.Store(math.Float64bits(p)) }

func (j *jevAnswers) RoundTrip(r *http.Request) (*http.Response, error) {
	var req struct {
		Questions map[string]json.RawMessage `json:"questions"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	answers := map[string]any{}
	for k := range req.Questions {
		answers[k] = map[string]any{"noul": math.Float64frombits(j.p.Load()), "type": "noul"}
	}
	b, _ := json.Marshal(map[string]any{"answers": answers, "model": "jev-1.13.0", "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}})
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(b)), Request: r}, nil
}

// jevDaemon is webDaemon with gates whose Jev answers what jev says.
func jevDaemon(t *testing.T, jev *jevAnswers) string {
	t.Helper()
	d, _, stop := runDaemon(t, &fakes{statsCalls: map[string]int{}}, func(o *Options) {
		chatProvider(fake.Options{})(o)
		o.Gates = decide.GatesWith(jev)
	})
	t.Cleanup(stop)
	ws := &web.Server{Token: func() (string, error) { return webToken, nil }, Chats: chatHub{d}}
	hs := httptest.NewServer(ws.Handler())
	t.Cleanup(func() { ws.Close(); hs.Close() })
	return strings.TrimPrefix(hs.URL, "http://")
}

func tempDir(t *testing.T) string {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// readUntil reads chat until ok holds of its snapshot.
func readUntil(t *testing.T, c *wsClient, chat, what string, ok func(r map[string]any) bool) map[string]any {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		r := c.call(map[string]any{"type": "read", "sessionId": chat})
		if ok(r) {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %v", what, r)
		}
	}
}

func hasText(r map[string]any, kind, prefix string) bool {
	items, _ := at(r, "data.snapshot.content").([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		if text, _ := m["text"].(string); m["kind"] == kind && strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}

// TestJevConsent: an aide chat starts no turn and queues nothing without
// aide's key, or with its workspace on aide's consent list; with consent
// given the turn runs, the transcript keeps the typed prompt, and the key
// never comes back. A chat another client starts has no gates, and only
// aide hands over its settings.
func TestJevConsent(t *testing.T) {
	jev := &jevAnswers{}
	jev.set(0.5)
	addr := jevDaemon(t, jev)
	c := dialWeb(t, addr, aideClient)
	dir := tempDir(t)
	r := c.call(map[string]any{"type": "start", "dir": dir, "provider": "claude"})
	chat, _ := at(r, "data.sessionId").(string)
	if chat == "" {
		t.Fatalf("start: %v", r)
	}
	send := func(how string) map[string]any {
		return c.call(map[string]any{"type": "send", "sessionId": chat, "text": "hello", "how": how})
	}
	for _, how := range []string{"", "queue"} {
		if r := send(how); r["code"] != "jev" || r["message"] != decide.CredentialRequired {
			t.Errorf("no key, %q: %v", how, r)
		}
	}
	if r := dialWeb(t, addr, "phone").call(map[string]any{"type": "setJev", "apiKey": "test-key"}); r["code"] != "invalid" {
		t.Errorf("setJev from another client: %v", r)
	}
	if r := c.call(map[string]any{"type": "setJev", "apiKey": "has space"}); r["code"] != "invalid" {
		t.Errorf("a bad key: %v", r)
	}
	if r := c.call(map[string]any{"type": "setJev", "apiKey": "test-key", "disabledRoots": []string{dir}}); r["ok"] != true || r["data"] != nil {
		t.Fatalf("setJev: %v", r)
	}
	if r := send(""); r["code"] != "jev" || r["message"] != decide.ConsentRequired {
		t.Errorf("consent refused: %v", r)
	}
	c.call(map[string]any{"type": "setJev", "apiKey": "test-key"})
	if r := send(""); r["ok"] != true {
		t.Fatalf("consent given: %v", r)
	}
	r = readUntil(t, c, chat, "the reply", func(r map[string]any) bool { return hasText(r, "assistant-text", "Handled: hello") })
	if !hasText(r, "user-prompt", "hello") || hasText(r, "user-prompt", "hello\n") {
		t.Errorf("the transcript's prompt is not what was typed: %v", at(r, "data.snapshot.content"))
	}
	if b, _ := json.Marshal(r); strings.Contains(string(b), "test-key") {
		t.Error("the key came back in a read")
	}

	other := dialWeb(t, addr, "web-session")
	c.call(map[string]any{"type": "setJev"})
	r = other.call(map[string]any{"type": "start", "dir": tempDir(t), "provider": "claude", "prompt": "hi"})
	if r["ok"] != true {
		t.Errorf("a chat another client started: %v", r)
	}
}

// TestJevToolGate: with the tool gate off an approval waits for the user;
// on, Jev allows what it is sure is safe and denies what it is sure is
// not, each with a line in the transcript, and leaves the rest to the
// user.
func TestJevToolGate(t *testing.T) {
	jev := &jevAnswers{}
	addr := jevDaemon(t, jev)
	c := dialWeb(t, addr, aideClient)
	approval := func(p float64) (string, map[string]any) {
		t.Helper()
		jev.set(p)
		r := c.call(map[string]any{"type": "start", "dir": tempDir(t), "provider": "claude"})
		chat, _ := at(r, "data.sessionId").(string)
		if r := c.call(map[string]any{"type": "send", "sessionId": chat, "text": "approval"}); r["ok"] != true {
			t.Fatalf("send: %v", r)
		}
		return chat, readUntil(t, c, chat, fmt.Sprintf("p %v", p), func(r map[string]any) bool {
			return at(r, "data.snapshot.pendingApprovals.0") != nil || hasText(r, "system", "Jev ")
		})
	}
	c.call(map[string]any{"type": "setJev", "apiKey": "test-key"})
	if _, r := approval(0.01); at(r, "data.snapshot.pendingApprovals.0") == nil {
		t.Errorf("gate off: %v", r)
	}
	on := map[string]any{"type": "setJev", "apiKey": "test-key", "gates": map[string]any{"global": map[string]any{"toolGate": map[string]any{"enabled": true}}}}
	if r := c.call(on); r["ok"] != true {
		t.Fatalf("setJev: %v", r)
	}
	for _, tc := range []struct {
		p    float64
		line string
	}{{0.01, "Jev allowed: command-execution-approval. Jev allow:"}, {0.95, "Jev denied: command-execution-approval. Jev deny:"}} {
		chat, r := approval(tc.p)
		if !hasText(r, "system", tc.line) || at(r, "data.snapshot.pendingApprovals.0") != nil {
			t.Errorf("p %v: %v", tc.p, at(r, "data.snapshot.content"))
		}
		readUntil(t, c, chat, "the turn's end", func(r map[string]any) bool { return at(r, "data.snapshot.turns.0.state") == "completed" })
	}
	if _, r := approval(0.5); at(r, "data.snapshot.pendingApprovals.0") == nil || hasText(r, "system", "Jev ") {
		t.Errorf("unsure: %v", r)
	}
}

// TestJevQueued: a queued message whose turn Jev refuses when it comes up
// stays first in the queue, held, with the reason in the transcript.
func TestJevQueued(t *testing.T) {
	jev := &jevAnswers{}
	jev.set(0.5)
	c := dialWeb(t, jevDaemon(t, jev), aideClient)
	dir := tempDir(t)
	c.call(map[string]any{"type": "setJev", "apiKey": "test-key"})
	r := c.call(map[string]any{"type": "start", "dir": dir, "provider": "claude"})
	chat, _ := at(r, "data.sessionId").(string)
	c.call(map[string]any{"type": "send", "sessionId": chat, "text": "approval"})
	readUntil(t, c, chat, "the approval", func(r map[string]any) bool { return at(r, "data.snapshot.pendingApprovals.0") != nil })
	if r := c.call(map[string]any{"type": "send", "sessionId": chat, "text": "next", "how": "queue"}); r["ok"] != true {
		t.Fatalf("queue: %v", r)
	}
	c.call(map[string]any{"type": "setJev", "apiKey": "test-key", "disabledRoots": []string{dir}})
	c.call(map[string]any{"type": "resolveApproval", "sessionId": chat, "decision": "allow"})
	readUntil(t, c, chat, "the refusal", func(r map[string]any) bool {
		return hasText(r, "system", "The queued message did not start: "+decide.ConsentRequired)
	})
	if r := c.call(map[string]any{"type": "listSessions"}); at(r, "data.chats.0.queued") != 1.0 {
		t.Errorf("the message left the queue: %v", r)
	}
}
