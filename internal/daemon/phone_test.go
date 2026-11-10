package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/remote"
)

// phoneDaemon is a daemon with tab w1 running Claude Code in pane a at its
// input box, tab w2 a shell in pane sh in group infra, w3 detached, and the
// phone page on a paired device's token.
func phoneDaemon(t *testing.T) (*Daemon, *fakes, func(method, path, body string) (int, string)) {
	t.Helper()
	oldDelay, oldStill := settleDelay.set(20*time.Millisecond), stillFor.set(10*time.Millisecond)
	oldSettle, oldLook, oldReply := sendSettle.set(time.Millisecond), sendLook.set(10*time.Millisecond), replyDelay.set(time.Millisecond)
	oldSwitch := switchWait.set(300 * time.Millisecond)
	f := &fakes{statsCalls: map[string]int{}}
	ws := func(id, pane, project string, detached bool) model.Workspace {
		return model.Workspace{ID: id, SessionID: "s", Name: id, ProjectID: project, Detached: detached, Branch: "main",
			Tabs: []model.Tab{{ID: "t" + id, Layout: &layout.Node{Pane: pane}}}, ActiveTab: "t" + id}
	}
	f.saved = model.State{
		Sessions:   []model.Session{{ID: "s", Name: "work"}},
		Projects:   []model.Project{{ID: "g", SessionID: "s", Name: "infra", Kind: model.ProjectGroup}},
		Workspaces: []model.Workspace{ws("w1", "a", "", false), ws("w2", "sh", "g", false), ws("w3", "x", "", true)},
		Panes:      []model.Pane{{ID: "a", WorkspaceID: "w1"}, {ID: "sh", WorkspaceID: "w2"}, {ID: "x", WorkspaceID: "w3"}},
	}
	o := f.options()
	o.RemoteDir = t.TempDir()
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		time.Sleep(4 * settleDelay.get())
		d.mu.Lock()
		d.mu.Unlock()
		settleDelay.set(oldDelay)
		stillFor.set(oldStill)
		sendSettle.set(oldSettle)
		sendLook.set(oldLook)
		replyDelay.set(oldReply)
		switchWait.set(oldSwitch)
	})
	setScreen(f.pane(0), []string{"SECRET-SCROLLBACK token=abc", "> "})
	event(t, d, "a", "completed")
	return d, f, pairPhone(t, d)
}

func event(t *testing.T, d *Daemon, pane, state string) {
	t.Helper()
	time.Sleep(time.Millisecond) // a new UpdatedAt
	if err := d.agentEvent(context.Background(), proto.AgentEvent{Pane: pane, Provider: model.ProviderClaude, Payload: []byte(state)}); err != nil {
		t.Fatal(err)
	}
}

func TestPhoneTabs(t *testing.T) {
	d, _, _ := phoneDaemon(t)
	tabs := d.phoneTabs()
	if len(tabs) != 2 {
		t.Fatalf("tabs %+v", tabs)
	}
	a, sh := tabs[0], tabs[1]
	if a.Pane != "a" || a.Agent != "claude" || a.State != "completed" || !a.Send || !a.Picker || a.Session != "work" {
		t.Errorf("agent tab %+v", a)
	}
	if sh.Pane != "sh" || sh.Group != "infra" || sh.Send || sh.Picker || sh.State != "" {
		t.Errorf("shell tab %+v", sh)
	}
	b, _ := json.Marshal(tabs)
	if strings.Contains(string(b), "SECRET") {
		t.Errorf("tabs leak the screen: %s", b)
	}
}

func TestPhoneSend(t *testing.T) {
	d, f, do := phoneDaemon(t)
	send := func(pane, text string) int {
		b, _ := json.Marshal(map[string]string{"pane": pane, "text": text})
		st, _ := do("POST", "/api/send", string(b))
		return st
	}
	// At its input box: pasted with control characters gone, then Enter.
	if st := send("a", "hi\x1bthere"); st != 200 {
		t.Fatalf("send: %d", st)
	}
	if got := gotAfter(t, f.pane(0), 0, len("hithere\r")); got != "hithere\r" {
		t.Errorf("typed %q", got)
	}
	// A shell never gets it.
	if st := send("sh", "ls"); st != 409 || f.pane(1).got() != "" {
		t.Errorf("shell: %d %q", st, f.pane(1).got())
	}
	// Working: queued until the turn ends.
	event(t, d, "a", "working")
	n := len(f.pane(0).got())
	if st := send("a", "next"); st != 200 {
		t.Fatalf("send while working: %d", st)
	}
	time.Sleep(50 * time.Millisecond)
	if got := f.pane(0).got()[n:]; got != "" || d.phoneTabs()[0].Queued != 1 {
		t.Fatalf("typed into a running turn: %q, queued %d", got, d.phoneTabs()[0].Queued)
	}
	// Done, but a form on screen: still queued.
	setScreen(f.pane(0), []string{" Do you want to proceed?", " > 1. Yes", "   2. No", " Esc to cancel"})
	event(t, d, "a", "completed")
	time.Sleep(50 * time.Millisecond)
	if got := f.pane(0).got()[n:]; got != "" {
		t.Fatalf("typed into a form: %q", got)
	}
	// Back at the input box: typed.
	setScreen(f.pane(0), []string{"> "})
	if got := gotAfter(t, f.pane(0), n, len("next\r")); got != "next\r" {
		t.Errorf("queued message typed %q", got)
	}
	if st := send("a", " \x03 "); st != 409 {
		t.Errorf("empty message: %d", st)
	}
	// An agent that exited takes nothing.
	d.mu.Lock()
	d.st.Panes[0].Exited = true
	d.mu.Unlock()
	if st := send("a", "hello"); st != 409 || d.phoneTabs()[0].Send {
		t.Errorf("exited agent: %d", st)
	}
}

func TestPhoneCommand(t *testing.T) {
	d, f, do := phoneDaemon(t)
	cmd := func(what, value string) int {
		b, _ := json.Marshal(map[string]string{"pane": "a", "what": what, "value": value})
		st, _ := do("POST", "/api/command", string(b))
		return st
	}
	if st := cmd("model", "opus"); st != 200 {
		t.Fatalf("model: %d", st)
	}
	if got := gotAfter(t, f.pane(0), 0, len("/model opus\r")); got != "/model opus\r" {
		t.Errorf("typed %q", got)
	}
	// In a conversation Claude Code asks to confirm; the pick confirms it.
	n := len(f.pane(0).got())
	go func() {
		for !strings.HasSuffix(f.pane(0).got(), "/model sonnet\r") {
			time.Sleep(time.Millisecond)
		}
		setScreen(f.pane(0), []string{"   Switch model?", "   Your next response will be slower", "   ❯ 1. Yes, switch to Sonnet 5.5", "     2. No, go back"})
	}()
	if st := cmd("model", "sonnet"); st != 200 {
		t.Fatalf("model: %d", st)
	}
	if got := gotAfter(t, f.pane(0), n, len("/model sonnet\r1")); got != "/model sonnet\r1" {
		t.Errorf("confirm typed %q", got)
	}
	setScreen(f.pane(0), []string{"> "})
	n = len(f.pane(0).got())
	if st := cmd("effort", "xhigh"); st != 200 {
		t.Fatalf("effort: %d", st)
	}
	if got := gotAfter(t, f.pane(0), n, len("/effort xhigh\r")); got != "/effort xhigh\r" {
		t.Errorf("typed %q", got)
	}
	n = len(f.pane(0).got())
	for _, tc := range []struct{ what, value string }{{"model", "opus; rm"}, {"model", "x\r/exit"}, {"effort", "huge"}, {"clear", ""}} {
		if st := cmd(tc.what, tc.value); st != 400 {
			t.Errorf("%s %q: %d", tc.what, tc.value, st)
		}
	}
	event(t, d, "a", "working")
	if st := cmd("model", "sonnet"); st != 409 {
		t.Errorf("model during a turn: %d", st)
	}
	event(t, d, "a", "completed")
	setScreen(f.pane(0), []string{" Do you want to proceed?", " > 1. Yes", "   2. No", " Esc to cancel"})
	if st := cmd("model", "sonnet"); st != 409 {
		t.Errorf("model on a form: %d", st)
	}
	time.Sleep(20 * time.Millisecond)
	if got := f.pane(0).got()[n:]; got != "" {
		t.Errorf("typed %q", got)
	}
}

// Every new route wants a paired device and refuses another site's page.
func TestPhoneRoutesNeedAuth(t *testing.T) {
	d, _, _ := phoneDaemon(t)
	h := d.remoteServer().Handler()
	for _, rt := range []struct{ method, path string }{
		{"GET", "/api/stream"}, {"GET", "/api/chat?pane=a&before=9"}, {"POST", "/api/send"}, {"POST", "/api/command"},
	} {
		r := httptest.NewRequest(rt.method, rt.path, strings.NewReader(`{"pane":"a","text":"x","what":"model","value":"opus"}`))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Errorf("%s %s without a token: %d", rt.method, rt.path, w.Code)
		}
		r = httptest.NewRequest(rt.method, rt.path, nil)
		r.Header.Set("Origin", "https://evil.example.com")
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Errorf("%s %s from another origin: %d", rt.method, rt.path, w.Code)
		}
	}
}

// The stream sends the tab list, then a tab's transcript, and its new
// entries as the file grows.
func TestPhoneStream(t *testing.T) {
	d, _, _ := phoneDaemon(t)
	path := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(path, []byte(`{"type":"user","message":{"content":"first"}}`+"\n"), 0o600)
	d.mu.Lock()
	for i := range d.st.Panes {
		if d.st.Panes[i].ID == "a" {
			d.st.Panes[i].Transcript = path
		}
	}
	d.mu.Unlock()
	code, err := remote.Pair(d.o.RemoteDir, "tablet", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(d.remoteServer().Handler())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/api/pair", "application/json", strings.NewReader(`{"code":"`+code+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	var pr struct{ Token string }
	json.NewDecoder(resp.Body).Decode(&pr)
	resp.Body.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/stream?pane=a", nil)
	req.Header.Set("Authorization", "Bearer "+pr.Token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("stream: %v %v", err, resp)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	next := func() (string, string) {
		var ev string
		for sc.Scan() {
			line := sc.Text()
			if e, ok := strings.CutPrefix(line, "event: "); ok {
				ev = e
			} else if data, ok := strings.CutPrefix(line, "data: "); ok {
				return ev, data
			}
		}
		t.Fatalf("stream ended: %v", sc.Err())
		return "", ""
	}
	want := func(event, has string) {
		t.Helper()
		ev, data := next()
		if ev != event || !strings.Contains(data, has) || strings.Contains(data, "SECRET") {
			t.Fatalf("got %s %s, want %s with %q", ev, data, event, has)
		}
	}
	want("hello", `"models":["default"`)
	want("tabs", `"pane":"a"`)
	want("reset", `"text":"first"`)
	fh, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	fh.WriteString(`{"type":"assistant","message":{"content":[{"type":"text","text":"second"}]}}` + "\n")
	fh.Close()
	want("entries", `"text":"second"`)
	// Paging back reads the same log.
	oreq, _ := http.NewRequest("GET", srv.URL+"/api/chat?pane=a&before=2", nil)
	oreq.Header.Set("Authorization", "Bearer "+pr.Token)
	oresp, err := http.DefaultClient.Do(oreq)
	if err != nil {
		t.Fatal(err)
	}
	var older struct{ Entries []struct{ Text string } }
	json.NewDecoder(oresp.Body).Decode(&older)
	oresp.Body.Close()
	if len(older.Entries) != 1 || older.Entries[0].Text != "first" {
		t.Errorf("older: %+v", older)
	}
	// A change to the state sends the list again.
	event(t, d, "a", "working")
	want("tabs", `"state":"working"`)
	// Revoking the device ends its stream.
	if _, err := remote.Revoke(d.o.RemoteDir, "tablet"); err != nil {
		t.Fatal(err)
	}
	for sc.Scan() {
	}
	if ctx.Err() != nil {
		t.Error("the stream outlived its device")
	}
}
