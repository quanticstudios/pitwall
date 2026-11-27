package daemon

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/input"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// The phone's key bar presses keys as pitwall send-key encodes them for
// the pane's modes, never in a guarded tab or a pane that is gone.
func TestPhoneKey(t *testing.T) {
	d, f, do := phoneDaemon(t)
	p := f.pane(0)
	key := func(pane, k string) (int, string) {
		b, _ := json.Marshal(map[string]string{"pane": pane, "key": k})
		return do("POST", "/api/key", string(b))
	}
	for _, k := range []string{"esc", "shift+tab", "up", "ctrl+c", "2", "enter"} {
		if st, body := key("a", k); st != 200 {
			t.Fatalf("key %s: %d %s", k, st, body)
		}
	}
	if want := "\x1b\x1b[Z\x1b[A\x032\r"; gotAfter(t, p, 0, len(want)) != want {
		got := p.got()
		t.Fatalf("legacy bytes %q, want %q", got, want)
	}
	modes := vt.Modes{AppCursorKeys: true, KittyKeyboard: 1}
	p.mu.Lock()
	p.input, p.modes = nil, modes
	p.mu.Unlock()
	if st, _ := key("a", "esc"); st != 200 {
		t.Fatalf("kitty esc: %d", st)
	}
	if st, _ := key("a", "down"); st != 200 {
		t.Fatalf("app cursor down: %d", st)
	}
	esc, _ := input.EncodeKey("esc", modes)
	down, _ := input.EncodeKey("down", modes)
	if want := string(esc) + string(down); want != "\x1b[27;1u\x1b[1;1B" || gotAfter(t, p, 0, len(want)) != want {
		got := p.got()
		t.Fatalf("mode bytes %q, want %q", got, want)
	}
	if st, body := key("nope", "enter"); st != 409 || !strings.Contains(body, "no running pane nope") {
		t.Errorf("unknown pane: %d %s", st, body)
	}
	if st, _ := key("a", "f5"); st != 400 {
		t.Errorf("a key off the bar: %d", st)
	}
	d.mu.Lock()
	for i := range d.st.Panes {
		if d.st.Panes[i].ID == "a" {
			d.st.Panes[i].Env = []string{"PITWALL_GUARD=1"}
		}
	}
	d.mu.Unlock()
	p.mu.Lock()
	p.input = nil
	p.mu.Unlock()
	if st, body := key("a", "enter"); st != 409 || !strings.Contains(body, "guarded") || p.got() != "" {
		t.Errorf("guarded tab: %d %s, typed %q", st, body, p.got())
	}
}

// The Screen view reads the pane as pitwall read --screen does, redacted.
func TestPhoneScreen(t *testing.T) {
	_, f, do := phoneDaemon(t)
	setScreen(f.pane(0), []string{"key sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789", " ❯ 1. Yes", "   2. No"})
	st, body := do("GET", "/api/screen?pane=a", "")
	var r struct{ Text string }
	json.Unmarshal([]byte(body), &r)
	if st != 200 || strings.Contains(r.Text, "abcdefghijklmnop") || !strings.HasSuffix(r.Text, " ❯ 1. Yes\n   2. No") {
		t.Fatalf("screen: %d %q", st, body)
	}
	if st, _ := do("GET", "/api/screen?pane=nope", ""); st != 409 {
		t.Errorf("unknown pane: %d", st)
	}
}

// Stop presses Esc in a working agent's pane, as the window's Stop does,
// and a menu that holds a phone message says so in the tab.
func TestPhoneStopAndMenuHold(t *testing.T) {
	d, f, do := phoneDaemon(t)
	p := f.pane(0)
	event(t, d, "a", "working")
	if st, body := do("POST", "/api/stop", `{"pane":"a"}`); st != 200 {
		t.Fatalf("stop: %d %s", st, body)
	}
	if got := gotAfter(t, p, 0, 1); got != "\x1b" {
		t.Fatalf("stop typed %q", got)
	}
	event(t, d, "a", "completed")
	setScreen(p, []string{" Do you want to proceed?", " > 1. Yes", "   2. No", " Esc to cancel"})
	n := len(p.got())
	if _, err := d.phoneSend("a", "held"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if tab := d.phoneTabs()[0]; !tab.HoldMenu || !strings.Contains(tab.Hold, "menu") || p.got()[n:] != "" {
		t.Fatalf("menu hold %q %v, typed %q", tab.Hold, tab.HoldMenu, p.got()[n:])
	}
}
