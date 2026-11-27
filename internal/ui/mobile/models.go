package mobile

import (
	"slices"
	"strings"

	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/ui/chatin"
)

// modelName reads the family and version from an id, leaving unknown ids intact.
func modelName(id string) string { return chatin.ModelName(id) }

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

// effortShown is the effort chip's level: a pick settle keeps, else the
// session's.
func (k *composer) effortShown(current string) string {
	if k.effort != "" {
		return k.effort
	}
	return current
}

// modeShown is the mode chip's mode: a pick settle keeps, else the
// session's.
func (k *composer) modeShown(current string) string {
	if k.mode != "" {
		return k.mode
	}
	return current
}

// modeShort is the mode chip's word for m, short enough for a phone's
// toolbar; the menu names it in full.
func modeShort(m string) string {
	switch m {
	case "":
		return "Mode"
	case "acceptEdits", "accept-edits":
		return "Edits"
	case "bypassPermissions":
		return "Bypass"
	}
	return chatin.ModeName(m)
}

// chipLists are the models, efforts and modes t's chips offer, nil for a
// chip it does not show: the tab's own lists, or, from a daemon older than
// them, the hello's models and efforts for a Claude Code tab.
func chipLists(t remote.Tab, models, efforts []string) [3][]string {
	if t.Models == nil && t.Efforts == nil && t.Picker {
		return [3][]string{models, efforts, t.Modes}
	}
	return [3][]string{t.Models, t.Efforts, t.Modes}
}
