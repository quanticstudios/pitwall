package mobile

import (
	"regexp"
	"slices"
	"strings"

	"github.com/quanticstudios/pitwall/internal/remote"
)

// modelAliases names Claude Code's moving family aliases.
var modelAliases = map[string]string{
	"default": "Default",
	"fable":   "Fable 5.2",
	"opus":    "Opus 5.5",
	"sonnet":  "Sonnet 5.5",
	"haiku":   "Haiku 5.5",
}

var modelVersion = regexp.MustCompile(`^([a-z]+)-([0-9]+)(?:[.-]([0-9]+))?(?:-([a-z0-9-]+))?$`)

// modelName reads the family and version from an id, leaving unknown ids intact.
func modelName(id string) string {
	wide := ""
	if strings.HasSuffix(strings.ToLower(id), "[1m]") {
		id, wide = id[:len(id)-4], " 1M"
	}
	if name, ok := modelAliases[strings.ToLower(id)]; ok {
		return name + wide
	}
	short := id
	if strings.HasPrefix(strings.ToLower(short), "claude-") {
		short = short[len("claude-"):]
	}
	m := modelVersion.FindStringSubmatch(strings.ToLower(short))
	if m == nil {
		return short + wide
	}
	version := m[2]
	if m[3] != "" && len(m[3]) != 8 { // a date suffix is not a minor version
		version += "." + m[3]
	}
	if m[1] == "gpt" {
		name := "GPT-" + version
		for _, word := range strings.Split(m[4], "-") {
			if word != "" {
				name += " " + strings.ToUpper(word[:1]) + word[1:]
			}
		}
		return name + wide
	}
	return strings.ToUpper(m[1][:1]) + m[1][1:] + " " + version + wide
}

// modelNote is the line under a pick in the model menu.
func modelNote(pick, current string) string {
	switch {
	case pick == "default":
		return "what Claude Code starts with"
	case current != "" && modelName(pick) == modelName(current):
		return "now"
	case strings.HasSuffix(pick, "[1m]"):
		return "1M context"
	}
	return ""
}

// efforts are the effort levels, lowest first, for the five-bar glyph.
var efforts = []string{"low", "medium", "high", "xhigh", "max"}

// effortLevel is how many of the glyph's five bars e lights: 0 for none
// known.
func effortLevel(e string) int { return slices.Index(efforts, e) + 1 }

func (c *chatView) currentModel(t remote.Tab) string {
	if t.Model != "" {
		return t.Model
	}
	return c.data.Model
}

func (k *composer) effortShown(current string) string {
	if k.effort != "" && current == k.effortAt {
		return k.effort
	}
	if current != "" {
		return current
	}
	return k.effort
}
