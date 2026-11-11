package mobile

import (
	"slices"
	"strings"

	"github.com/quanticstudios/pitwall/internal/remote"
)

// modelNames names the models a turn reports and the picks the model menu
// offers (remote.Models): the name a person reads, never the id.
var modelNames = map[string]string{
	"default":  "Default",
	"fable":    "Fable 5.1",
	"opus":     "Opus 5.5",
	"opus[1m]": "Opus 5.5 1M",
	"sonnet":   "Sonnet 5.5",
	"haiku":    "Haiku 5.5",

	"claude-fable-5-1":    "Fable 5.1",
	"claude-opus-5-5":     "Opus 5.5",
	"claude-opus-5-5[1m]": "Opus 5.5 1M",
	"claude-sonnet-5-5":   "Sonnet 5.5",
	"claude-haiku-5-5":    "Haiku 5.5",
	"gpt-6.1-sol":         "GPT-6.1 Sol",
	"gpt-6-1-sol":         "GPT-6.1 Sol",
}

// modelName is id's name. An id the table lacks shows as it is, less a
// "claude-" prefix.
func modelName(id string) string {
	if n, ok := modelNames[strings.ToLower(id)]; ok {
		return n
	}
	return strings.TrimPrefix(id, "claude-")
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
