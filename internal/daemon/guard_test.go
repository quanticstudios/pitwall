package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// hookedDir is a Claude Code and Codex config dir with pitwall's hooks,
// which run this test binary.
func hookedDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for file, hooks := range map[string][]byte{"settings.json": agent.ClaudeHooks(self), "hooks.json": agent.CodexHooks(self)} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(`{"hooks": `+string(hooks)+`}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestGuardGate: a pane with the guard on starts only for Claude Code or
// Codex with pitwall's hooks in the config dir its final environment
// names, else the daemon's, else home's.
func TestGuardGate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("PITWALL_GUARD", "")
	hooked, empty := hookedDir(t), t.TempDir()
	for _, c := range []struct {
		name   string
		daemon string // the daemon's own CLAUDE_CONFIG_DIR
		cmd    []string
		env    []string
		want   string // "" starts
	}{
		{"no hooks anywhere", "", []string{"claude"}, []string{"PITWALL_GUARD=1"}, "no settings.json"},
		{"hooks in another dir", hooked, []string{"/usr/bin/claude"}, []string{"PITWALL_GUARD=1", "CLAUDE_CONFIG_DIR=" + empty}, empty},
		{"a later entry wins", "", []string{"claude"}, []string{"CLAUDE_CONFIG_DIR=" + hooked, "PITWALL_GUARD=1", "CLAUDE_CONFIG_DIR=" + empty}, empty},
		{"another program", hooked, []string{"deepagent"}, []string{"PITWALL_GUARD=1"}, "not deepagent"},
		{"a shell", hooked, nil, []string{"PITWALL_GUARD=1"}, "not a shell"},
		{"guard set to anything", hooked, []string{"aider"}, []string{"PITWALL_GUARD=yes"}, "not aider"},
		{"hooks in the env's dir", "", []string{"claude"}, []string{"PITWALL_GUARD=1", "CLAUDE_CONFIG_DIR=" + hooked}, ""},
		{"hooks in the daemon's dir", hooked, []string{"claude"}, []string{"PITWALL_GUARD=1"}, ""},
		{"codex", "", []string{"codex"}, []string{"PITWALL_GUARD=1", "CODEX_HOME=" + hooked}, ""},
		{"codex without", "", []string{"codex"}, []string{"PITWALL_GUARD=1", "CLAUDE_CONFIG_DIR=" + hooked}, filepath.Join(home, ".codex")},
		{"guard off", "", []string{"deepagent"}, []string{"PITWALL_GUARD="}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("CLAUDE_CONFIG_DIR", c.daemon)
			f := &fakes{statsCalls: map[string]int{}}
			d, err := NewWith(f.options())
			if err != nil {
				t.Fatal(err)
			}
			err = d.handle(context.Background(), proto.NewSession{Cwd: t.TempDir(), Cmd: c.cmd, Env: c.env})
			f.mu.Lock()
			started := len(f.panes)
			f.mu.Unlock()
			if c.want == "" {
				if err != nil || started != 1 {
					t.Fatalf("err %v, %d panes started; want one", err, started)
				}
				return
			}
			if !errors.Is(err, errUnguarded) || !strings.Contains(err.Error(), c.want) || started != 0 {
				t.Fatalf("err %v, %d panes started; want %q and none", err, started, c.want)
			}
			if st := snapshot(d); len(st.Panes) != 0 || len(st.Workspaces) != 0 {
				t.Fatalf("a refused start left %d panes, %d tabs", len(st.Panes), len(st.Workspaces))
			}
		})
	}
}

// TestGuardGateRestore: a guarded agent whose hooks went missing is not
// resumed after a restart, and the window says why.
func TestGuardGateRestore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	hooked := hookedDir(t)
	f := &fakes{statsCalls: map[string]int{}}
	d, err := NewWith(f.options())
	if err != nil {
		t.Fatal(err)
	}
	must(t, d.handle(context.Background(), proto.NewSession{Cwd: t.TempDir(), Cmd: []string{"claude"}, Env: []string{"PITWALL_GUARD=1", "CLAUDE_CONFIG_DIR=" + hooked}}))
	f.saved = snapshot(d)
	f.saved.Panes[0].Provider, f.saved.Panes[0].SessionID = model.ProviderClaude, "s"
	if err := os.Remove(filepath.Join(hooked, "settings.json")); err != nil {
		t.Fatal(err)
	}
	d, err = NewWith(f.options())
	if err != nil {
		t.Fatal(err)
	}
	st := snapshot(d)
	if len(st.Panes) != 0 || len(f.panes) != 1 || !strings.Contains(st.Notice, "guarded tab not started") {
		t.Fatalf("restored %d panes (%d started), notice %q", len(st.Panes), len(f.panes), st.Notice)
	}
}
