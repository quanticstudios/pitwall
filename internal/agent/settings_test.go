package agent

import (
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/vt"
)

func TestClaudeSettings(t *testing.T) {
	for _, tc := range []struct{ text, model, effort string }{
		{"────────\n❯ \n────────\n  Opus 5.5 · high · project · 8% 83k/1.0M", "Opus 5.5 1M", "high"},
		{"────────\n❯ \n────────\n  Sonnet 5.5 · low · project", "Sonnet 5.5", "low"},
		{"▝▜██████▀  Opus 5.5 with high effort · Claude Ultra\n────────\n❯ \n────────", "Opus 5.5", "high"},
		{"▐▛███▜▌  Sonnet 5.5 with low effort · Claude Pro\n────────\n❯ \n────────", "Sonnet 5.5", "low"},
		{"⏺ I'd run the review on Opus 5.5 with max effort and the rest on Sonnet.\n────────\n❯ \n────────\n  ⏵⏵ don't ask on (shift+tab to cycle)", "", ""},
		{"❯ Opus 5.5 with max effort\n────────\n❯ \n────────", "", ""},
		{"  ⎿  Set model to Sonnet 5.5 and saved as your default for new sessions\n  ⎿  Set effort level to low (saved as your default for new sessions):\n────────\n❯ \n────────", "Sonnet 5.5", "low"},
		{"Opus 5.5 · high · an old answer\n────────\n❯ \n────────\n ? for shortcuts", "", ""},
		{"────────\n❯ \n────────\n Opus private text · secret · project", "", ""},
		{"  ⎿  Set model to Opus private text\n  ⎿  Set effort level to secret\n────────\n❯ \n────────", "", ""},
		{"  ⎿  Set model to Opus 5.5 (1M context) and saved as your default for new sessions\n────────\n❯ \n────────", "Opus 5.5 1M", ""},
		{"  ⎿  Set model to Haiku 5.5 for this session only with medium effort\n────────\n❯ \n────────", "Haiku 5.5", "medium"},
		// Claude Code 2.1.329: a space and a no-break space after ⎿.
		{"  ⎿ \u00a0Set model to Sonnet 5.5 for this session only\n  ⎿ \u00a0Set effort level to high (this session only)\n────────\n❯ \n────────", "Sonnet 5.5", "high"},
		{"  ⎿  Set model to Opus 5.5 (default) for this session only\n────────\n❯ \n────────", "Opus 5.5", ""},
		{"  ⎿  Set model to Sonnet 5.5 for this session only\n  ⎿  Kept model as Opus 5.5 (default)\n────────\n❯ \n────────", "Opus 5.5", ""},
		{"  ⎿  Set effort level to xhigh (this session only): Deeper reasoning than high\n  ⎿  Set model to Haiku 5.5 for this session only with medium effort\n────────\n❯ \n────────", "Haiku 5.5", "medium"},
		{"  ⎿  Set model to Haiku 5.5 for this session only with medium effort\n  ⎿  Set effort level to xhigh (this session only): Deeper reasoning than high\n────────\n❯ \n────────", "Haiku 5.5", "xhigh"},
		// Claude Code 2.1.328: the header names the model, the effort sits over the box.
		{" ▐▛███▜▌   Claude Code v2.1.328\n▝▜█████▛▘  Opus 5.5 · Claude Ultra\n  ▘▘ ▝▝    /tmp/repo\n                    ◐ medium · /effort\n────────\n❯ \n────────", "Opus 5.5", "medium"},
		{" ▐▛███▜▌   Claude Code v2.1.328\n▝▜█████▛▘  Opus private text · Claude Ultra\n────────\n❯ \n────────", "", ""},
		{"  ⎿  Set effort level to low (this session only)\n                    ◐ high · /effort\n────────\n❯ \n────────", "", "high"},
	} {
		e := vt.New(100, 10, nil)
		e.Write([]byte(strings.ReplaceAll(tc.text, "\n", "\r\n")))
		m, effort := ClaudeSettings(e.Snapshot())
		if m != tc.model || effort != tc.effort {
			t.Errorf("%q: %q %q, want %q %q", tc.text, m, effort, tc.model, tc.effort)
		}
	}
}

// A narrow pane wraps an acknowledgement onto more rows: the terminal wraps
// it mid-word, Claude Code indents its own wraps past the ⎿. The ack line is
// Claude Code 2.1.322's, from claude-model-menu.txt; the banner above says
// high, which only the wrapped ack corrects.
func TestClaudeSettingsWrapped(t *testing.T) {
	banner := "▝▜██████▀  Opus 5.5 with high effort · Claude Ultra\n\n"
	prompt := "\n────────\n❯ \n────────"
	for _, tc := range []struct {
		cols                  int
		text                  string
		wantModel, wantEffort string
	}{
		{60, banner + "  ⎿  Set model to Haiku 5.5 for this session only with medium effort" + prompt, "Haiku 5.5", "medium"},
		{30, banner + "  ⎿  Set model to Haiku 5.5 for this session only with medium effort" + prompt, "Haiku 5.5", "medium"},
		{60, banner + "  ⎿  Set model to Haiku 5.5 for this session only with\n     medium effort" + prompt, "Haiku 5.5", "medium"},
		{60, banner + "  ⎿  Set model to Haiku\n     5.5 for this session only with medium effort" + prompt, "Haiku 5.5", "medium"},
		{60, banner + "  ⎿  Set effort level to xhigh (this session only): Deeper reasoning than high, just below maximum\n     (on supported models)" + prompt, "Opus 5.5", "xhigh"},
		// The next ⎿ or a row at the left edge is not a wrap.
		{60, banner + "  ⎿  Set model to Haiku 5.5 for this session only with\n  ⎿  medium effort\n❯ max effort" + prompt, "Haiku 5.5", "high"},
	} {
		e := vt.New(tc.cols, 12, nil)
		e.Write([]byte(strings.ReplaceAll(tc.text, "\n", "\r\n")))
		m, effort := ClaudeSettings(e.Snapshot())
		if m != tc.wantModel || effort != tc.wantEffort {
			t.Errorf("%d columns, %q: %q %q, want %q %q", tc.cols, tc.text, m, effort, tc.wantModel, tc.wantEffort)
		}
	}
}

func TestClaudeMode(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"────────\n❯ \n────────\n  ⏵⏵ accept edits on (shift+tab to cycle)", "acceptEdits"},
		{"────────\n❯ \n────────\n  ⏸ plan mode on (shift+tab to cycle)", "plan"},
		{"────────\n❯ \n────────\n  ⏵⏵ bypass permissions on (shift+tab to cycle)", "bypassPermissions"},
		{"────────\n❯ \n────────\n  ⏵⏵ auto mode on (shift+tab to cycle)", "auto"},
		{"⏺ plan mode on is a phrase\n────────\n❯ \n────────\n  ? for shortcuts", "default"},
		{"⏺ Working…", ""},
		{"✻ Working… (12s · esc to interrupt)\n────────\n❯ \n────────\n  Opus 5.5 · high · pitwall main · 16% 159k/1.0M\n  ⏵⏵ bypass permissions on (shift+tab to cycle) · esc to interrupt · ← 1 agent", "bypassPermissions"},
	} {
		e := vt.New(100, 10, nil)
		e.Write([]byte(strings.ReplaceAll(tc.text, "\n", "\r\n")))
		if got := ClaudeMode(e.Snapshot()); got != tc.want {
			t.Errorf("%q: %q, want %q", tc.text, got, tc.want)
		}
	}
}

// Claude Code 2.1.328's slash menu sits right over the input box; a sent
// command's echo higher up, or a box with no menu, is none.
func TestSlashMenu(t *testing.T) {
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"❯ /pwprobe      Probe skill for a pitwall test. (user)\n  /pwprobe-two  Second probe skill. (user)\n────────\n❯ /pwpro\n────────", true},
		{"  /pwprobe-two  Second probe skill. (user)\n❯ /pwprobe      Probe skill for a pitwall test. (user)\n────────\n❯ /pwpro\n────────", true},
		{"❯ /pwprobe\n\n● PROBE-OK\n\n────────\n❯ \n────────", false},
		{"❯ /pwprobe\n────────\n❯ \n────────", false},
		{"────────\n❯ /pwpro\n────────", false},
	} {
		e := vt.New(100, 10, nil)
		e.Write([]byte(strings.ReplaceAll(tc.text, "\n", "\r\n")))
		if got := SlashMenu(e.Snapshot()); got != tc.want {
			t.Errorf("%q: %v, want %v", tc.text, got, tc.want)
		}
	}
}
