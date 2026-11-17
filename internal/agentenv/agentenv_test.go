package agentenv

import (
	"os"
	"slices"
	"testing"
)

func TestClean(t *testing.T) {
	keep := []string{"PATH=/bin", "CLAUDE_CODE_USE_BEDROCK=1", "ANTHROPIC_MODEL=opus", "CLAUDE_CONFIG_DIR=/c", "CLAUDE_CODE_SIMPLE=1", "CODEX_HOME=/x", "HTTPS_PROXY=http://p", "TERM_PROGRAM=x"}
	env := append(slices.Clone(keep), "CLAUDECODE=1", "CLAUDE_CODE_CHILD_SESSION=1", "CLAUDE_CODE_ENTRYPOINT=cli", "CLAUDE_CODE_SSE_PORT=1", "CODEX_THREAD_ID=t", "GHOSTTY_RESOURCES_DIR=/g", "WEZTERM_PANE=3", "claudecode=1")
	if got := Clean(env); !slices.Equal(got, keep) {
		t.Fatalf("Clean = %q, want %q", got, keep)
	}
}

func TestUnset(t *testing.T) {
	t.Setenv("CLAUDE_CODE_CHILD_SESSION", "1")
	t.Setenv("CLAUDE_CONFIG_DIR", "/c")
	Unset()
	if _, ok := os.LookupEnv("CLAUDE_CODE_CHILD_SESSION"); ok || os.Getenv("CLAUDE_CONFIG_DIR") != "/c" {
		t.Fatal("Unset kept a marker or dropped a setting")
	}
}
