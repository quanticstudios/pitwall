package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
)

// A resumed Codex pane gets the rollout named for its session under the
// CODEX_HOME it runs with, so its chat shows the history before a hook.
func TestCodexResumed(t *testing.T) {
	d, _ := talkDaemon(t)
	t.Cleanup(d.shutdown)
	home := t.TempDir()
	sid := "01a1fee8-dd7a-7da3-b443-d061661d6061"
	path := filepath.Join(home, "sessions", "2026", "11", "21", "rollout-2026-11-21T17-20-36-"+sid+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	p := &d.st.Panes[0]
	p.Provider, p.SessionID, p.Env = model.ProviderCodex, sid, []string{"CODEX_HOME=" + home}
	d.codexResumed(*p)
	if p.Transcript != path {
		t.Errorf("transcript %q, want %q", p.Transcript, path)
	}
	p.Transcript, p.SessionID = "", "01a1fee8-0000-7000-8000-000000000000"
	d.codexResumed(*p)
	if p.Transcript != "" {
		t.Errorf("a session with no rollout got %q", p.Transcript)
	}
}
