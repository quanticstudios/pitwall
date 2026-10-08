package agent

import (
	"path/filepath"

	"github.com/quanticstudios/pitwall/internal/model"
)

// Identify names the agent a process is from its comm and the path of its
// executable, or returns "" for any other program. comm covers Claude's
// native installer, whose executable is named after its version, and
// node-run Claude, which sets its process title; the executable covers a
// program that renamed itself. Pass nothing else about a process: its
// arguments and environment carry secrets.
func Identify(comm, exe string) model.Provider {
	for _, s := range []string{comm, filepath.Base(exe)} {
		switch s {
		case "claude":
			return model.ProviderClaude
		case "codex":
			return model.ProviderCodex
		case "pi":
			return model.ProviderPi
		}
	}
	return ""
}

// HooksFor reports whether a hookless program, by its process name, is an
// agent `pitwall hooks install` can set hooks up for, and which.
func HooksFor(comm string) model.Provider {
	switch comm {
	case "gemini":
		return model.ProviderGemini
	case "opencode":
		return model.ProviderOpenCode
	}
	return ""
}
