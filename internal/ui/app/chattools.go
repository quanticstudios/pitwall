package app

import (
	"cmp"
	"log"
	"slices"

	"github.com/quanticstudios/pitwall/internal/config"
)

// nextTools is the [chat] tool_calls mode after cur, in config.ToolModes'
// order, all after zen.
func nextTools(cur string) string {
	i := max(0, slices.Index(config.ToolModes, cur)) // "" is all
	return config.ToolModes[(i+1)%len(config.ToolModes)]
}

// toolsLabel is the chat header's Tools button for mode.
func toolsLabel(mode string) string {
	switch mode {
	case config.ToolsFailed:
		return "Failed only"
	case config.ToolsNone:
		return "No tools"
	case config.ToolsZen:
		return "Zen"
	}
	return "Tools"
}

// toolsTip is the Tools button's tooltip: what shows, and what a click
// shows next.
func toolsTip(mode string) string {
	say := map[string]string{config.ToolsAll: "all", config.ToolsFailed: "failed only", config.ToolsNone: "none", config.ToolsZen: "zen, your messages and final replies"}
	if say[mode] == "" {
		mode = config.ToolsAll
	}
	return "Tool calls: " + say[mode] + ". Click for " + say[nextTools(mode)]
}

// saveTools keeps mode in path's [chat] tool_calls, as the Settings
// control does: all, the default, removes the key.
func saveTools(path, mode string) error {
	if mode == config.ToolsAll {
		return config.RemoveKey(path, "chat", "tool_calls")
	}
	return config.SetKey(path, "chat", "tool_calls", config.Quote(mode))
}

// cycleTools is the header's Tools button: the next mode, shown at once
// and written to config.toml.
func (u *ui) cycleTools() {
	u.cfg.Chat.ToolCalls = nextTools(u.cfg.Chat.ToolCalls)
	if err := saveTools(cmp.Or(u.cfg.Path, config.Path()), u.cfg.Chat.ToolCalls); err != nil {
		log.Printf("tool calls: %q", err)
	}
	if u.invalidate != nil {
		u.invalidate()
	}
}
