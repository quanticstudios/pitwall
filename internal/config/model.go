package config

import (
	"path/filepath"
	"strings"
)

// WithModel returns args with the agent's model option before them. An empty
// selection leaves args alone; "default" removes an explicit model. Existing
// model options are replaced, including those supplied by a preset.
func WithModel(agent string, args []string, selection string) []string {
	selection = strings.TrimSpace(selection)
	if selection == "" {
		return append([]string(nil), args...)
	}
	base := strings.TrimSuffix(filepath.Base(agent), ".exe")
	flag := "--model"
	if base == "codex" {
		flag = "-m"
	}
	var out []string
	if selection != "default" {
		out = append(out, flag, selection)
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			out = append(out, args[i:]...)
			break
		}
		if a == "--model" || base == "codex" && a == "-m" {
			if i+1 < len(args) {
				i++
			}
			continue
		}
		if strings.HasPrefix(a, "--model=") || base == "codex" && strings.HasPrefix(a, "-m=") {
			continue
		}
		out = append(out, a)
	}
	return out
}
