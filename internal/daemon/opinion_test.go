package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// A second opinion opens the other agent read only beside the reviewed
// pane, on a file of instructions, and sends its first reply, once, to
// the reviewed agent.
func TestSecondOpinion(t *testing.T) {
	d, f := talkDaemon(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	codex := filepath.Join(t.TempDir(), "codex")
	if runtime.GOOS == "windows" {
		codex += ".exe"
	}
	if err := os.WriteFile(codex, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	opinion := func(agent string) error { return d.secondOpinion(proto.SecondOpinion{Pane: "a", Agent: agent}) }
	refused := func(agent, want string) {
		t.Helper()
		if err := opinion(agent); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: %v, want an error with %q", agent, err, want)
		}
	}
	refused("gemini", "pick claude or codex")
	refused(filepath.Join(t.TempDir(), "claude"), "not installed")
	refused(codex, "runs no agent") // a takes no messages yet

	claudeHook(t, d, "a", `{"hook_event_name":"SessionStart","source":"startup"}`)
	refused(codex, "no git repository")
	// The finished turn refreshes the tab's stats, which the fake git
	// reports for a path with "repo" in it, with no default branch.
	d.mu.Lock()
	d.st.Workspaces[0].Path = "/work/repo"
	d.mu.Unlock()
	claudeHook(t, d, "a", `{"hook_event_name":"Stop","last_assistant_message":"done"}`)
	for start := time.Now(); ; time.Sleep(time.Millisecond) {
		d.mu.Lock()
		_, git := d.st.Stats["w"]
		d.mu.Unlock()
		if git {
			break
		}
		if time.Since(start) > 2*time.Second {
			t.Fatal("no stats for the tab")
		}
	}
	if err := opinion(codex); err != nil {
		t.Fatal(err)
	}

	d.mu.Lock()
	r := d.st.Panes[len(d.st.Panes)-1]
	panes := layout.Panes(d.st.Workspaces[0].Tabs[0].Layout)
	d.mu.Unlock()
	if !slices.Contains(panes, r.ID) {
		t.Fatalf("panes %v lack the reviewer %s", panes, r.ID)
	}
	file := filepath.Join(state, "pitwall", "reviews", "a.md")
	if len(r.Cmd) != 4 || r.Cmd[0] != codex || r.Cmd[1] != "-s" || r.Cmd[2] != "read-only" || !strings.Contains(r.Cmd[3], file) {
		t.Fatalf("reviewer cmd %q", r.Cmd)
	}
	if b, err := os.ReadFile(file); err != nil || !strings.Contains(string(b), "git diff --merge-base origin/HEAD") {
		t.Fatalf("instructions %q, %v", b, err)
	}

	claudeHook(t, d, r.ID, `{"hook_event_name":"Stop","last_assistant_message":"Changes required.\n- [major] x.go:3: nil map. Fix: make it."}`)
	want := reviewNote + "  Changes required. - [major] x.go:3: nil map. Fix: make it.\r"
	if got := gotAfter(t, f.pane(0), 0, 200); !strings.HasSuffix(got, want) {
		t.Fatalf("a got %q, want it to end with %q", got, want)
	}
	// One round: the reviewer's next reply stays with it.
	n := len(f.pane(0).got())
	claudeHook(t, d, r.ID, `{"hook_event_name":"Stop","last_assistant_message":"more"}`)
	d.mu.Lock()
	queued := len(d.mail.queue["a"])
	d.mu.Unlock()
	if queued != 0 || len(f.pane(0).got()) != n {
		t.Fatalf("a second reply went to a: %d queued, %q typed", queued, f.pane(0).got()[n:])
	}
}
