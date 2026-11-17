package agentenv

import (
	"os"
	"slices"
	"testing"
)

func TestClean(t *testing.T) {
	keep := []string{"PATH=/bin", "CLAUDE_CODE_USE_BEDROCK=1", "ANTHROPIC_MODEL=opus", "CLAUDE_CONFIG_DIR=/c", "CLAUDE_CODE_SIMPLE=1", "CLAUDE_CODE_EFFORT_LEVEL=high", "CODEX_HOME=/x", "HTTPS_PROXY=http://p", "TERM_PROGRAM=x",
		"GHOSTTY_RESOURCES_DIR=/g", "GHOSTTY_BIN_DIR=/bin", "GHOSTTY_SHELL_INTEGRATION_NO_SUDO=1", "GHOSTTY_LOG=debug", "GHOSTTY_CUSTOM_SETTING=1"}
	env := append(slices.Clone(keep), "CLAUDECODE=1", "CLAUDE_CODE_CHILD_SESSION=1", "CLAUDE_CODE_ENTRYPOINT=cli", "CLAUDE_CODE_SSE_PORT=1", "CODEX_THREAD_ID=t", "GHOSTTY_SURFACE_ID=0x1234", "WEZTERM_PANE=3", "claudecode=1")
	original := slices.Clone(env)
	if got := Clean(env); !slices.Equal(got, keep) {
		t.Fatalf("Clean = %q, want %q", got, keep)
	}
	if !slices.Equal(env, original) {
		t.Fatal("Clean changed its input")
	}
}

func TestUnset(t *testing.T) {
	t.Setenv("CLAUDE_CODE_CHILD_SESSION", "1")
	t.Setenv("GHOSTTY_SURFACE_ID", "0x1234")
	t.Setenv("CLAUDE_CONFIG_DIR", "/c")
	t.Setenv("GHOSTTY_RESOURCES_DIR", "/g")
	Unset()
	for _, name := range []string{"CLAUDE_CODE_CHILD_SESSION", "GHOSTTY_SURFACE_ID"} {
		if _, ok := os.LookupEnv(name); ok {
			t.Errorf("Unset kept %s", name)
		}
	}
	for name, want := range map[string]string{"CLAUDE_CONFIG_DIR": "/c", "GHOSTTY_RESOURCES_DIR": "/g"} {
		if os.Getenv(name) != want {
			t.Errorf("Unset dropped %s", name)
		}
	}
}
