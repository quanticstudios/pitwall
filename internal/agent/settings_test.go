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
		{"  ⎿  Set model to Sonnet 5.5 and saved as your default for new sessions\n  ⎿  Set effort level to low (saved as your default for new sessions):\n────────\n❯ \n────────", "Sonnet 5.5", "low"},
		{"Opus 5.5 · high · an old answer\n────────\n❯ \n────────\n ? for shortcuts", "", ""},
		{"────────\n❯ \n────────\n Opus private text · secret · project", "", ""},
		{"  ⎿  Set model to Opus private text\n  ⎿  Set effort level to secret\n────────\n❯ \n────────", "", ""},
		{"  ⎿  Set model to Opus 5.5 (1M context) and saved as your default for new sessions\n────────\n❯ \n────────", "Opus 5.5 1M", ""},
	} {
		e := vt.New(100, 10, nil)
		e.Write([]byte(strings.ReplaceAll(tc.text, "\n", "\r\n")))
		m, effort := ClaudeSettings(e.Snapshot())
		if m != tc.model || effort != tc.effort {
			t.Errorf("%q: %q %q, want %q %q", tc.text, m, effort, tc.model, tc.effort)
		}
	}
}
