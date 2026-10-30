package daemon

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

func claudeContinueFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "claude-continue", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeClaudeContinueFixture(t *testing.T, path, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, claudeContinueFixture(t, name), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeAutoContinueFile(t *testing.T) {
	for _, name := range []string{"on", "off", "absent", "null", "invalid", "malformed", "wrong-case"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			writeClaudeContinueFixture(t, path, name)
			if got, want := claudeAutoContinueFile(path), name != "off"; got != want {
				t.Errorf("auto continue = %v, want %v", got, want)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, claudeContinueFixture(t, name)) {
				t.Fatalf("settings changed: %v", err)
			}
		})
	}
	// A missing file uses Claude's default; a read error also defers to Claude.
	dir := t.TempDir()
	for _, path := range []string{filepath.Join(dir, "missing.json"), dir} {
		if !claudeAutoContinueFile(path) {
			t.Errorf("did not defer to Claude for %s", path)
		}
	}
}

func TestClaudeAutoContinueConfigDir(t *testing.T) {
	home, override, paneDir := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	writeClaudeContinueFixture(t, filepath.Join(home, ".claude", "settings.json"), "off")
	writeClaudeContinueFixture(t, filepath.Join(override, "settings.json"), "on")
	writeClaudeContinueFixture(t, filepath.Join(paneDir, "settings.json"), "off")
	if claudeAutoContinue(model.Pane{}) {
		t.Fatal("did not read the default user settings")
	}
	t.Setenv("CLAUDE_CONFIG_DIR", override)
	if !claudeAutoContinue(model.Pane{}) {
		t.Fatal("ignored CLAUDE_CONFIG_DIR")
	}
	if claudeAutoContinue(model.Pane{Env: []string{"CLAUDE_CONFIG_DIR=" + paneDir}}) {
		t.Fatal("ignored the pane's CLAUDE_CONFIG_DIR")
	}
	if claudeAutoContinue(model.Pane{Env: []string{"CLAUDE_CONFIG_DIR="}}) {
		t.Fatal("an empty pane override did not restore the default path")
	}
	if claudeAutoContinue(model.Pane{Cwd: filepath.Dir(paneDir), Env: []string{"CLAUDE_CONFIG_DIR=" + filepath.Base(paneDir)}}) {
		t.Fatal("a relative config directory did not use the pane's cwd")
	}
}

func TestResumeClaudeAutoContinue(t *testing.T) {
	for _, source := range []string{"hook", "screen"} {
		for _, name := range []string{"on", "off", "absent", "invalid", "malformed", "wrong-case"} {
			t.Run(source+"/"+name, func(t *testing.T) {
				clock := useFakeClock(t, time.Date(2026, 10, 22, 13, 20, 0, 0, time.UTC))
				d, f := talkDaemon(t)
				d.o.ResumeAfterLimit = func() bool { return true }
				path := filepath.Join(t.TempDir(), "settings.json")
				writeClaudeContinueFixture(t, path, name)
				d.o.ClaudeContinues = func(model.Pane) bool { return claudeAutoContinueFile(path) }
				msg := "You've hit your session limit · resets 3pm (UTC)"
				if source == "hook" {
					claudeHook(t, d, "a", claudeLimit)
				} else {
					d.mu.Lock()
					d.setProvider("a", model.ProviderClaude)
					d.mu.Unlock()
					f.pane(0).screen = []string{msg}
					d.checkLimits()
				}
				d.mu.Lock()
				acts := d.snapshot().Activities
				d.mu.Unlock()
				if len(acts) != 1 || acts[0].AutoContinues != (name != "off") || acts[0].Resumes.IsZero() != (name != "off") {
					t.Fatalf("activities = %+v", acts)
				}
				want := 0
				if name == "off" {
					want = 1
				}
				if len(clock.fns) != want || len(d.lim.pending) != want {
					t.Fatalf("timers %d, pending %d, want %d", len(clock.fns), len(d.lim.pending), want)
				}
				// The same settings never suppress Codex's timer or typing.
				d.mu.Lock()
				d.setProvider("b", model.ProviderCodex)
				d.mu.Unlock()
				f.pane(1).screen = []string{"You’ve hit your usage limit. Try again at 3:04 PM."}
				d.checkLimits()
				if len(clock.fns) != want+1 || resumes(d, "b").IsZero() {
					t.Fatal("Codex resume was suppressed")
				}
				clock.fire()
				if got := gotAfter(t, f.pane(1), 0, 9); got != "continue\r" {
					t.Fatalf("Codex typed %q", got)
				}
			})
		}
	}
}

func TestResumeClaudeAutoContinueEnabledWhileWaiting(t *testing.T) {
	clock := useFakeClock(t, time.Date(2026, 10, 22, 13, 20, 0, 0, time.UTC))
	d, f := talkDaemon(t)
	d.o.ResumeAfterLimit = func() bool { return true }
	path := filepath.Join(t.TempDir(), "settings.json")
	writeClaudeContinueFixture(t, path, "off")
	d.o.ClaudeContinues = func(model.Pane) bool { return claudeAutoContinueFile(path) }
	claudeHook(t, d, "a", claudeLimit)
	if len(clock.fns) != 1 {
		t.Fatal("no initial resume")
	}
	writeClaudeContinueFixture(t, path, "on")
	if !resumes(d, "a").IsZero() {
		t.Fatal("a pending resume is still advertised")
	}
	clock.fire()
	if got := f.pane(0).got(); got != "" {
		t.Fatalf("typed %q with Claude's option on", got)
	}
}

func TestResumeClaudeAutoContinueWithoutReset(t *testing.T) {
	clock := useFakeClock(t, time.Date(2026, 10, 22, 13, 20, 0, 0, time.UTC))
	d, f := talkDaemon(t)
	d.o.ResumeAfterLimit = func() bool { return true }
	d.o.ClaudeContinues = func(model.Pane) bool {
		return claudeAutoContinueFile(filepath.Join("testdata", "claude-continue", "on.json"))
	}
	d.mu.Lock()
	d.setProvider("a", model.ProviderClaude)
	d.mu.Unlock()
	f.pane(0).screen = []string{"You've hit your session limit"}
	d.checkLimits()
	d.mu.Lock()
	acts := d.snapshot().Activities
	d.mu.Unlock()
	if len(clock.fns) != 0 || len(acts) != 1 || !acts[0].AutoContinues || !acts[0].Resumes.IsZero() {
		t.Fatalf("timers %d, activities %+v", len(clock.fns), acts)
	}
}
