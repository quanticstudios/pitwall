package daemon

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/fake"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// remoteDaemon is a daemon with one tab of panes a (Claude), b (Codex)
// and c (pi), each asking permission, and the phone page on a paired
// device's token.
func remoteDaemon(t *testing.T) (*Daemon, *fakes, func(method, path, body string) (int, string)) {
	t.Helper()
	oldDelay, oldStill := settleDelay.set(20*time.Millisecond), stillFor.set(10*time.Millisecond)
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
		time.Sleep(4 * settleDelay.get())
		d.mu.Lock()
		d.mu.Unlock()
		settleDelay.set(oldDelay)
		stillFor.set(oldStill)
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
	return d, f, pairPhone(t, d)
}

// pairPhone pairs a device called pixel with d's phone page and returns
// what calls the page with its token.
func pairPhone(t *testing.T, d *Daemon) func(method, path, body string) (int, string) {
	t.Helper()
	code, err := remote.Pair(d.o.RemoteDir, "pixel", time.Now())
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
	return do
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
	f.pane(0).mu.Lock()
	f.pane(0).screen = []string{"> "}
	f.pane(0).mu.Unlock()
	evil := "yes\x1b[201~\x03rm -rf ~\r\nand more\x1b[200~"
	f.pane(0).mu.Lock()
	f.pane(0).modes = vt.Modes{BracketedPaste: true}
	f.pane(0).mu.Unlock()
	if st := reply("a", evil); st != 200 {
		t.Fatalf("reply: %d", st)
	}
	if reportedPane(d, "a").LastPromptAt.IsZero() {
		t.Fatal("phone reply did not set prompt destination")
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
	if !reportedPane(d, "b").LastPromptAt.IsZero() {
		t.Fatal("permission status set prompt destination")
	}
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

// setForm shows lines on pane i and gives its activity a's fields, as if a
// hook had reported them.
func setForm(t *testing.T, d *Daemon, f *fakes, i int, pane string, a model.Activity, lines ...string) int64 {
	t.Helper()
	p := f.pane(i)
	p.mu.Lock()
	p.screen, p.input = lines, nil
	p.mu.Unlock()
	d.mu.Lock()
	defer d.mu.Unlock()
	cur := &d.st.Activities[d.activityIndex(pane)]
	cur.State, cur.Detail, cur.Options = a.State, a.Detail, a.Options
	return cur.UpdatedAt.UnixNano()
}

var questionForm = []string{"Which fruit do you prefer?", "> 1. apple", "  2. banana", "  3. Type something.", "Enter to select · ↑/↓ to navigate · Esc to cancel"}

// A row's button presses the key the form on screen gives its label, and
// only while that form is the one the activity is about.
func TestPressOffer(t *testing.T) {
	d, f, _ := remoteDaemon(t)
	ctx := context.Background()
	q := model.Activity{State: model.StateAwaitingInput, Detail: "Which fruit do you prefer?", Options: []string{"apple", "banana"}}
	at := setForm(t, d, f, 0, "a", q, questionForm...)
	press := func(pane, label string, at int64) error {
		return d.handle(ctx, proto.Press{Pane: pane, At: at, Label: label})
	}
	if err := press("a", "banana", at); err != nil {
		t.Fatal(err)
	}
	if got := gotAfter(t, f.pane(0), 0, 1); got != "2" {
		t.Fatalf("banana sent %q", got)
	}

	// The same options under a newer question, and a screen that moved on.
	for name, lines := range map[string][]string{
		"another question": {"Which color?", "> 1. apple", "  2. banana", "Enter to select · Esc to cancel"},
		"other options":    {"Which fruit do you prefer?", "> 1. pear", "  2. banana", "Enter to select · Esc to cancel"},
		"cursor in field":  {"Which fruit do you prefer?", "  1. apple", "  2. banana", "> 3. Type something.", "Enter to select · Esc to cancel"},
		"a prompt":         {"> "},
		"a permission":     {"Do you want to proceed?", "> 1. Yes", "  2. No", "Esc to cancel"},
	} {
		at := setForm(t, d, f, 0, "a", q, lines...)
		if err := press("a", "apple", at); err == nil || f.pane(0).got() != "" {
			t.Errorf("%s: pressed: %v %q", name, err, f.pane(0).got())
		}
	}
	at = setForm(t, d, f, 0, "a", q, questionForm...)
	if err := press("a", "apple", at-1); err == nil || f.pane(0).got() != "" {
		t.Errorf("stale At: %v %q", err, f.pane(0).got())
	}
	if err := press("a", "cherry", at); err == nil || f.pane(0).got() != "" {
		t.Errorf("label not offered: %v %q", err, f.pane(0).got())
	}

	// Codex's Allow always is its letter, not its number.
	at = setForm(t, d, f, 1, "b", model.Activity{State: model.StatePendingApproval, Detail: "x"},
		"Would you like to run the following command?", "› 1. Yes, proceed (y)", "  2. Yes, and don't ask again for this command (a)", "  3. No, and tell Codex what to do differently (esc)", "Press enter to confirm or esc to cancel")
	if err := press("b", agent.AllowAlways, at); err != nil {
		t.Fatal(err)
	}
	if got := gotAfter(t, f.pane(1), 0, 1); got != "a" {
		t.Fatalf("always sent %q", got)
	}

	// pi has no keys pitwall knows.
	at = setForm(t, d, f, 2, "c", q, questionForm...)
	if err := press("c", "apple", at); err == nil || f.pane(2).got() != "" {
		t.Errorf("pi: %v %q", err, f.pane(2).got())
	}
}

// Allow on a screen that shows a plan or question instead of the permission
// prompt its row showed would pick that form's first option.
func TestAllowNeedsPermissionForm(t *testing.T) {
	d, f, _ := remoteDaemon(t)
	at := setForm(t, d, f, 0, "a", model.Activity{State: model.StatePendingApproval}, "Would you like to proceed?", "> 1. Yes, and use auto mode", "  2. No, keep planning")
	if err := d.remoteAnswer("a", at, true); err == nil || f.pane(0).got() != "" {
		t.Errorf("allowed a plan: %v %q", err, f.pane(0).got())
	}
	at = setForm(t, d, f, 0, "a", model.Activity{State: model.StatePendingApproval}, questionForm...)
	if err := d.remoteAnswer("a", at, true); err == nil || f.pane(0).got() != "" {
		t.Errorf("allowed a question: %v %q", err, f.pane(0).got())
	}
}

// The prompt screen for a command, rows being what the rules around it hold.
func commandForm(rows ...string) []string {
	form := append([]string{" Bash command", " Create a directory", " ╌╌╌╌╌╌"}, rows...)
	return append(form, " ╌╌╌╌╌╌", " Do you want to proceed?", " ❯ 1. Yes", "   2. Yes, and don't ask again for mkdir", "   3. No", "", " Esc to cancel · Tab to amend")
}

// Allow, Deny and Always land only on the command the hook reported, which an
// agent's next prompt can replace before its own hook arrives. Whichever way
// the answer is sent, it goes through remoteAnswer or pressOffer.
func TestAnswerNeedsTheCommand(t *testing.T) {
	d, f, _ := remoteDaemon(t)
	d.o.Derive = agent.Derive
	ctx := context.Background()
	report := func(payload string) int64 {
		t.Helper()
		if err := d.agentEvent(ctx, proto.AgentEvent{Pane: "a", Provider: model.ProviderClaude, Payload: []byte(payload)}); err != nil {
			t.Fatal(err)
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.st.Activities[d.activityIndex("a")].UpdatedAt.UnixNano()
	}
	show := func(rows ...string) {
		f.pane(0).mu.Lock()
		f.pane(0).screen, f.pane(0).input = commandForm(rows...), nil
		f.pane(0).mu.Unlock()
	}
	const asks = `{"hook_event_name":"PermissionRequest","session_id":"s","tool_name":"Bash","tool_input":{"command":"go test -race -timeout 5m ./internal/daemon ./internal/agent ./internal/remote ./internal/ui/sidebar","description":"Run the tests"}}`
	report(`{"hook_event_name":"PreToolUse","session_id":"s","tool_name":"Bash"}`)
	at := report(asks)

	show(" go test -race -timeout 5m ./internal/daemon ./internal/agent ./internal/secret")
	for name, answer := range map[string]func() error{
		"allow":  func() error { return d.handle(ctx, proto.Answer{Pane: "a", At: at, Allow: true}) },
		"deny":   func() error { return d.handle(ctx, proto.Answer{Pane: "a", At: at}) },
		"always": func() error { return d.handle(ctx, proto.Press{Pane: "a", At: at, Label: agent.AllowAlways}) },
	} {
		if err := answer(); err == nil || !strings.Contains(err.Error(), "answer it in its tab") || f.pane(0).got() != "" {
			t.Errorf("%s on a changed command: %v %q", name, err, f.pane(0).got())
		}
	}

	show(" go test -race -timeout 5m ./internal/daemon ./internal/agent ./internal/remote", " ./internal/ui/sidebar")
	if err := d.handle(ctx, proto.Answer{Pane: "a", At: at, Allow: true}); err != nil {
		t.Fatalf("allow on the same command, wrapped: %v", err)
	}
	if got := gotAfter(t, f.pane(0), 0, 1); got != "1" {
		t.Fatalf("allow sent %q", got)
	}

	// The Notification that follows the request is the same prompt.
	at = report(`{"hook_event_name":"Notification","session_id":"s","notification_type":"permission_prompt","message":"Claude needs your permission to use Bash"}`)
	show(" rm -rf ~")
	if err := d.handle(ctx, proto.Answer{Pane: "a", At: at, Allow: true}); err == nil || f.pane(0).got() != "" {
		t.Errorf("allow after the notification: %v %q", err, f.pane(0).got())
	}

	// With no command reported, what stands is today's check of the prompt.
	report(`{"hook_event_name":"PostToolUse","session_id":"s","tool_name":"Bash"}`)
	at = report(`{"hook_event_name":"PermissionRequest","session_id":"s","tool_name":"Edit","tool_input":{"file_path":"a.go"}}`)
	show(" anything")
	if err := d.handle(ctx, proto.Answer{Pane: "a", At: at, Allow: true}); err != nil {
		t.Fatalf("allow with no command to compare: %v", err)
	}
	if got := gotAfter(t, f.pane(0), 0, 1); got != "1" {
		t.Fatalf("allow sent %q", got)
	}
}

// The sidebar and phone match complete multi-line commands and reject stale rows.
func TestAnswerMultilineCommand(t *testing.T) {
	for _, provider := range []model.Provider{model.ProviderClaude, model.ProviderCodex} {
		t.Run(string(provider), func(t *testing.T) {
			d, f, phone := remoteDaemon(t)
			d.o.Derive = agent.Derive
			ctx := context.Background()
			command := "printf first\n\tprintf second\n\n    printf third"
			if provider == model.ProviderCodex {
				command = strings.ReplaceAll(command, "\t", "        ")
			}
			payload, err := json.Marshal(map[string]any{"hook_event_name": "PermissionRequest", "session_id": "s", "tool_name": "Bash", "tool_input": map[string]string{"command": command}})
			if err != nil {
				t.Fatal(err)
			}
			if err := d.agentEvent(ctx, proto.AgentEvent{Pane: "a", Provider: provider, Payload: payload}); err != nil {
				t.Fatal(err)
			}
			d.mu.Lock()
			at := d.st.Activities[d.activityIndex("a")].UpdatedAt.UnixNano()
			d.mu.Unlock()
			show := func(second string) {
				rows := commandForm(" │ printf first", " │         printf   "+second, " │", " │     printf third")
				if provider == model.ProviderCodex {
					rows = []string{"Would you like to run the following command?", "  $ printf first", "          printf   " + second, "", "      printf third", "", "› 1. Yes, proceed (y)", "  2. Yes, and don't ask again for this command (a)", "  3. No, and tell Codex what to do differently (esc)", "Press enter to confirm or esc to cancel"}
				}
				f.pane(0).mu.Lock()
				f.pane(0).screen = rows
				f.pane(0).mu.Unlock()
			}
			always := "2"
			allow := "1"
			if provider == model.ProviderCodex {
				always, allow = "a", "y"
			}
			for _, answer := range []struct {
				name, want string
				run        func() error
			}{
				{"sidebar", allow, func() error { return d.handle(ctx, proto.Answer{Pane: "a", At: at, Allow: true}) }},
				{"deny", "\x1b", func() error { return d.handle(ctx, proto.Answer{Pane: "a", At: at}) }},
				{"always", always, func() error { return d.handle(ctx, proto.Press{Pane: "a", At: at, Label: agent.AllowAlways}) }},
				{"phone", allow, func() error {
					status, body := phone("POST", "/api/answer", fmt.Sprintf(`{"pane":"a","at":"%d","allow":true}`, at))
					if status != http.StatusOK {
						return fmt.Errorf("HTTP %d: %s", status, body)
					}
					return nil
				}},
			} {
				t.Run(answer.name, func(t *testing.T) {
					show("second")
					before := len(f.pane(0).got())
					if err := answer.run(); err != nil {
						t.Fatal(err)
					}
					if got := gotAfter(t, f.pane(0), before, len(answer.want)); got != answer.want {
						t.Fatalf("sent %q, want %q", got, answer.want)
					}
					show("different")
					before = len(f.pane(0).got())
					if err := answer.run(); err == nil || len(f.pane(0).got()) != before {
						t.Fatalf("answered a different command with the same first line: %v", err)
					}
				})
			}
		})
	}
}

// Missing command text is refused through the whole answer path.
func TestAnswerRefusesWhatItCannotCompare(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		rows          []string
		scrolled      bool // the upper rule is off screen
	}{
		{"only the count row", "echo safe", []string{" +3 lines, +2 above (ctrl+o to see more)"}, true},
		{"a tab", "mkdir '/tmp/a\tb'", []string{" mkdir '/tmp/ab'"}, false},
		{"a newline off screen", "rm -rf /tmp/victim\necho safe", []string{" echo safe"}, true},
		{"scrolled past the title, with several blocks", "echo safe", []string{" rm -rf /tmp/wrong", " ╌╌╌╌╌╌", " echo safe"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, f, _ := remoteDaemon(t)
			d.o.Derive = agent.Derive
			ctx := context.Background()
			payload, err := json.Marshal(map[string]any{"hook_event_name": "PermissionRequest", "session_id": "s", "tool_name": "Bash", "tool_input": map[string]string{"command": tc.command, "description": "Run"}})
			if err != nil {
				t.Fatal(err)
			}
			if err := d.agentEvent(ctx, proto.AgentEvent{Pane: "a", Provider: model.ProviderClaude, Payload: payload}); err != nil {
				t.Fatal(err)
			}
			d.mu.Lock()
			at := d.st.Activities[d.activityIndex("a")].UpdatedAt.UnixNano()
			d.mu.Unlock()
			f.pane(0).mu.Lock()
			f.pane(0).screen, f.pane(0).input = commandForm(tc.rows...), nil
			if tc.scrolled {
				f.pane(0).screen = f.pane(0).screen[3:]
			}
			f.pane(0).mu.Unlock()
			for name, answer := range map[string]func() error{
				"allow":  func() error { return d.handle(ctx, proto.Answer{Pane: "a", At: at, Allow: true}) },
				"deny":   func() error { return d.handle(ctx, proto.Answer{Pane: "a", At: at}) },
				"always": func() error { return d.handle(ctx, proto.Press{Pane: "a", At: at, Label: agent.AllowAlways}) },
			} {
				if err := answer(); err == nil || !strings.Contains(err.Error(), "answer it in its tab") || f.pane(0).got() != "" {
					t.Errorf("%s: %v %q", name, err, f.pane(0).got())
				}
			}
		})
	}
}

// A hook that changes the command moves the row on, even when Derive sees no
// change in it, so an answer to the old command cannot land on the new one.
func TestNewCommandMovesTheRow(t *testing.T) {
	d, f, _ := remoteDaemon(t)
	d.o.Derive = agent.Derive
	ctx := context.Background()
	asks := func(command string) int64 {
		t.Helper()
		payload := fmt.Sprintf(`{"hook_event_name":"PermissionRequest","session_id":"s","tool_name":"Bash","tool_input":{"command":%q,"description":"Run command"}}`, command)
		if err := d.agentEvent(ctx, proto.AgentEvent{Pane: "a", Provider: model.ProviderClaude, Payload: []byte(payload)}); err != nil {
			t.Fatal(err)
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.st.Activities[d.activityIndex("a")].UpdatedAt.UnixNano()
	}
	first := asks("echo safe")
	if again := asks("echo safe"); again != first {
		t.Errorf("the same command moved the row: %d, was %d", again, first)
	}
	second := asks("rm -rf /tmp/target")
	if second <= first {
		t.Fatal("a new command kept the row's timestamp")
	}
	f.pane(0).mu.Lock()
	f.pane(0).screen, f.pane(0).input = commandForm(" rm -rf /tmp/target"), nil
	f.pane(0).mu.Unlock()
	if err := d.handle(ctx, proto.Answer{Pane: "a", At: first, Allow: true}); err == nil || f.pane(0).got() != "" {
		t.Fatalf("an answer to the old command: %v %q", err, f.pane(0).got())
	}
	if err := d.handle(ctx, proto.Answer{Pane: "a", At: second, Allow: true}); err != nil {
		t.Fatal(err)
	}
	if got := gotAfter(t, f.pane(0), 0, 1); got != "1" {
		t.Fatalf("allow sent %q", got)
	}
}

// A window shows no request's error, so a refused answer shows as its notice
// until one lands. The phone's page reads the error, and its state stays as it is.
func TestRefusedAnswerShowsANotice(t *testing.T) {
	d, f, _ := remoteDaemon(t)
	d.o.Derive = agent.Derive
	ctx := context.Background()
	payload := `{"hook_event_name":"PermissionRequest","session_id":"s","tool_name":"Bash","tool_input":{"command":"echo safe","description":"Run"}}`
	if err := d.agentEvent(ctx, proto.AgentEvent{Pane: "a", Provider: model.ProviderClaude, Payload: []byte(payload)}); err != nil {
		t.Fatal(err)
	}
	notice := func() string {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.st.Notice
	}
	d.mu.Lock()
	at := d.st.Activities[d.activityIndex("a")].UpdatedAt.UnixNano()
	d.mu.Unlock()
	show := func(row string) {
		f.pane(0).mu.Lock()
		f.pane(0).screen, f.pane(0).input = commandForm(row), nil
		f.pane(0).mu.Unlock()
	}
	show(" rm -rf ~")
	if err := d.remoteAnswer("a", at, true); err == nil || notice() != "" {
		t.Errorf("the phone's path: %v, notice %q", err, notice())
	}
	for name, refused := range map[string]any{
		"answer": proto.Answer{Pane: "a", At: at, Allow: true},
		"press":  proto.Press{Pane: "a", At: at, Label: agent.AllowAlways},
	} {
		d.mu.Lock()
		d.st.Notice = ""
		d.mu.Unlock()
		if err := d.handle(ctx, refused); err == nil || !strings.Contains(notice(), "answer it in its tab") {
			t.Errorf("%s: %v, notice %q", name, err, notice())
		}
	}
	show(" echo safe")
	if err := d.handle(ctx, proto.Answer{Pane: "a", At: at, Allow: true}); err != nil || notice() != "" {
		t.Errorf("an answer that lands: %v, notice %q", err, notice())
	}
}

// A coarse clock gives two reports one time, and UpdatedAt must still tell their
// prompts apart. A clock set back does the same on every platform.
func TestReportMovesTheTimestampForward(t *testing.T) {
	d, _, _ := remoteDaemon(t)
	d.o.Derive = agent.Derive
	d.mu.Lock()
	i := d.activityIndex("a")
	d.st.Activities[i].UpdatedAt = time.Now().Add(time.Hour)
	before := d.st.Activities[i].UpdatedAt
	d.mu.Unlock()
	payload := `{"hook_event_name":"PermissionRequest","session_id":"s","tool_name":"Bash","tool_input":{"command":"echo safe","description":"Run"}}`
	if err := d.agentEvent(context.Background(), proto.AgentEvent{Pane: "a", Provider: model.ProviderClaude, Payload: []byte(payload)}); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if got := d.st.Activities[d.activityIndex("a")].UpdatedAt; !got.After(before) {
		t.Errorf("UpdatedAt %v, was %v", got, before)
	}
}

// A command belongs to its activity: a later agent in the same pane, which
// reports none, is not held to it.
func TestAskedEndsWithItsActivity(t *testing.T) {
	const asks = `{"hook_event_name":"PermissionRequest","session_id":"s","tool_name":"Bash","tool_input":{"command":"mkdir /tmp/old","description":"Create"}}`
	const notifies = `{"hook_event_name":"Notification","session_id":"t","notification_type":"permission_prompt","message":"Permission needed"}`
	for name, end := range map[string]func(d *Daemon){
		"dropped": func(d *Daemon) { d.dropActivity("a") },
		"replaced": func(d *Daemon) {
			i := d.activityIndex("a")
			d.st.Activities[i].UpdatedAt = d.st.Activities[i].UpdatedAt.Add(time.Second)
		},
		"working in the same clock tick": func(d *Daemon) {
			d.st.Activities[d.activityIndex("a")].State = model.StateWorking
		},
	} {
		t.Run(name, func(t *testing.T) {
			d, f, _ := remoteDaemon(t)
			d.o.Derive = agent.Derive
			ctx := context.Background()
			if err := d.agentEvent(ctx, proto.AgentEvent{Pane: "a", Provider: model.ProviderClaude, Payload: []byte(asks)}); err != nil {
				t.Fatal(err)
			}
			d.mu.Lock()
			end(d)
			d.mu.Unlock()
			if err := d.agentEvent(ctx, proto.AgentEvent{Pane: "a", Provider: model.ProviderClaude, Payload: []byte(notifies)}); err != nil {
				t.Fatal(err)
			}
			d.mu.Lock()
			at := d.st.Activities[d.activityIndex("a")].UpdatedAt.UnixNano()
			d.mu.Unlock()
			f.pane(0).mu.Lock()
			f.pane(0).screen, f.pane(0).input = commandForm(" npm test"), nil
			f.pane(0).mu.Unlock()
			if err := d.handle(ctx, proto.Answer{Pane: "a", At: at, Allow: true}); err != nil {
				t.Fatalf("held to the old agent's command: %v", err)
			}
			if got := gotAfter(t, f.pane(0), 0, 1); got != "1" {
				t.Fatalf("allow sent %q", got)
			}
		})
	}
}

// A phone's reply on a menu is refused: its Enter would pick the highlighted
// option, and what was typed would be lost.
func TestReplyRefusesMenu(t *testing.T) {
	d, f, _ := remoteDaemon(t)
	at := setForm(t, d, f, 0, "a", model.Activity{State: model.StateAwaitingInput, Detail: "Which fruit?"}, questionForm...)
	if err := d.remoteReply("a", at, "plum"); err == nil || f.pane(0).got() != "" {
		t.Fatalf("typed into a menu: %v %q", err, f.pane(0).got())
	}
	// A free-text form takes it, and so does a finished turn.
	at = setForm(t, d, f, 0, "a", model.Activity{State: model.StateAwaitingInput, Detail: "Your name?"}, "Claude requests your input", "Your name: ", "Enter to submit")
	if err := d.remoteReply("a", at, "plum"); err != nil {
		t.Fatal(err)
	}
	if got := gotAfter(t, f.pane(0), 0, 5); got != "plum\r" {
		t.Errorf("reply %q", got)
	}
}

// checkForms puts on the activity what the screen offers, keeps its
// UpdatedAt, and takes it back when the form goes.
func TestCheckForms(t *testing.T) {
	d, f, _ := remoteDaemon(t)
	q := model.Activity{State: model.StateAwaitingInput, Detail: "Which fruit do you prefer?", Options: []string{"apple", "banana"}}
	at := setForm(t, d, f, 0, "a", q, questionForm...)
	read := func() model.Activity {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.st.Activities[d.activityIndex("a")]
	}
	d.checkForms()
	if a := read(); !slices.Equal(a.Buttons, q.Options) || !a.Menu || a.UpdatedAt.UnixNano() != at {
		t.Fatalf("with the form: %+v", a)
	}
	// A hook's announcement holds Menu until its form could be drawn; then the
	// screen decides, and the buttons follow it from the start.
	setForm(t, d, f, 0, "a", q, "> ")
	d.checkForms()
	if a := read(); len(a.Buttons) != 0 || !a.Menu {
		t.Fatalf("form not drawn yet: %+v", a)
	}
	d.mu.Lock()
	d.st.Activities[d.activityIndex("a")].UpdatedAt = time.Now().Add(-formDraw)
	d.mu.Unlock()
	d.checkForms()
	if a := read(); a.Menu {
		t.Fatalf("form never came: %+v", a)
	}
	setForm(t, d, f, 0, "a", q, questionForm...)
	d.checkForms()
	if a := read(); !a.Menu {
		t.Fatalf("form drawn: %+v", a)
	}
	d.mu.Lock()
	d.st.Activities[d.activityIndex("a")].State = model.StateWorking
	d.mu.Unlock()
	d.checkForms()
	if a := read(); a.Menu {
		t.Fatalf("no longer asking: %+v", a)
	}
}

// A menu drawn between a reply's paste and its Enter does not get the Enter.
func TestReplyHoldsEnterForMenu(t *testing.T) {
	d, f, _ := remoteDaemon(t)
	at := setForm(t, d, f, 0, "a", model.Activity{State: model.StateAwaitingInput, Detail: "Your name?"}, "Claude requests your input", "Your name: ")
	defer replyDelay.set(replyDelay.set(200 * time.Millisecond))
	p := f.pane(0)
	p.mu.Lock()
	p.wrote = func() {
		p.screen = []string{"Which database?", "> 1. Redis", "  2. SQLite", "Press enter to confirm or esc to cancel"}
	}
	p.mu.Unlock()
	if err := d.remoteReply("a", at, "plum"); err == nil {
		t.Error("no error for the held Enter")
	}
	if got := p.got(); got != "plum" {
		t.Errorf("sent %q", got)
	}
}

// A stale state does not let a reply into a form on screen.
func TestReplyRefusesFormWhateverTheState(t *testing.T) {
	d, f, _ := remoteDaemon(t)
	at := setForm(t, d, f, 0, "a", model.Activity{State: model.StateCompleted}, questionForm...)
	if err := d.remoteReply("a", at, "plum"); err == nil || f.pane(0).got() != "" {
		t.Errorf("typed into a form: %v %q", err, f.pane(0).got())
	}
}

// A footer quoted in output is not a form, so Enter goes.
func TestReplyEnterIgnoresOldHint(t *testing.T) {
	d, f, _ := remoteDaemon(t)
	at := setForm(t, d, f, 0, "a", model.Activity{State: model.StateCompleted}, "The menu says Enter to select", "> ")
	if err := d.remoteReply("a", at, "plum"); err != nil {
		t.Fatal(err)
	}
	if got := gotAfter(t, f.pane(0), 0, 5); got != "plum\r" {
		t.Errorf("sent %q", got)
	}
}

// A hook that announced a menu holds a reply before the menu is on screen.
func TestReplyRefusesAnnouncedMenu(t *testing.T) {
	d, f, _ := remoteDaemon(t)
	at := setForm(t, d, f, 0, "a", model.Activity{State: model.StateAwaitingInput, Detail: "Which?"}, "> ")
	d.mu.Lock()
	d.st.Activities[d.activityIndex("a")].Menu = true
	d.mu.Unlock()
	if err := d.remoteReply("a", at, "plum"); err == nil || f.pane(0).got() != "" {
		t.Errorf("typed before the menu: %v %q", err, f.pane(0).got())
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

// TestRemoteChat answers a fake Claude's approval and question from a
// paired phone: each turn goes on, a second answer and the GUI's get
// conflict, and the approval pushes as a pane's would.
func TestRemoteChat(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d, sock, stop := runDaemon(t, f, func(o *Options) {
		o.RemoteDir = t.TempDir()
		o.Providers = map[model.Provider]session.Provider{model.ProviderClaude: fake.New(fake.Options{})}
	})
	defer stop()
	do := pairPhone(t, d)
	pushed := make(chan *http.Request, 10)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pushed <- r
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
		running := d.pushWake != nil
		d.mu.Unlock()
		if running {
			break
		}
	}

	gui := dialHello(t, sock, proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "gui", Cwd: t.TempDir()})
	// waitRequest waits for chat's item to list one request, and returns it.
	waitRequest := func(chat string) remote.Request {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if got, _ := items(t, do); len(got[chat].Requests) == 1 {
				return got[chat].Requests[0]
			}
		}
		t.Fatalf("chat %s lists no request", chat)
		return remote.Request{}
	}
	waitTurn := func(chat string) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			var s session.Snapshot
			json.Unmarshal(chatCall[proto.ChatDeltas](gui, proto.ChatRead{Chat: chat}).Snapshot, &s)
			if len(s.PendingApprovals)+len(s.PendingUserInputs) == 0 && len(s.Turns) == 1 && s.Turns[0].State == session.TurnCompleted {
				return
			}
		}
		t.Fatalf("chat %s: the turn did not finish", chat)
	}

	r := chatCall[proto.ChatResult](gui, proto.ChatNew{Provider: model.ProviderClaude, Dir: t.TempDir(), Prompt: "approval"})
	if r.Code != "" {
		t.Fatalf("new: %+v", r)
	}
	req := waitRequest(r.Chat)
	if req.Kind != "approval" || req.Tool != "Bash" || req.Command != "rm -rf build && bun run build" {
		t.Fatalf("approval %+v", req)
	}
	select {
	case p := <-pushed:
		if p.Header.Get("Urgency") != "high" {
			t.Fatalf("push headers %v", p.Header)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no push for the chat's approval")
	}
	body := fmt.Sprintf(`{"chat":%q,"request":%q,"decision":"allow"}`, r.Chat, req.ID)
	if st, out := do("POST", "/api/approve", body); st != 200 {
		t.Fatalf("approve: %d %s", st, out)
	}
	if st, out := do("POST", "/api/approve", body); st != http.StatusConflict {
		t.Fatalf("second approve: %d %s", st, out)
	}
	if res := chatCall[proto.ChatResult](gui, proto.ChatApprove{Chat: r.Chat, Request: req.ID, Decision: "deny"}); res.Code != "conflict" {
		t.Fatalf("the GUI's late answer: %+v", res)
	}
	waitTurn(r.Chat)

	r = chatCall[proto.ChatResult](gui, proto.ChatNew{Provider: model.ProviderClaude, Dir: t.TempDir(), Prompt: "input"})
	if r.Code != "" {
		t.Fatalf("new: %+v", r)
	}
	req = waitRequest(r.Chat)
	if req.Kind != "input" || len(req.Questions) != 1 || req.Questions[0].Options[0].Label != "Continue" {
		t.Fatalf("input %+v", req)
	}
	body = fmt.Sprintf(`{"chat":%q,"request":%q,"answers":{%q:["Continue"]}}`, r.Chat, req.ID, req.Questions[0].ID)
	if st, out := do("POST", "/api/input", body); st != 200 {
		t.Fatalf("answer: %d %s", st, out)
	}
	waitTurn(r.Chat)
}

// A window's state carries the command a pending approval asks to run, and
// drops it once the agent moves on.
func TestSnapshotCarriesTheCommand(t *testing.T) {
	d, _, _ := remoteDaemon(t)
	d.o.Derive = agent.Derive
	ctx := context.Background()
	command := func(payload string) string {
		t.Helper()
		if err := d.agentEvent(ctx, proto.AgentEvent{Pane: "a", Provider: model.ProviderClaude, Payload: []byte(payload)}); err != nil {
			t.Fatal(err)
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		for _, a := range d.snapshot().Activities {
			if a.PaneID == "a" {
				return a.Command
			}
		}
		return "missing"
	}
	if got := command(`{"hook_event_name":"PermissionRequest","session_id":"s","tool_name":"Bash","tool_input":{"command":"npm run migrate","description":"Migrate"}}`); got != "npm run migrate" {
		t.Errorf("pending approval: %q", got)
	}
	if got := command(`{"hook_event_name":"PostToolUse","session_id":"s","tool_name":"Bash"}`); got != "" {
		t.Errorf("after the tool ran: %q", got)
	}
}
