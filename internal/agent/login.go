package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/quanticstudios/pitwall/internal/model"
)

// LoggedIn returns why provider's agent reading its config from dir would
// start at a login screen, nil when it has a login or pitwall cannot tell.
// get reads the agent's environment. Claude Code keeps its login in
// .credentials.json there, except on macOS, where it uses the Keychain;
// Codex keeps it in auth.json. An API key in the environment counts too.
func LoggedIn(provider model.Provider, dir string, get func(string) string) error {
	var file string
	var keys []string
	switch provider {
	case model.ProviderClaude:
		if runtime.GOOS == "darwin" {
			return nil
		}
		file, keys = ".credentials.json", []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"}
	case model.ProviderCodex:
		file, keys = "auth.json", []string{"OPENAI_API_KEY", "CODEX_API_KEY"}
	default:
		return nil
	}
	for _, k := range keys {
		if get(k) != "" {
			return nil
		}
	}
	if _, err := os.Stat(filepath.Join(dir, file)); err != nil {
		return fmt.Errorf("no %s login in %s", provider, dir)
	}
	return nil
}
