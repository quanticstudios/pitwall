package daemon

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// remoteDaemon is a daemon with one tab of panes a (Claude), b (Codex)
// and c (pi), each asking permission, and the phone page on a paired
// device's token.
func remoteDaemon(t *testing.T) (*Daemon, *fakes, func(method, path, body string) (int, string)) {
	t.Helper()
	old, oldDelay, oldStill := replyDelay, settleDelay, stillFor
	replyDelay, settleDelay, stillFor = 0, 20*time.Millisecond, 10*time.Millisecond
	f := &fakes{statsCalls: map[string]int{}}
	f.saved = model.State{
		Sessions: []model.Session{{ID: "s", Name: "work"}},
		Workspaces: []model.Workspace{{ID: "w", SessionID: "s", Name: "w", Tabs: []model.Tab{{ID: "t", Layout: &layout.Node{
			Dir: layout.Horizontal, Children: []*layout.Node{{Pane: "a"}, {Pane: "b"}, {Pane: "c"}}}}}, ActiveTab: "t"}},
		Panes: []model.Pane{{ID: "a", WorkspaceID: "w"}, {ID: "b", WorkspaceID: "w"}, {ID: "c", WorkspaceID: "w"}},
	}
	o := f.options()
	o.RemoteDir = t.TempDir()
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// why: a key starts settle, which reads these, then takes d.mu; taking
		// d.mu after it has run orders those reads before the restore.
		time.Sleep(4 * settleDelay)
		d.mu.Lock()
		d.mu.Unlock()
		replyDelay, settleDelay, stillFor = old, oldDelay, oldStill
	})
	for i, p := range []struct {
		id string
		pr model.Provider
	}{{"a", model.ProviderClaude}, {"b", model.ProviderCodex}, {"c", model.ProviderPi}} {
		f.pane(i).mu.Lock()
		f.pane(i).screen = []string{"SECRET-SCROLLBACK token=abc", " Do you want to proceed?", " > 1. Yes", "   2. No", " Esc to cancel"}
		f.pane(i).mu.Unlock()
		if err := d.agentEvent(context.Background(), proto.AgentEvent{Pane: p.id, Provider: p.pr, Payload: []byte(model.StatePendingApproval)}); err != nil {
			t.Fatal(err)
		}
	}
	code, err := remote.Pair(o.RemoteDir, "pixel", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	h := d.remoteServer().Handler()
	token := ""
	do := func(method, path, body string) (int, string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	st, body := do("POST", "/api/pair", `{"code":"`+code+`"}`)
	var pr struct{ Token string }
	if json.Unmarshal([]byte(body), &pr); st != 200 || pr.Token == "" {
		t.Fatalf("pair: %d %s", st, body)
	}
	token = pr.Token
	return d, f, do
}

// gotAfter waits for pane p's input past its first n bytes to be want
// long, then returns it.
func gotAfter(t *testing.T, p *fakePane, n, want int) string {
	t.Helper()
	for start := time.Now(); len(p.got()) < n+want && time.Since(start) < 2*time.Second; {
		time.Sleep(time.Millisecond)
	}
	return p.got()[n:]
}

// items reads the page's items by pane.
func items(t *testing.T, do func(method, path, body string) (int, string)) (map[string]remote.Item, string) {
	t.Helper()
	st, body := do("GET", "/api/items", "")
	if st != 200 {
		t.Fatalf("items: %d %s", st, body)
	}
	var r struct{ Items []remote.Item }
	json.Unmarshal([]byte(body), &r)
	out := map[string]remote.Item{}
	for _, it := range r.Items {
		out[it.Pane] = it
	}
	return out, body
}

func TestRemoteAnswer(t *testing.T) {
	d, f, do := remoteDaemon(t)
	its, body := items(t, do)
	// The page shows state, never the screen.
	if strings.Contains(body, "SECRET") || strings.Contains(body, "proceed") || !strings.Contains(body, `"at":"`) {
		t.Fatalf("items leak the screen: %s", body)
	}
	if len(its) != 3 || !its["a"].Answer || !its["b"].Answer || its["c"].Answer || its["a"].Tab == "" || its["a"].Session != "work" {
		t.Fatalf("items %+v", its)
	}
	answer := func(pane string, allow bool) (int, string) {
		return do("POST", "/api/answer", fmt.Sprintf(`{"pane":%q,"at":"%d","allow":%v}`, pane, its[pane].At, allow))
	}
	for i, tc := range []struct {
		pane  string
		allow bool
		want  string
	}{
		{"a", true, "1"},     // Claude: option 1, Yes
		{"b", true, "y"},     // Codex: "Yes, proceed (y)"
		{"a", false, "\x1b"}, // both deny with Esc
		{"b", false, "\x1b"},
	} {
		p := f.pane(map[string]int{"a": 0, "b": 1}[tc.pane])
		before := len(p.got())
		if st, body := answer(tc.pane, tc.allow); st != 200 {
			t.Fatalf("case %d: %d %s", i, st, body)
		}
		if got := gotAfter(t, p, before, len(tc.want)); got != tc.want {
			t.Errorf("case %d: pane %s got %q, want %q", i, tc.pane, got, tc.want)
		}
	}
	// With the kitty keyboard protocol on, Esc goes as the GUI sends it.
	f.pane(0).mu.Lock()
	f.pane(0).modes = vt.Modes{KittyKeyboard: 1}
	f.pane(0).mu.Unlock()
	before := len(f.pane(0).got())
	answer("a", false)
	if got := gotAfter(t, f.pane(0), before, 7); got != "\x1b[27;1u" {
		t.Errorf("kitty Esc = %q", got)
	}

	// No keys for pi, for a screen without a prompt, or for a prompt the
	// phone did not show.
	if st, _ := answer("c", true); st != 409 || f.pane(2).got() != "" {
		t.Errorf("pi answered: %d %q", st, f.pane(2).got())
	}
	f.pane(1).mu.Lock()
	f.pane(1).screen = []string{"› ask anything"}
	f.pane(1).mu.Unlock()
	before = len(f.pane(1).got())
	if st, _ := answer("b", true); st != 409 || len(f.pane(1).got()) != before {
		t.Errorf("answered a screen without a prompt: %d", st)
	}
	time.Sleep(time.Millisecond)
	d.agentEvent(context.Background(), proto.AgentEvent{Pane: "a", Provider: model.ProviderClaude, Payload: []byte("working")})
	d.agentEvent(context.Background(), proto.AgentEvent{Pane: "a", Provider: model.ProviderClaude, Payload: []byte(model.StatePendingApproval)})
	before = len(f.pane(0).got())
	if st, _ := answer("a", true); st != 409 || len(f.pane(0).got()) != before {
		t.Errorf("answered a newer prompt than the phone showed: %d", st)
	}
}

func TestRemoteReply(t *testing.T) {
	d, f, do := remoteDaemon(t)
	time.Sleep(time.Millisecond)
	d.agentEvent(context.Background(), proto.AgentEvent{Pane: "a", Provider: model.ProviderClaude, Payload: []byte(model.StateAwaitingInput)})
	its, _ := items(t, do)
	if !its["a"].Reply || its["b"].Reply {
		t.Fatalf("items %+v", its)
	}
	reply := func(pane, text string) int {
		b, _ := json.Marshal(map[string]any{"pane": pane, "at": fmt.Sprint(its[pane].At), "text": text})
		st, _ := do("POST", "/api/reply", string(b))
		return st
	}
	evil := "yes\x1b[201~\x03rm -rf ~\r\nand more\x1b[200~"
	f.pane(0).mu.Lock()
	f.pane(0).modes = vt.Modes{BracketedPaste: true}
	f.pane(0).mu.Unlock()
	if st := reply("a", evil); st != 200 {
		t.Fatalf("reply: %d", st)
	}
	want := "\x1b[200~yes[201~rm -rf ~\nand more[200~\x1b[201~\r"
	if got := gotAfter(t, f.pane(0), 0, len(want)); got != want {
		t.Errorf("bracketed reply %q, want %q", got, want)
	}
	// Without bracketed paste a newline would submit early, so it is a space.
	f.pane(0).mu.Lock()
	f.pane(0).modes, f.pane(0).input = vt.Modes{}, nil
	f.pane(0).mu.Unlock()
	if st := reply("a", "one\ntwo"); st != 200 {
		t.Fatalf("reply: %d", st)
	}
	if got := gotAfter(t, f.pane(0), 0, 8); got != "one two\r" {
		t.Errorf("plain reply %q", got)
	}
	// An approval takes no typing.
	if st := reply("b", "hello"); st != 409 || f.pane(1).got() != "" {
		t.Errorf("reply into an approval: %d %q", st, f.pane(1).got())
	}
}

// A GUI's proto.Answer takes the phone's path: it presses the key only on
// the prompt whose UpdatedAt it names, and only while the screen shows it.
func TestGUIAnswer(t *testing.T) {
	d, f, _ := remoteDaemon(t)
	at := func(pane string) int64 {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.st.Activities[d.activityIndex(pane)].UpdatedAt.UnixNano()
	}
	ctx := context.Background()
	if err := d.handle(ctx, proto.Answer{Pane: "a", At: at("a"), Allow: true}); err != nil {
		t.Fatal(err)
	}
	if got := gotAfter(t, f.pane(0), 0, 1); got != "1" {
		t.Fatalf("allow sent %q", got)
	}
	if err := d.handle(ctx, proto.Answer{Pane: "b", At: at("b") - 1}); err == nil || f.pane(1).got() != "" {
		t.Fatalf("answered a stale prompt: %v %q", err, f.pane(1).got())
	}
	if err := d.handle(ctx, proto.Answer{Pane: "c", At: at("c"), Allow: true}); err == nil || f.pane(2).got() != "" {
		t.Fatalf("answered pi: %v %q", err, f.pane(2).got())
	}
	if err := d.handle(ctx, proto.Answer{Pane: "b", At: at("b")}); err != nil {
		t.Fatal(err)
	}
	if got := gotAfter(t, f.pane(1), 0, 1); got != "\x1b" {
		t.Fatalf("deny sent %q", got)
	}
}

// TestPushLoop: with no window open, the daemon pushes an approval that
// comes after it started, by the desktop's triggers, and nothing for the
// approvals it found waiting.
func TestPushLoop(t *testing.T) {
	d, _, do := remoteDaemon(t)
	got := make(chan *http.Request, 10)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	ua, _ := ecdh.P256().GenerateKey(nil)
	sub, _ := json.Marshal(remote.Subscription{Endpoint: srv.URL + "/p", Keys: remote.PushKeys{
		P256dh: base64.RawURLEncoding.EncodeToString(ua.PublicKey().Bytes()), Auth: base64.RawURLEncoding.EncodeToString(make([]byte, 16))}})
	if st, body := do("POST", "/api/push", string(sub)); st != 200 {
		t.Fatalf("subscribe: %d %s", st, body)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.pushLoop(ctx, &remote.Pusher{Dir: d.o.RemoteDir, Client: srv.Client()}); close(done) }()
	defer func() { cancel(); <-done }()
	for start := time.Now(); time.Since(start) < 2*time.Second; time.Sleep(time.Millisecond) {
		d.mu.Lock()
		running := d.pushWake != nil // its baseline is in
		d.mu.Unlock()
		if running {
			break
		}
	}
	d.mu.Lock()
	d.changed()
	d.mu.Unlock()
	select {
	case r := <-got:
		t.Fatalf("pushed the approvals waiting at start: %v", r.Header)
	case <-time.After(50 * time.Millisecond):
	}
	time.Sleep(time.Millisecond)
	d.agentEvent(ctx, proto.AgentEvent{Pane: "a", Provider: model.ProviderClaude, Payload: []byte("working")})
	time.Sleep(50 * time.Millisecond) // the loop sees working, as the desktop's would
	d.agentEvent(ctx, proto.AgentEvent{Pane: "a", Provider: model.ProviderClaude, Payload: []byte(model.StatePendingApproval)})
	select {
	case r := <-got:
		if r.Header.Get("Urgency") != "high" || !strings.HasPrefix(r.Header.Get("Authorization"), "vapid t=") {
			t.Fatalf("push headers %v", r.Header)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no push for a new approval")
	}
}
