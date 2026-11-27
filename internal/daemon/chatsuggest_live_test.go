package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

// TestLiveSuggest asks the real agents for a suggestion with the exact
// command lines chatSuggest builds, each under a throwaway config: set
// PITWALL_LIVE_CLAUDE_DIR (a CLAUDE_CONFIG_DIR), PITWALL_LIVE_CODEX_HOME
// (a CODEX_HOME) or PITWALL_LIVE_AGY_HOME (a HOME for agy) to run one.
func TestLiveSuggest(t *testing.T) {
	conv := finished("Add a --dry-run flag to the deploy script", "Added --dry-run to deploy.sh: it prints each step instead of running it. The README documents it.")
	for _, tc := range []struct {
		prov model.Provider
		key  string
		env  func(dir string) []string
	}{
		{model.ProviderClaude, "PITWALL_LIVE_CLAUDE_DIR", func(d string) []string { return []string{"CLAUDE_CONFIG_DIR=" + d} }},
		{model.ProviderCodex, "PITWALL_LIVE_CODEX_HOME", func(d string) []string { return []string{"CODEX_HOME=" + d} }},
		{model.ProviderAntigravity, "PITWALL_LIVE_AGY_HOME", func(d string) []string { return []string{"HOME=" + d} }},
	} {
		t.Run(string(tc.prov), func(t *testing.T) {
			dir := os.Getenv(tc.key)
			if dir == "" {
				t.Skip(tc.key + " is not set")
			}
			work := filepath.Join(dir, "work")
			os.MkdirAll(work, 0o700)
			run := suggestRun{prov: tc.prov, dir: work, env: tc.env(dir), snap: conv, busy: func() bool { return false }}
			prompt := "Here is the end of a conversation between a user and a coding agent.\n\n<conversation>\n" + suggestContext(conv) + "\n</conversation>\n\n" + suggestPrompt
			cmd, ok := suggestCommand(tc.prov, run.env, prompt)
			if !ok {
				t.Fatal("no command")
			}
			t.Logf("%s %q", cmd.name, cmd.args[:len(cmd.args)-1])
			start := time.Now()
			text, err := runSuggest(context.Background(), run, cmd)
			t.Logf("%s answered %q in %s (err %v)", tc.prov, text, time.Since(start).Round(100*time.Millisecond), err)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestLiveAway asks the real Claude Code for a Since you left summary of
// awaySnap with the prompt awayOpen sends, under the throwaway
// PITWALL_LIVE_CLAUDE_DIR.
func TestLiveAway(t *testing.T) {
	dir := os.Getenv("PITWALL_LIVE_CLAUDE_DIR")
	if dir == "" {
		t.Skip("PITWALL_LIVE_CLAUDE_DIR is not set")
	}
	work := filepath.Join(dir, "work")
	os.MkdirAll(work, 0o700)
	left := time.Now().Add(-time.Hour)
	snap := awaySnap(left)
	run := suggestRun{prov: model.ProviderClaude, dir: work, env: []string{"CLAUDE_CONFIG_DIR=" + dir}, snap: snap, busy: func() bool { return false }}
	start := time.Now()
	out, err := awaySummarize(context.Background(), run, awayPrompt(snap, left))
	status, needs := awayLines(out)
	t.Logf("answered in %s (err %v):\nState: %s\nNeeds: %s", time.Since(start).Round(100*time.Millisecond), err, status, needs)
	if err != nil || status == "" || needs == "" {
		t.Fatalf("no summary from %q", out)
	}
}
