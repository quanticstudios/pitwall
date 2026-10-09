package agent

import (
	"path/filepath"
	"strings"

	"github.com/quanticstudios/pitwall/internal/model"
)

// programs names each agent by the program it runs as.
var programs = map[string]model.Provider{
	"claude":       model.ProviderClaude,
	"codex":        model.ProviderCodex,
	"pi":           model.ProviderPi,
	"gemini":       model.ProviderGemini,
	"opencode":     model.ProviderOpenCode,
	"cursor-agent": model.ProviderCursor,
	"amp":          model.ProviderAmp,
	"aider":        model.ProviderAider,
}

// packages names the agents whose npm package an interpreter runs a
// script from, by a directory on the script's path. A script behind a bin
// symlink keeps the link's name, which programs matches instead.
var packages = map[string]model.Provider{
	"/@anthropic-ai/claude-code/": model.ProviderClaude,
	"/@openai/codex/":             model.ProviderCodex,
	"/@google/gemini-cli/":        model.ProviderGemini,
	"/opencode-ai/":               model.ProviderOpenCode,
	"/cursor-agent/":              model.ProviderCursor,
	"/@sourcegraph/amp/":          model.ProviderAmp,
	"/@ampcode/cli/":              model.ProviderAmp,
}

// Identify names the agent a process is from its comm and the path of its
// executable, or returns "" for any other program. comm covers Claude's
// native installer, whose executable is named after its version, and
// node-run Claude, which sets its process title; the executable covers a
// program that renamed itself. Pass nothing else about a process: its
// arguments and environment carry secrets. An agent an interpreter runs
// needs Script as well.
func Identify(comm, exe string) model.Provider {
	for _, s := range []string{comm, strings.TrimSuffix(filepath.Base(exe), ".exe")} {
		if p := programs[s]; p != "" {
			return p
		}
	}
	return ""
}

// Interpreter reports whether a process, by its comm or the path of its
// executable, runs a script whose path Script can name the agent by: node,
// bun, deno or python. The executable covers node 26, which names its main
// thread, and so the comm, "node-MainThread".
func Interpreter(comm, exe string) bool {
	for _, s := range []string{comm, filepath.Base(exe)} {
		switch s {
		case "node", "nodejs", "bun", "deno":
			return true
		}
		if strings.HasPrefix(s, "python") {
			return true
		}
	}
	return false
}

// scriptArgs is how many arguments Script reads past argv[0], to step
// over an interpreter's options.
const scriptArgs = 8

// Script names the agent an interpreter runs from the start of its argv:
// argv[0] when the program set its title, else the first argument that is
// no option, which is the script (deno's run and python's -m module
// included). Only the script's path is matched; the arguments after it
// are never looked at.
func Script(argv []string) model.Provider {
	if len(argv) == 0 {
		return ""
	}
	if p := scriptAgent(argv[0]); p != "" {
		return p
	}
	for i := 1; i < len(argv) && i <= scriptArgs; i++ {
		switch a := argv[i]; {
		case a == "-m" && i+1 < len(argv):
			return programs[argv[i+1]]
		case a == "run" && i == 1, strings.HasPrefix(a, "-"):
		default:
			return scriptAgent(a)
		}
	}
	return ""
}

// scriptAgent names the agent a script path runs: by its base name, less
// a script extension, or by the npm package it lies in.
func scriptAgent(path string) model.Provider {
	base := filepath.Base(path)
	for _, ext := range []string{".js", ".mjs", ".cjs", ".ts", ".py"} {
		base = strings.TrimSuffix(base, ext)
	}
	if p := programs[base]; p != "" {
		return p
	}
	path = filepath.ToSlash(path)
	for dir, p := range packages {
		if strings.Contains(path, dir) {
			return p
		}
	}
	return ""
}

// HooksFor reports whether pitwall can set hooks up for agent p with
// `pitwall hooks install`. Amp and Aider have none.
func HooksFor(p model.Provider) bool {
	switch p {
	case model.ProviderClaude, model.ProviderCodex, model.ProviderPi, model.ProviderGemini, model.ProviderOpenCode, model.ProviderCursor:
		return true
	}
	return false
}

// Command is the program agent p runs as, the name a decision model's
// list of programs uses.
func Command(p model.Provider) string {
	if p == model.ProviderCursor {
		return "cursor-agent"
	}
	return string(p)
}
