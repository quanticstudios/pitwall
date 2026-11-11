package agent

import (
	"slices"
	"testing"
)

// claude-model-menu.txt and claude-effort-menu.txt are Claude Code 2.1.322's
// /model picker and /effort slider in a 100x30 PTY.
func TestModelMenu(t *testing.T) {
	rows, cursor, ok := ModelMenu(screenFixture(t, "claude-model-menu.txt"))
	want := []MenuRow{{1, "Default (recommended)"}, {2, "Opus"}, {3, "Fable"}, {4, "Sonnet"}, {5, "Haiku"}}
	if !ok || cursor != 5 || !slices.Equal(rows, want) {
		t.Fatalf("picker: %v %d %v", rows, cursor, ok)
	}
	for name, row := range map[string]int{"default": 1, "opus": 2, "fable": 3, "sonnet": 4, "haiku": 5, "opus[1m]": -1, "gpt": -1} {
		if got := ModelRow(rows, name); got != row {
			t.Errorf("%s: row %d, want %d", name, got, row)
		}
	}
	if got := ModelRow([]MenuRow{{1, "Opus 5.5"}, {2, "Opus 5.5 (1M context)"}}, "opus[1m]"); got != 2 {
		t.Errorf("opus[1m] in a picker with a 1M row: %d", got)
	}
	// In a short pane the picker scrolls and shows two of its rows.
	g := screenFixture(t, "claude-model-menu-scrolled.txt")
	rows, cursor, ok = ModelMenu(g)
	if want := []MenuRow{{3, "Fable 5.2"}, {4, "Sonnet 5.5"}}; !ok || cursor != 4 || !slices.Equal(rows, want) {
		t.Fatalf("scrolled picker: %v %d %v", rows, cursor, ok)
	}
	if ModelRow(rows, "sonnet") != 4 || ModelRow(rows, "haiku") != -1 || !SessionMenuUp(g) {
		t.Error("scrolled picker rows")
	}
	for _, file := range []string{"claude-idle.txt", "claude-permission.txt", "claude-effort-menu.txt"} {
		if _, _, ok := ModelMenu(screenFixture(t, file)); ok {
			t.Errorf("%s read as the /model picker", file)
		}
	}
	if SessionMenuUp(screenFixture(t, "claude-idle.txt")) || !SessionMenuUp(screenFixture(t, "claude-effort-menu.txt")) {
		t.Error("SessionMenuUp")
	}
}

func TestEffortMenu(t *testing.T) {
	levels, at, ok := EffortMenu(screenFixture(t, "claude-effort-menu.txt"))
	if !ok || at != 3 || !slices.Equal(levels, effortLevels) {
		t.Fatalf("slider: %q %d %v", levels, at, ok)
	}
	slider := func(marker int) []string {
		line := []rune("                   ─────────────────────────────────────────")
		line[marker] = '▲'
		return []string{"────────", "  Effort", "", string(line),
			"                   low     medium     high     xhigh      max      Tab to toggle", "",
			"  ←/→ to adjust · Enter to confirm · s for this session only · Esc to cancel"}
	}
	for marker, want := range map[int]int{20: 0, 29: 1, 39: 2, 49: 3, 58: 4} {
		if _, at, ok := EffortMenu(gridOf(slider(marker)...)); !ok || at != want {
			t.Errorf("marker at %d: level %d %v, want %d", marker, at, ok, want)
		}
	}
	for _, file := range []string{"claude-idle.txt", "claude-model-menu.txt"} {
		if _, _, ok := EffortMenu(screenFixture(t, file)); ok {
			t.Errorf("%s read as the /effort slider", file)
		}
	}
}
