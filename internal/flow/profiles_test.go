package flow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
)

func TestLimitsPerProfile(t *testing.T) {
	home, state := t.TempDir(), t.TempDir()
	now := time.Now().Truncate(time.Second)
	profiles := map[string]config.Profile{"work": {CodexHome: t.TempDir(), Label: "Work plan"}, "personal": {CodexHome: t.TempDir()}, "claude-only": {ClaudeConfigDir: t.TempDir()}}
	for i, name := range []string{"", "work", "personal"} {
		root := profiles[name].CodexHome
		if name == "" {
			root = filepath.Join(home, ".codex")
		}
		writeFile(t, filepath.Join(root, "sessions", now.Format("2006/01/02"), "rollout.jsonl"), fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":%d,"window_minutes":300,"resets_at":%d}}}}`+"\n", now.Format(time.RFC3339), 10+i, now.Add(time.Hour).Unix()))
		l := Limit{Provider: model.ProviderClaude, Profile: name, Seen: now, Windows: []Window{{Minutes: 300, Used: float64(70 + i), Resets: now.Add(time.Hour)}}}
		data, _ := json.Marshal(l)
		if err := os.WriteFile(ClaudeLimitsPath(state, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var s ProfileScanner
	for range 2 {
		limits := s.Read(home, state, profiles, now, 2)
		if len(limits) != 6 {
			t.Fatalf("got %d limits: %+v", len(limits), limits)
		}
		for i, name := range []string{"", "work", "personal"} {
			found := 0
			for _, l := range limits {
				if l.Profile != name {
					continue
				}
				found++
				want := float64(10 + i)
				if l.Provider == model.ProviderClaude {
					want = float64(70 + i)
				}
				if l.Windows[0].Used != want || l.ProfileLabel != profiles[name].Label {
					t.Fatalf("wrong account: %+v, want %s %g", l, name, want)
				}
			}
			if found != 2 {
				t.Fatalf("%s has %d limits", name, found)
			}
		}
	}
	delete(profiles, "work")
	if got := s.Read(home, state, profiles, now, 2); len(got) != 4 || s.scans["work"] != nil {
		t.Fatalf("removed profile kept: %+v", got)
	}
	profiles["personal"] = config.Profile{CodexHome: t.TempDir()}
	if got := s.Read(home, state, profiles, now, 2); len(got) != 3 {
		t.Fatalf("old rollout cache kept: %+v", got)
	}
}

func TestClaudeLimitsPath(t *testing.T) {
	dir := t.TempDir()
	if got := ClaudeLimitsPath(dir, ""); got != filepath.Join(dir, ClaudeLimitsFile) {
		t.Fatal(got)
	}
	for _, name := range []string{"work", "../work", "a/b", "équipe"} {
		got := ClaudeLimitsPath(dir, name)
		if filepath.Dir(got) != dir || got == ClaudeLimitsPath(dir, "") || got != ClaudeLimitsPath(dir, name) {
			t.Fatalf("unsafe or unstable path %q", got)
		}
	}
	if ClaudeLimitsPath(dir, "a/b") == ClaudeLimitsPath(dir, "a_b") {
		t.Fatal("profiles collide")
	}
}
