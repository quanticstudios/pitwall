package input

import (
	"bytes"
	"testing"

	"github.com/quanticstudios/pitwall/internal/vt"
)

func TestEncodeKey(t *testing.T) {
	cases := []struct {
		name  string
		spec  string
		modes vt.Modes
		want  []byte
	}{
		{"enter legacy", "enter", vt.Modes{}, []byte("\r")},
		{"return alias", "Return", vt.Modes{}, []byte("\r")},
		{"esc legacy", "esc", vt.Modes{}, []byte("\x1b")},
		{"escape alias", "escape", vt.Modes{}, []byte("\x1b")},
		{"tab legacy", "tab", vt.Modes{}, []byte("\t")},
		{"shift+tab legacy", "shift+tab", vt.Modes{}, []byte("\x1b[Z")},
		{"btab alias", "btab", vt.Modes{}, []byte("\x1b[Z")},
		{"up normal", "up", vt.Modes{}, []byte("\x1b[A")},
		{"down normal", "down", vt.Modes{}, []byte("\x1b[B")},
		{"up app cursor", "up", vt.Modes{AppCursorKeys: true}, []byte("\x1bOA")},
		{"down app cursor", "down", vt.Modes{AppCursorKeys: true}, []byte("\x1bOB")},
		{"ctrl+c legacy", "ctrl+c", vt.Modes{}, []byte{0x03}},
		{"C-c alias", "C-c", vt.Modes{}, []byte{0x03}},
		{"alt+x legacy", "alt+x", vt.Modes{}, []byte("\x1bx")},
		{"digit", "1", vt.Modes{}, []byte("1")},
		{"letter lower", "y", vt.Modes{}, []byte("y")},
		{"letter upper", "Y", vt.Modes{}, []byte("Y")},
		{"shift+letter", "shift+y", vt.Modes{}, []byte("Y")},
		{"space", "space", vt.Modes{}, []byte(" ")},
		{"literal plus", "+", vt.Modes{}, []byte("+")},
		{"literal minus", "-", vt.Modes{}, []byte("-")},
		{"ctrl++", "ctrl++", vt.Modes{KittyKeyboard: 1}, []byte("\x1b[43;5u")},
		{"backspace", "backspace", vt.Modes{}, []byte{0x7f}},
		{"ctrl+backspace", "ctrl+backspace", vt.Modes{}, []byte{0x17}},
		{"kitty esc", "esc", vt.Modes{KittyKeyboard: 1}, []byte("\x1b[27;1u")},
		{"kitty shift+tab", "shift+tab", vt.Modes{KittyKeyboard: 1}, []byte("\x1b[9;2u")},
		{"kitty shift+enter", "shift+enter", vt.Modes{KittyKeyboard: 1}, []byte("\x1b[13;2u")},
		{"kitty ctrl+c", "ctrl+c", vt.Modes{KittyKeyboard: 1}, []byte("\x1b[99;5u")},
		{"kitty plain enter stays CR without flag 8", "enter", vt.Modes{KittyKeyboard: 1}, []byte("\r")},
		{"kitty report-all letter", "a", vt.Modes{KittyKeyboard: 8}, []byte("\x1b[97;1u")},
		{"kitty press+release", "esc", vt.Modes{KittyKeyboard: 3}, []byte("\x1b[27;1:1u\x1b[27;1:3u")},
		{"f1 legacy", "f1", vt.Modes{}, []byte("\x1bOP")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EncodeKey(tc.spec, tc.modes)
			if err != nil {
				t.Fatalf("EncodeKey(%q): %v", tc.spec, err)
			}
			if !bytes.Equal(got, tc.want) {
				t.Fatalf("EncodeKey(%q) = %q, want %q", tc.spec, got, tc.want)
			}
		})
	}
}

func TestParseKeyErrors(t *testing.T) {
	for _, bad := range []string{"", "ctrl+", "notakey", "foo+a", "\x01"} {
		if _, _, err := ParseKey(bad); err == nil {
			t.Errorf("ParseKey(%q) succeeded, want error", bad)
		}
	}
}
