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
		{"  ⎿  Set model to Opus 5.5 (default) for this session only\n────────\n❯ \n────────", "Opus 5.5", ""},
		{"  ⎿  Set model to Sonnet 5.5 for this session only\n  ⎿  Kept model as Opus 5.5 (default)\n────────\n❯ \n────────", "Opus 5.5", ""},
		{"  ⎿  Set effort level to xhigh (this session only): Deeper reasoning than high\n  ⎿  Set model to Haiku 5.5 for this session only with medium effort\n────────\n❯ \n────────", "Haiku 5.5", "medium"},
		{"  ⎿  Set model to Haiku 5.5 for this session only with medium effort\n  ⎿  Set effort level to xhigh (this session only): Deeper reasoning than high\n────────\n❯ \n────────", "Haiku 5.5", "xhigh"},
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
