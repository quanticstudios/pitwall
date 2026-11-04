package store

import (
	"slices"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
)

func TestForkCmd(t *testing.T) {
	for _, tc := range []struct {
		name string
		pane model.Pane
		want []string
	}{
		{"claude flags", model.Pane{Provider: model.ProviderClaude, SessionID: "source", AgentMode: "plan", Cmd: []string{"/bin/claude", "--resume=old", "--continue", "--fork-session", "--session-id", "fixed", "--model", "opus", "--settings", "account.json", "--permission-mode=acceptEdits", "--allowed-tools", "Read", "Edit", "--print", "--output-format", "json", "--worktree", "old-tree", "--name", "old-name", "old prompt"}},
			[]string{"/bin/claude", "--model", "opus", "--settings", "account.json", "--permission-mode=acceptEdits", "--allowed-tools", "Read", "Edit", "--fork-session", "--resume", "source"}},
		{"claude reported mode", model.Pane{Provider: model.ProviderClaude, SessionID: "source", AgentMode: "bypassPermissions", Cmd: []string{"claude", "--", "--permission-mode=plan"}},
			[]string{"claude", "--dangerously-skip-permissions", "--fork-session", "--resume", "source"}},
		{"claude missing value", model.Pane{Provider: model.ProviderClaude, SessionID: "source", Cmd: []string{"claude", "--model"}},
			[]string{"claude", "--fork-session", "--resume", "source"}},
		{"codex flags", model.Pane{Provider: model.ProviderCodex, SessionID: "source", Cmd: []string{"/bin/codex", "resume", "old", "--last", "--all", "--model=gpt-6", "--profile", "work", "-c", "model_reasoning_effort=high", "--sandbox", "workspace-write", "-a", "on-request", "--image", "one.png", "two.png", "-C", "/old", "--cd=/also-old", "-C/short", "--worktree", "--add-dir", "../lib", "old prompt"}},
			[]string{"/bin/codex", "fork", "--model=gpt-6", "--profile", "work", "-c", "model_reasoning_effort=high", "--sandbox", "workspace-write", "-a", "on-request", "-C", "/old", "--cd=/also-old", "-C/short", "--add-dir", "../lib", "source"}},
		{"codex attached and mode", model.Pane{Provider: model.ProviderCodex, SessionID: "source", AgentMode: "bypassPermissions", Cmd: []string{"codex", "fork", "old", "-mgpt-6", "-pwork", "-cx=true", "--no-alt-screen", "--", "--sandbox=read-only"}},
			[]string{"codex", "fork", "-mgpt-6", "-pwork", "-cx=true", "--no-alt-screen", "--dangerously-bypass-approvals-and-sandbox", "source"}},
		{"codex option as value", model.Pane{Provider: model.ProviderCodex, SessionID: "source", Cmd: []string{"codex", "-c", "--worktree", "--model", "--cd", "--profile"}},
			[]string{"codex", "fork", "-c", "--worktree", "--model", "--cd", "source"}},
		{"codex dangling directory", model.Pane{Provider: model.ProviderCodex, SessionID: "source", Cmd: []string{"codex", "--cd"}},
			[]string{"codex", "fork", "source"}},
		{"wrapper", model.Pane{Provider: model.ProviderClaude, SessionID: "source", AgentMode: "plan", Cmd: []string{"sh", "launch.sh", "--model", "wrong"}},
			[]string{"claude", "--permission-mode", "plan", "--fork-session", "--resume", "source"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := slices.Clone(tc.pane.Cmd)
			got, err := ForkCmd(tc.pane)
			if err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("ForkCmd = %q, %v; want %q", got, err, tc.want)
			}
			if !slices.Equal(tc.pane.Cmd, original) {
				t.Fatal("fork changed the source command")
			}
			// The fork's next restart resumes its own id without forking again.
			p := tc.pane
			p.Cmd, p.SessionID = got, "child"
			resume := RestoreCmd(p)
			if slices.Contains(resume, "--fork-session") || resume[len(resume)-1] != "child" || p.Provider == model.ProviderCodex && resume[1] != "resume" {
				t.Fatalf("restart = %q", resume)
			}
		})
	}
}

func TestForkCmdErrors(t *testing.T) {
	for _, tc := range []struct {
		pane model.Pane
		why  string
	}{
		{model.Pane{ID: "p", Provider: model.ProviderClaude}, "claude has no stored session id yet"},
		{model.Pane{ID: "p", Provider: model.ProviderCodex}, "codex has no stored session id yet"},
		{model.Pane{ID: "p", Provider: model.ProviderPi, SessionID: "s"}, `cannot fork agent "pi"`},
		{model.Pane{ID: "p"}, "no agent is running in this pane"},
	} {
		if _, err := ForkCmd(tc.pane); err == nil || !strings.Contains(err.Error(), tc.why) || !strings.Contains(err.Error(), "pane p") {
			t.Errorf("ForkCmd(%+v) = %v; want %q", tc.pane, err, tc.why)
		}
	}
}

func TestForkCodexDirectory(t *testing.T) {
	for _, args := range [][]string{{"--cd", "other"}, {"--cd=other"}, {"-C", "other"}, {"-Cother"}, {"-c", "--cd", "--model", "--cd", "--cd", "other"}} {
		p := model.Pane{Provider: model.ProviderCodex, SessionID: "s", Cmd: append([]string{"codex"}, args...)}
		if got := CodexCwd(p); got != "other" {
			t.Fatalf("directory in %q = %q", args, got)
		}
		got, err := ForkCmdIn(p, "/new")
		if err != nil || !slices.Equal(got[len(got)-3:], []string{"--cd", "/new", "s"}) || slices.Contains(got, "other") || slices.Contains(got, "-Cother") {
			t.Fatalf("fork in /new: %q, %v", got, err)
		}
	}
	for _, args := range [][]string{{"--cd"}, {"--", "--cd", "prompt"}, {"-c", "--cd"}} {
		p := model.Pane{Provider: model.ProviderCodex, SessionID: "s", Cmd: append([]string{"codex"}, args...)}
		if dir := CodexCwd(p); dir != "" {
			t.Fatalf("directory in %q = %q", args, dir)
		}
	}
}

func TestForkSourceIsNotResumable(t *testing.T) {
	for _, provider := range []model.Provider{model.ProviderClaude, model.ProviderCodex} {
		p := model.Pane{Provider: provider, SessionID: "source"}
		cmd, err := ForkCmd(p)
		if err != nil {
			t.Fatal(err)
		}
		p.Cmd, p.Held = cmd, true
		if ForkSource(p) != "source" || Resumes(p) {
			t.Fatalf("source id accepted for fork: %+v", p)
		}
		if _, err := ForkCmd(p); err == nil {
			t.Fatal("fork accepted the source id")
		}
		p.SessionID = "child"
		if !Resumes(p) || RestoreCmd(p)[len(RestoreCmd(p))-1] != "child" {
			t.Fatal("child does not resume")
		}
	}
}
