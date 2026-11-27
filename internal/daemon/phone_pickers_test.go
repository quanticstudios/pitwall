package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/remote"
)

func TestAgyFamilies(t *testing.T) {
	list := "Fetching available models...\ngemini-4-neon-high\tNeon\ngemini-4-neon-low\tNeon (Low)\ngemini-3.8-flash-xhigh\tFlash\nclaude-opus-5-5\tOpus\nBad Name\tx\n"
	if got, want := agyFamilies(list), []string{"gemini-4-neon", "gemini-3.8-flash", "claude-opus-5-5"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// agyPhone makes phoneDaemon's pane a an agy at its input box, its footer
// the fixture's, and `agy models` a fixed list.
func agyPhone(t *testing.T) (*Daemon, *fakes, func(method, path, body string) (int, string)) {
	t.Helper()
	old := agyModels
	t.Cleanup(func() { agyModels = old })
	agyModels = func() string { return "gemini-4-neon-high\tNeon\ngemini-3.8-flash-low\tFlash (Low)\n" }
	d, f, do := phoneDaemon(t)
	b, err := os.ReadFile(filepath.Join("..", "agent", "testdata", "antigravity-footer.txt"))
	if err != nil {
		t.Fatal(err)
	}
	rows := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	// why: the fake pane is 80 bytes wide; agy pads its footer to the pane's width
	foot := rows[len(rows)-1]
	left, right := foot[:strings.Index(foot, "   ")], strings.TrimLeft(foot[strings.Index(foot, "   "):], " ")
	rows[len(rows)-1] = left + strings.Repeat(" ", 80-len(left)-len(right)) + right
	setScreen(f.pane(0), rows)
	d.mu.Lock()
	d.st.Panes[0].Provider = model.ProviderAntigravity
	d.st.Activities[d.activityIndex("a")].Provider = model.ProviderAntigravity
	d.mu.Unlock()
	return d, f, do
}

// TestPhoneTabsAgy: an agy tab offers agy's models, once listed, its
// efforts and modes, and shows what its footer says it runs.
func TestPhoneTabsAgy(t *testing.T) {
	d, _, _ := agyPhone(t)
	var tab remote.Tab
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if tab = d.phoneTabs()[0]; tab.Models != nil {
			break
		}
	}
	if !reflect.DeepEqual(tab.Models, []string{"gemini-4-neon", "gemini-3.8-flash"}) || !reflect.DeepEqual(tab.Efforts, remote.AgyEfforts) || !reflect.DeepEqual(tab.Modes, remote.AgyModes) {
		t.Errorf("lists %q %q %q", tab.Models, tab.Efforts, tab.Modes)
	}
	if tab.Picker || tab.Model != "Gemini 3.8 Flash" || tab.Effort != "low" || tab.Mode != "accept-edits" {
		t.Errorf("tab %+v", tab)
	}
}

// TestPhoneSetAgy: the phone's agy picks go in as agy's own /model and
// /effort and as Shift+Tab, as the desktop's pane chat sends them, and a
// model waits for the turn to end.
func TestPhoneSetAgy(t *testing.T) {
	d, f, do := agyPhone(t)
	for deadline := time.Now().Add(2 * time.Second); d.phoneTabs()[0].Models == nil && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	cmd := func(what, value string) (int, string) {
		b, _ := json.Marshal(map[string]string{"pane": "a", "what": what, "value": value})
		return do("POST", "/api/command", string(b))
	}
	if st, body := cmd("model", "gemini-4-neon"); st != 200 {
		t.Fatalf("model: %d %s", st, body)
	}
	if got := gotAfter(t, f.pane(0), 0, len("/model gemini-4-neon\r")); got != "/model gemini-4-neon\r" {
		t.Errorf("model typed %q", got)
	}
	n := len(f.pane(0).got())
	if st, body := cmd("effort", "xhigh"); st != 200 {
		t.Fatalf("effort: %d %s", st, body)
	}
	if got := gotAfter(t, f.pane(0), n, len("/effort xhigh\r")); got != "/effort xhigh\r" {
		t.Errorf("effort typed %q", got)
	}
	// The footer says accept-edits; auto is one Shift+Tab on.
	fp := f.pane(0)
	fp.mu.Lock()
	foot := len(fp.screen) - 1
	fp.wrote = func() {
		if strings.Contains(string(fp.input), "\x1b[Z") {
			fp.screen[foot] = strings.Replace(fp.screen[foot], "accept-edits", "        auto", 1)
		}
	}
	fp.mu.Unlock()
	n = len(fp.got())
	if st, body := cmd("mode", "auto"); st != 200 {
		t.Fatalf("mode: %d %s", st, body)
	}
	if got := fp.got()[n:]; got != "\x1b[Z" {
		t.Errorf("mode typed %q, want one Shift+Tab", got)
	}
	if st, _ := cmd("model", "opus"); st != 400 {
		t.Errorf("a Claude alias for agy: %d", st)
	}
	d.mu.Lock()
	d.st.Activities[d.activityIndex("a")].State = model.StateWorking
	d.mu.Unlock()
	n = len(fp.got())
	if st, body := cmd("model", "gemini-3.8-flash"); st != 409 || !strings.Contains(body, "try again when it waits at its input box") {
		t.Errorf("model mid-turn: %d %s", st, body)
	}
	time.Sleep(20 * time.Millisecond)
	if got := fp.got()[n:]; got != "" {
		t.Errorf("typed mid-turn %q", got)
	}
}

// TestPhoneModeClaude: a Claude Code tab shows the mode its footer reads,
// and a mode pick is Shift+Tab until the footer reads it (paneMode).
func TestPhoneModeClaude(t *testing.T) {
	d, f, do := phoneDaemon(t)
	cycle := []string{"", "  ⏵⏵ accept edits on (shift+tab to cycle)", "  ⏸ plan mode on (shift+tab to cycle)"}
	fp := f.pane(0)
	fp.mu.Lock()
	footer := func() {
		fp.screen = []string{"────────", "❯ ", "────────", cycle[strings.Count(string(fp.input), "\x1b[Z")%len(cycle)]}
	}
	footer()
	fp.wrote = footer
	fp.mu.Unlock()
	if tab := d.phoneTabs()[0]; tab.Mode != "default" || !reflect.DeepEqual(tab.Modes, []string{"default", "acceptEdits", "plan"}) {
		t.Fatalf("tab mode %q of %q", tab.Mode, tab.Modes)
	}
	if st, body := do("POST", "/api/command", `{"pane":"a","what":"mode","value":"plan"}`); st != 200 {
		t.Fatalf("mode: %d %s", st, body)
	}
	if got := fp.got(); got != "\x1b[Z\x1b[Z" {
		t.Errorf("typed %q, want Shift+Tab twice", got)
	}
	if tab := d.phoneTabs()[0]; tab.Mode != "plan" {
		t.Errorf("after the pick the tab says %q", tab.Mode)
	}
	if st, _ := do("POST", "/api/command", `{"pane":"a","what":"mode","value":"bypassPermissions"}`); st != 400 {
		t.Errorf("bypass in a session not started with it: %d", st)
	}
}
