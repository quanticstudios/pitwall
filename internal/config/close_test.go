package config

import (
	"strings"
	"testing"
)

func TestConfirmClose(t *testing.T) {
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"", true},
		{"[appearance]\nconfirm_close = true\n", true},
		{"[appearance]\nconfirm_close = false\n", false},
	} {
		s, problems := LoadFile(write(t, t.TempDir(), "config.toml", tc.text))
		if len(problems) != 0 || s.ConfirmClose != tc.want {
			t.Errorf("%q: confirm close %v, problems %v", tc.text, s.ConfirmClose, problems)
		}
	}
	if !strings.Contains(Default(), "# confirm_close = true") || !strings.Contains(string(Schema()), `"confirm_close"`) {
		t.Fatal("confirm_close missing from default config or schema")
	}
}
