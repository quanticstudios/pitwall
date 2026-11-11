package agent

import (
	"regexp"
	"strings"

	"github.com/quanticstudios/pitwall/internal/vt"
)

// ClaudeSettings reads Claude Code's status line, command acknowledgements
// and startup banner. Empty values leave the last known setting intact.
func ClaudeSettings(g vt.Grid) (model, effort string) {
	rows, _ := boxRows(g)
	for i := len(rows) - 1; i >= 0; i-- {
		if isRule(rows[i]) {
			break
		}
		parts := strings.Split(strings.TrimSpace(rows[i]), " · ")
		if len(parts) < 2 {
			continue
		}
		if !strings.HasPrefix(parts[0], "Opus ") && !strings.HasPrefix(parts[0], "Sonnet ") &&
			!strings.HasPrefix(parts[0], "Haiku ") && !strings.HasPrefix(parts[0], "Fable ") {
			continue
		}
		model = parts[0]
		if strings.HasPrefix(model, "Opus ") && strings.Contains(rows[i], "/1.0M") && !strings.HasSuffix(model, " 1M") {
			model += " 1M"
		}
		switch parts[1] {
		case "low", "medium", "high", "xhigh", "max":
			effort = parts[1]
		}
		return cleanClaudeSettings(model, effort)
	}
	for i := len(rows) - 1; i >= 0; i-- {
		row := strings.TrimSpace(rows[i])
		if model == "" && strings.HasPrefix(row, "⎿  Set model to ") {
			var tail string
			model, _, _ = strings.Cut(strings.TrimPrefix(row, "⎿  Set model to "), " and saved")
			model, tail, _ = strings.Cut(model, " for this session only")
			model = strings.TrimSuffix(model, " (default)")
			if w := strings.Fields(tail); effort == "" && len(w) == 3 && w[0] == "with" && w[2] == "effort" {
				effort = w[1]
			}
		}
		if model == "" && strings.HasPrefix(row, "⎿  Kept model as ") {
			model = strings.TrimSuffix(strings.TrimPrefix(row, "⎿  Kept model as "), " (default)")
		}
		if effort == "" && strings.HasPrefix(row, "⎿  Set effort level to ") {
			effort = strings.Fields(strings.TrimPrefix(row, "⎿  Set effort level to "))[0]
		}
		if !strings.HasPrefix(row, "▝▜") && !strings.HasPrefix(row, "▐▛") {
			continue
		}
		if name, tail, ok := strings.Cut(row, " with "); ok {
			words := strings.Fields(tail)
			if len(words) < 2 || words[1] != "effort" {
				continue
			}
			for _, family := range []string{"Opus ", "Sonnet ", "Haiku ", "Fable "} {
				if at := strings.Index(name, family); at >= 0 {
					if model == "" {
						model = name[at:]
					}
					if effort == "" {
						effort = words[0]
					}
					break
				}
			}
		}
	}
	return cleanClaudeSettings(model, effort)
}

var claudeSettingModel = regexp.MustCompile(`^(Opus|Sonnet|Haiku|Fable) [0-9]+(\.[0-9]+){0,2}( 1M)?$`)

// Only setting values leave the terminal; other text stays on the computer.
func cleanClaudeSettings(model, effort string) (string, string) {
	model = strings.TrimSpace(strings.ReplaceAll(model, "(1M context)", "1M"))
	if !claudeSettingModel.MatchString(model) {
		model = ""
	}
	switch effort {
	case "low", "medium", "high", "xhigh", "max":
	default:
		effort = ""
	}
	return model, effort
}
