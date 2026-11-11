package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/session"
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

func TestPhoneCurrentSettings(t *testing.T) {
	d, f, _ := phoneDaemon(t)
	setScreen(f.pane(0), []string{"────────", "❯ ", "────────", "Opus 5.5 · high · project · 8% 83k/1.0M"})
	tab := d.phoneTabs()[0]
	if tab.Model != "Opus 5.5 1M" || tab.Effort != "high" {
		t.Fatalf("current terminal settings: %+v", tab)
	}
	setScreen(f.pane(0), []string{"────────", "❯ ", "────────", "Sonnet 5.5 · low · project"})
	tab = d.phoneTabs()[0]
	if tab.Model != "Sonnet 5.5" || tab.Effort != "low" {
		t.Fatalf("external change: %+v", tab)
	}
	d.rememberClaudeSettings("a", f.pane(0), f.pane(0).Snapshot())
	setScreen(f.pane(0), []string{"────────", "❯ ", "────────"})
	tab = d.phoneTabs()[0]
	if tab.Model != "Sonnet 5.5" || tab.Effort != "low" {
		t.Fatalf("settings lost off screen: %+v", tab)
	}
}

func TestPhoneDelayedSubmissionReleasesDraft(t *testing.T) {
	d, f, do := phoneDaemon(t)
	d.o.Derive = agent.Derive
	p := f.pane(0)
	setScreen(p, []string{"────────", "❯ Try a test", "────────"})
	d.noteUserKey("a", []byte("\r"))
	d.mu.Lock()
	d.mail.returnAt["a"] = time.Now().Add(-5 * time.Second)
	d.mu.Unlock()
	claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"write a function"}`)
	claudeHook(t, d, "a", `{"hook_event_name":"Stop","last_assistant_message":"done"}`)
	if st, body := do("POST", "/api/command", `{"pane":"a","what":"effort","value":"high"}`); st != 200 {
		t.Fatalf("empty box refused: %d %s", st, body)
	}
	gotAfter(t, p, 0, len("/effort high\r"))
	d.mu.Lock()
	d.mail.keyAt["a"] = time.Now().Add(-5 * time.Second)
	d.mu.Unlock()
	n := len(p.got())
	if st, body := do("POST", "/api/send", `{"pane":"a","text":"next"}`); st != 200 {
		t.Fatalf("send: %d %s", st, body)
	}
	if got := gotAfter(t, p, n, len("next\r")); got != "next\r" {
		t.Fatalf("queue did not drain: %q", got)
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

// A phone message queued for an agent that then exits is dropped, never
// typed into the shell left behind, and its tab lists it as not sent.
func TestPhoneMessageDroppedWhenAgentExits(t *testing.T) {
	oldSettle, oldLook, oldReply := sendSettle.set(time.Millisecond), sendLook.set(10*time.Millisecond), replyDelay.set(time.Millisecond)
	t.Cleanup(func() { sendSettle.set(oldSettle); sendLook.set(oldLook); replyDelay.set(oldReply) })
	d, lp, id := liveDaemon(t, model.StateWorking)
	msg, err := d.phoneSend(id, "echo PHONE-MESSAGE")
	if err != nil || msg == 0 {
		t.Fatalf("send: %d %v", msg, err)
	}
	// The agent exits: its shell takes the foreground and shows a prompt.
	lp.show("$ ", false)
	lp.fgGroup.Store(50)
	waitUntil(t, "the message dropped", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return len(d.mail.queue[id]) == 0 && slices.Contains(d.phoneDropped[id], msg)
	})
	time.Sleep(50 * time.Millisecond)
	if got := lp.got(); strings.Contains(got, "PHONE-MESSAGE") {
		t.Errorf("the shell got the phone's message: %q", got)
	}
}

// Two quick picks take turns: each command goes in whole, with its Enter.
func TestPhoneCommandsTakeTurns(t *testing.T) {
	_, f, do := phoneDaemon(t)
	old := replyDelay.set(30 * time.Millisecond)
	t.Cleanup(func() { replyDelay.set(old) })
	var wg sync.WaitGroup
	for _, v := range []string{"low", "high"} {
		wg.Go(func() {
			if st, body := do("POST", "/api/command", `{"pane":"a","what":"effort","value":"`+v+`"}`); st != 200 {
				t.Errorf("effort %s: %d %s", v, st, body)
			}
		})
	}
	wg.Wait()
	got := gotAfter(t, f.pane(0), 0, len("/effort low\r/effort high\r"))
	if got != "/effort low\r/effort high\r" && got != "/effort high\r/effort low\r" {
		t.Errorf("typed %q", got)
	}
}

// A chat restored after a restart shows its saved transcript on the phone
// before any other client reads it.
func TestPhoneRestoredChat(t *testing.T) {
	root := t.TempDir()
	f := &fakes{statsCalls: map[string]int{}}
	spy := &spyProvider{}
	opts := func(o *Options) {
		o.Chats = root
		o.Providers = map[model.Provider]session.Provider{model.ProviderClaude: spy}
	}
	hello := proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "gui", Cwd: t.TempDir()}
	sock, stop := runWith(t, f, opts)
	gui := dialHello(t, sock, hello)
	r := chatCall[proto.ChatResult](gui, proto.ChatNew{Provider: model.ProviderClaude, Dir: t.TempDir(), Prompt: "hello"})
	if r.Code != "" {
		t.Fatalf("new: %+v", r)
	}
	waitChat(gui, "first turn", func(s model.State) bool { c := chatOf(s); return c.Turns == 1 && c.State == session.StateReady })
	stop()
	d, sock, stop := runDaemon(t, f, opts)
	t.Cleanup(stop)
	waitChat(dialHello(t, sock, hello), "chat back", func(s model.State) bool { return chatOf(s).ID == r.Chat })
	es, _, ok := d.phoneChatEntries(r.Chat)
	if !ok || !slices.ContainsFunc(es, func(e flow.Entry) bool { return e.Kind == flow.EntryUser && e.Text == "hello" }) {
		t.Errorf("restored chat on the phone: %v %+v", ok, es)
	}
}

// A phone message waits while the agent's tool holds the foreground: the
// agent has not left, so the message is neither typed nor dropped.
func TestPhoneMessageWaitsForTool(t *testing.T) {
	oldSettle, oldLook := sendSettle.set(time.Millisecond), sendLook.set(10*time.Millisecond)
	t.Cleanup(func() { sendSettle.set(oldSettle); sendLook.set(oldLook) })
	d, lp, id := openLive(t, 400)
	useReportHooks(d)
	report(t, d, id, agent.Report{Name: "My agent", State: "working", ResumeCmd: []string{"my-agent"}})
	d.sendMu.Lock()
	msg, err := d.phoneSend(id, "keep this message")
	lp.fgGroup.Store(500) // the agent's tool takes the foreground
	d.sendMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond) // many delivery attempts
	d.mu.Lock()
	n, dropped := len(d.mail.queue[id]), slices.Contains(d.phoneDropped[id], msg)
	d.mu.Unlock()
	if n != 1 || dropped || strings.Contains(lp.got(), "keep this") {
		t.Fatalf("during the tool: queue %d, dropped %v, typed %q", n, dropped, lp.got())
	}
}

// A /model waiting for Claude Code's confirm holds up only its own pane:
// another agent's queued message goes in meanwhile.
func TestPhoneModelDoesNotBlockOtherPane(t *testing.T) {
	d, f, _ := phoneDaemon(t)
	old := switchWait.set(1500 * time.Millisecond)
	t.Cleanup(func() { switchWait.set(old) })
	event(t, d, "sh", "completed")
	setScreen(f.pane(1), []string{"> "})
	done := make(chan error, 1)
	go func() { done <- d.phoneCommand("a", "/model opus") }()
	gotAfter(t, f.pane(0), 0, len("/model opus\r"))
	if _, err := d.phoneSend("sh", "OTHER"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(350 * time.Millisecond)
	got := f.pane(1).got()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got != "OTHER\r" {
		t.Errorf("the other pane got %q while /model waited for its confirm", got)
	}
}

// A phone message to a parked agent resumes it, as pitwall send does, and
// goes in once the agent is back: parking is not the agent leaving.
func TestPhoneMessageToParkedAgent(t *testing.T) {
	d, f := parkDaemon(t)
	park(t, d, f)
	n := len(f.panes)
	msg, err := d.phoneSend("a", "hello from the phone")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.panes) != n+1 {
		t.Fatalf("panes %d, want the agent resumed", len(f.panes))
	}
	d.checkWaking()
	p := back(t, d, f)
	if got := gotAfter(t, p, 0, len("hello from the phone")); !strings.Contains(got, "hello from the phone") {
		t.Fatalf("typed %q, want the message", got)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if slices.Contains(d.phoneDropped["a"], msg) {
		t.Error("the parked agent's message was dropped")
	}
}
