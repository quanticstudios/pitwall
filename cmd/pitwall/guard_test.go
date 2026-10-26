package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/guard"
	"github.com/quanticstudios/pitwall/internal/model"
)

// guardConfig points config.Load at a config.toml with guard_deny.
func guardConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	os.MkdirAll(filepath.Join(dir, "pitwall"), 0o755)
	os.WriteFile(filepath.Join(dir, "pitwall", "config.toml"), []byte("[agents]\nguard_deny = [\"npm publish *\"]\n"), 0o600)
}

// TestGuardHook: in a guarded tab, a Claude Code or Codex PreToolUse of a
// denied command gets the deny JSON on stdout, the reason on stderr and
// exit 2; anything else prints nothing.
func TestGuardHook(t *testing.T) {
	guardConfig(t)
	t.Setenv(guard.Env, "1")
	for _, c := range []struct {
		provider model.Provider
		payload  string
		denied   string
	}{
		{model.ProviderClaude, `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"go test ./... && git push origin x"}}`, "git push origin x"},
		{model.ProviderCodex, `{"hook_event_name":"PreToolUse","tool_name":"shell","tool_input":{"command":["bash","-lc","gh pr merge 3"]}}`, "gh pr merge 3"},
		{model.ProviderCodex, `{"hook_event_name":"PreToolUse","tool_name":"exec_command","tool_input":{"cmd":"npm publish"}}`, "npm publish"},
		{model.ProviderClaude, `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git status"}}`, ""},
		{model.ProviderClaude, `{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"git push"}}`, ""},
		{model.ProviderClaude, `{"hook_event_name":"PreToolUse","tool_name":"Edit","tool_input":{"file_path":"x"}}`, ""},
		{model.ProviderGemini, `{"hook_event_name":"PreToolUse","tool_input":{"command":"git push"}}`, ""},
	} {
		var stdout, stderr bytes.Buffer
		code := guardHook(c.provider, []byte(c.payload), &stdout, &stderr)
		if c.denied == "" {
			if code != 0 || stdout.Len()+stderr.Len() != 0 {
				t.Errorf("%s %s: code %d, printed %q %q", c.provider, c.payload, code, stdout.String(), stderr.String())
			}
			continue
		}
		var out struct {
			HookSpecificOutput struct {
				HookEventName, PermissionDecision, PermissionDecisionReason string
			}
		}
		if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
			t.Fatalf("%s: stdout %q: %v", c.payload, stdout.String(), err)
		}
		o := out.HookSpecificOutput
		if code != 2 || o.HookEventName != "PreToolUse" || o.PermissionDecision != "deny" || o.PermissionDecisionReason != guard.Reason(c.denied) {
			t.Errorf("%s: code %d, output %+v", c.payload, code, o)
		}
		if strings.TrimSpace(stderr.String()) != guard.Reason(c.denied) {
			t.Errorf("%s: stderr %q", c.payload, stderr.String())
		}
	}
	t.Setenv(guard.Env, "")
	var stdout bytes.Buffer
	if code := guardHook(model.ProviderClaude, []byte(`{"hook_event_name":"PreToolUse","tool_input":{"command":"git push"}}`), &stdout, &stdout); code != 0 || stdout.Len() != 0 {
		t.Errorf("unguarded tab: code %d, printed %q", code, stdout.String())
	}
}

// TestGuardCommand: pitwall guard, which pi's extension and OpenCode's
// plugin run, exits 2 with the reason on stderr for a denied command.
func TestGuardCommand(t *testing.T) {
	guardConfig(t)
	for in, want := range map[string]int{
		`{"tool_input":{"command":"git push"}}`:         2,
		`{"tool_input":{"command":"npm publish"}}`:      2,
		`{"tool_input":{"command":"git stash push"}}`:   0,
		`{"tool_input":{"path":"README.md"}}`:           0,
		`not json`:                                      0,
		`{"tool_input":{"command":"echo $(git push)"}}`: 0, // a known bypass
	} {
		var stdout, stderr bytes.Buffer
		if got := runGuard(strings.NewReader(in), &stdout, &stderr); got != want {
			t.Errorf("%s: exit %d, want %d", in, got, want)
		}
		if want == 2 && !strings.HasPrefix(stderr.String(), "pitwall guard: ") {
			t.Errorf("%s: stderr %q", in, stderr.String())
		}
	}
}

// TestGuardedHookUnreported: a denied call is not reported to the daemon;
// an allowed one in a guarded tab is, as in any tab.
func TestGuardedHookUnreported(t *testing.T) {
	guardConfig(t)
	sock, got := silentDaemon(t)
	t.Setenv("PITWALL_PANE", "p1")
	t.Setenv("PITWALL_SOCKET", sock)
	t.Setenv(guard.Env, "1")
	hook := func(req string) int {
		in := filepath.Join(t.TempDir(), "in.json")
		os.WriteFile(in, []byte(req), 0o600)
		stdin, err := os.Open(in)
		if err != nil {
			t.Fatal(err)
		}
		defer stdin.Close()
		devnull, _ := os.Open(os.DevNull)
		defer devnull.Close()
		oldIn, oldOut, oldErr := os.Stdin, os.Stdout, os.Stderr
		os.Stdin, os.Stdout, os.Stderr = stdin, devnull, devnull
		defer func() { os.Stdin, os.Stdout, os.Stderr = oldIn, oldOut, oldErr }()
		return runHook([]string{"claude"})
	}
	if code := hook(`{"hook_event_name":"PreToolUse","tool_input":{"command":"git push"}}`); code != 2 {
		t.Errorf("denied: exit %d", code)
	}
	allowed := `{"hook_event_name":"PreToolUse","tool_input":{"command":"go test"}}`
	if code := hook(allowed); code != 0 {
		t.Errorf("allowed: exit %d", code)
	}
	select {
	case ev := <-got:
		if string(ev.Payload) != allowed {
			t.Errorf("reported %s, want only the allowed call", ev.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the allowed call was not reported")
	}
}
