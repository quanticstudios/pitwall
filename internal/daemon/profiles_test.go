package daemon

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func TestProfileTabsAndRestart(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	o.Profiles = func() map[string]config.Profile {
		return map[string]config.Profile{"work": {ClaudeConfigDir: "/work/claude", CodexHome: "/work/codex"}, "personal": {ClaudeConfigDir: "/personal/claude", CodexHome: "/personal/codex"}}
	}
	d := newDaemon(t, o)
	ctx := context.Background()
	dir := t.TempDir()
	must(t, d.handle(ctx, proto.NewTask{Task: model.Task{Dir: dir, Profile: "work", Cmd: []string{"claude", "hello"}, Env: []string{"OTHER=keep"}}}))
	wid := d.st.Workspaces[0].ID
	must(t, d.handle(ctx, proto.NewTab{WorkspaceID: wid, Profile: "personal"}))
	for i, name := range []string{"work", "personal"} {
		w := d.st.Workspaces[i]
		got := f.pane(i).cfg.Env
		if w.Profile != name || !slices.Contains(got, "CLAUDE_CONFIG_DIR=/"+name+"/claude") || !slices.Contains(got, "CODEX_HOME=/"+name+"/codex") || !slices.Contains(got, "PITWALL_PROFILE="+name) {
			t.Fatalf("%s workspace %+v env %q", name, w, got)
		}
	}
	must(t, d.handle(ctx, proto.OpenPane{WorkspaceID: wid, Target: d.st.Panes[0].ID}))
	if got := f.pane(2).cfg.Env; !slices.Contains(got, "CODEX_HOME=/work/codex") || slices.Contains(got, "OTHER=keep") {
		t.Fatalf("split env %q", got)
	}
	if err := d.handle(ctx, proto.NewSession{Cwd: dir, Profile: "missing"}); err == nil {
		t.Fatal("unknown profile started")
	}
	d.st.Panes[0].Provider, d.st.Panes[0].SessionID = model.ProviderClaude, "session"
	f.saved = d.st
	// A restart uses saved directories even if the configured account is gone.
	o.Profiles = nil
	restarted, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.st.Workspaces[0].Profile != "work" || !slices.Contains(f.pane(3).cfg.Env, "CODEX_HOME=/work/codex") || !slices.Contains(f.pane(3).cfg.Cmd, "--resume") {
		t.Fatalf("restart workspace %+v pane %+v", restarted.st.Workspaces[0], f.pane(3).cfg)
	}
}

func TestLimitResumeUsesProfile(t *testing.T) {
	clock := useFakeClock(t, time.Date(2026, 10, 22, 13, 0, 0, 0, time.UTC))
	d, _ := talkDaemon(t)
	d.mu.Lock()
	d.setProvider("a", model.ProviderClaude)
	d.mu.Unlock()
	d.o.ResumeAfterLimit = func() bool { return true }
	d.o.Limits = func() []flow.Limit {
		return []flow.Limit{
			{Provider: model.ProviderClaude, Windows: []flow.Window{{Used: 100, Resets: clock.now.Add(time.Hour)}}},
			{Provider: model.ProviderClaude, Profile: "work", Windows: []flow.Window{{Used: 100, Resets: clock.now.Add(2 * time.Hour)}}},
			{Provider: model.ProviderClaude, Profile: "personal", Windows: []flow.Window{{Used: 100, Resets: clock.now.Add(3 * time.Hour)}}},
		}
	}
	for _, name := range []string{"", "work", "personal", "missing"} {
		d.mu.Lock()
		w := d.paneTab("a")
		w.Profile = name
		var turn time.Time
		if i := d.activityIndex("a"); i >= 0 {
			turn = d.st.Activities[i].UpdatedAt
		}
		d.mu.Unlock()
		d.limitStopped("a", turn, "usage limit", time.Time{})
		got := resumes(d, "a")
		if name == "missing" {
			if !got.IsZero() {
				t.Fatal("unknown profile borrowed another account's reset")
			}
			continue
		}
		hours := map[string]time.Duration{"": 1, "work": 2, "personal": 3}[name]
		if want := clock.now.Add(hours*time.Hour + resumeDelay); !got.Equal(want) {
			t.Fatalf("%s resumed at %v, want %v", name, got, want)
		}
		d.handle(t.Context(), proto.CancelResume{Pane: "a"})
	}
}
