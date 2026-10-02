package input

import (
	"fmt"
	"testing"

	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"github.com/quanticstudios/pitwall/internal/vt"
)

func TestLegacyKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  key.Name
		mods key.Modifiers
		app  bool
		want string
	}{
		{"return", key.NameReturn, 0, false, "\r"}, {"enter", key.NameEnter, 0, false, "\r"},
		{"backspace", key.NameDeleteBackward, 0, false, "\x7f"}, {"tab", key.NameTab, 0, false, "\t"},
		{"shift tab", key.NameTab, key.ModShift, false, "\x1b[Z"}, {"alt tab", key.NameTab, key.ModAlt, false, "\x1b\t"},
		{"escape", key.NameEscape, 0, false, "\x1b"}, {"alt escape", key.NameEscape, key.ModAlt, false, "\x1b\x1b"},
		{"plain text uses EditEvent", "A", 0, false, ""}, {"shift text uses EditEvent", "A", key.ModShift, false, ""},
		{"alt letter", "A", key.ModAlt, false, "\x1ba"}, {"alt shift letter", "A", key.ModAlt | key.ModShift, false, "\x1bA"},
		{"alt unicode", "Ж", key.ModAlt, false, "\x1bж"}, {"alt ctrl", "C", key.ModAlt | key.ModCtrl, false, "\x1b\x03"},
		{"space control", key.NameSpace, key.ModCtrl, false, "\x00"}, {"alt space", key.NameSpace, key.ModAlt, false, "\x1b "},
		{"unknown", "Unknown", key.ModAlt, false, ""}, {"bare ctrl", key.NameCtrl, key.ModCtrl, false, ""},
		{"super text", "A", key.ModSuper, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Key(key.Event{Name: tc.key, Modifiers: tc.mods}, vt.Modes{AppCursorKeys: tc.app})
			if string(got) != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	for r := 'A'; r <= 'Z'; r++ {
		if got := Key(key.Event{Name: key.Name(string(r)), Modifiers: key.ModCtrl}, vt.Modes{}); string(got) != string(r-'A'+1) {
			t.Errorf("Ctrl+%c: %q", r, got)
		}
	}
	for i, r := range []rune{'[', '\\', ']', '^', '_'} {
		if got := Key(key.Event{Name: key.Name(string(r)), Modifiers: key.ModCtrl}, vt.Modes{}); string(got) != string(rune(27+i)) {
			t.Errorf("Ctrl+%c: %q", r, got)
		}
	}
	if got := Key(key.Event{Name: key.NameReturn, State: key.Release}, vt.Modes{}); got != nil {
		t.Fatalf("release: %q", got)
	}
}

func TestLegacyFunctionalKeys(t *testing.T) {
	for _, tc := range []struct {
		name                 key.Name
		plain, app, modified string
	}{
		{key.NameUpArrow, "\x1b[A", "\x1bOA", "\x1b[1;6A"},
		{key.NameDownArrow, "\x1b[B", "\x1bOB", "\x1b[1;6B"},
		{key.NameRightArrow, "\x1b[C", "\x1bOC", "\x1b[1;6C"},
		{key.NameLeftArrow, "\x1b[D", "\x1bOD", "\x1b[1;6D"},
		{key.NameHome, "\x1b[H", "\x1bOH", "\x1b[1;6H"},
		{key.NameEnd, "\x1b[F", "\x1bOF", "\x1b[1;6F"},
		{"Insert", "\x1b[2~", "\x1b[2~", "\x1b[2;6~"},
		{key.NameDeleteForward, "\x1b[3~", "\x1b[3~", "\x1b[3;6~"},
		{key.NamePageUp, "\x1b[5~", "\x1b[5~", "\x1b[5;6~"},
		{key.NamePageDown, "\x1b[6~", "\x1b[6~", "\x1b[6;6~"},
		{key.NameF1, "\x1bOP", "\x1bOP", "\x1b[1;6P"},
		{key.NameF2, "\x1bOQ", "\x1bOQ", "\x1b[1;6Q"},
		{key.NameF3, "\x1bOR", "\x1bOR", "\x1b[1;6R"},
		{key.NameF4, "\x1bOS", "\x1bOS", "\x1b[1;6S"},
		{key.NameF5, "\x1b[15~", "\x1b[15~", "\x1b[15;6~"},
		{key.NameF6, "\x1b[17~", "\x1b[17~", "\x1b[17;6~"},
		{key.NameF7, "\x1b[18~", "\x1b[18~", "\x1b[18;6~"},
		{key.NameF8, "\x1b[19~", "\x1b[19~", "\x1b[19;6~"},
		{key.NameF9, "\x1b[20~", "\x1b[20~", "\x1b[20;6~"},
		{key.NameF10, "\x1b[21~", "\x1b[21~", "\x1b[21;6~"},
		{key.NameF11, "\x1b[23~", "\x1b[23~", "\x1b[23;6~"},
		{key.NameF12, "\x1b[24~", "\x1b[24~", "\x1b[24;6~"},
	} {
		t.Run(string(tc.name), func(t *testing.T) {
			for _, mode := range []struct {
				app  bool
				mods key.Modifiers
				want string
			}{
				{false, 0, tc.plain}, {true, 0, tc.app}, {false, key.ModCtrl | key.ModShift, tc.modified}, {true, key.ModCtrl | key.ModShift, tc.modified},
			} {
				if got := Key(key.Event{Name: tc.name, Modifiers: mode.mods}, vt.Modes{AppCursorKeys: mode.app}); string(got) != mode.want {
					t.Errorf("got %q, want %q", got, mode.want)
				}
			}
		})
	}
	for mod := 0; mod < 8; mod++ {
		var mods key.Modifiers
		if mod&1 != 0 {
			mods |= key.ModShift
		}
		if mod&2 != 0 {
			mods |= key.ModAlt
		}
		if mod&4 != 0 {
			mods |= key.ModCtrl
		}
		want := "\x1b[A"
		if mod != 0 {
			want = fmt.Sprintf("\x1b[1;%dA", 1+mod)
		}
		if got := Key(key.Event{Name: key.NameUpArrow, Modifiers: mods}, vt.Modes{}); string(got) != want {
			t.Errorf("mods %d: %q, want %q", mod, got, want)
		}
	}
}

func TestKittyKey(t *testing.T) {
	for _, tc := range []struct {
		name  string
		key   key.Name
		mods  key.Modifiers
		flags uint8
		state key.State
		want  string
	}{
		{"disambiguate ctrl", "C", key.ModCtrl, 1, key.Press, "\x1b[99;5u"},
		{"disambiguate shift ctrl", "C", key.ModCtrl | key.ModShift, 1, key.Press, "\x1b[99;6u"},
		{"escape", key.NameEscape, 0, 1, key.Press, "\x1b[27;1u"},
		{"text uses EditEvent", "A", 0, 1, key.Press, ""},
		{"shift text uses EditEvent", "A", key.ModShift, 1, key.Press, ""},
		{"return compatibility", key.NameReturn, 0, 1, key.Press, "\r"},
		{"tab compatibility", key.NameTab, 0, 1, key.Press, "\t"},
		{"backspace compatibility", key.NameDeleteBackward, 0, 1, key.Press, "\x7f"},
		{"modified return", key.NameReturn, key.ModCtrl, 1, key.Press, "\x1b[13;5u"},
		{"all return", key.NameReturn, 0, 8, key.Press, "\x1b[13;1u"},
		{"keypad enter", key.NameEnter, 0, 1, key.Press, "\x1b[57414;1u"},
		{"all tab", key.NameTab, 0, 8, key.Press, "\x1b[9;1u"},
		{"all backspace", key.NameDeleteBackward, 0, 8, key.Press, "\x1b[127;1u"},
		{"all plain", "A", 0, 8, key.Press, "\x1b[97;1u"},
		{"unicode", "Ж", key.ModCtrl, 1, key.Press, "\x1b[1078;5u"},
		{"alternate", "A", key.ModShift | key.ModCtrl, 5, key.Press, "\x1b[97:65;6u"},
		{"associated", "A", key.ModShift, 24, key.Press, "\x1b[97;2;65u"},
		{"associated space", key.NameSpace, 0, 24, key.Press, "\x1b[32;1;32u"},
		{"associated control has no text", "A", key.ModCtrl, 24, key.Press, "\x1b[97;5u"},
		{"all flags", "Ж", key.ModShift, 31, key.Press, "\x1b[1078:1046;2:1;1046u"},
		{"super", "A", key.ModSuper, 1, key.Press, "\x1b[97;9u"},
		{"command", "A", key.ModCommand, 1, key.Press, "\x1b[97;9u"},
		{"bare modifier", key.NameShift, key.ModShift, 31, key.Press, ""},
		{"release off", "A", key.ModCtrl, 1, key.Release, ""},
		{"release ctrl", "A", key.ModCtrl, 3, key.Release, "\x1b[97;5:3u"},
		{"release text off", "A", 0, 3, key.Release, ""},
		{"release all", "A", 0, 10, key.Release, "\x1b[97;1:3u"},
		{"release no associated text", "A", key.ModShift, 31, key.Release, "\x1b[97:65;2:3u"},
		{"return release compatibility", key.NameReturn, 0, 3, key.Release, ""},
		{"return release all", key.NameReturn, 0, 10, key.Release, "\x1b[13;1:3u"},
		{"arrow", key.NameUpArrow, 0, 1, key.Press, "\x1b[1;1A"},
		{"arrow release", key.NameLeftArrow, key.ModAlt, 3, key.Release, "\x1b[1;3:3D"},
		{"F3", key.NameF3, 0, 1, key.Press, "\x1b[13;1~"},
		{"F12 release", key.NameF12, key.ModShift, 3, key.Release, "\x1b[24;2:3~"},
		{"alternate flag alone", "A", key.ModCtrl, 4, key.Press, "\x01"},
		{"unknown", "Unknown", 0, 31, key.Press, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Key(key.Event{Name: tc.key, Modifiers: tc.mods, State: tc.state}, vt.Modes{KittyKeyboard: tc.flags}); string(got) != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTextAndPaste(t *testing.T) {
	for _, s := range []string{"", "a\n", "ž😀", "\x1b[31m"} {
		if got := Text(s); string(got) != s {
			t.Fatalf("Text(%q) = %q", s, got)
		}
	}
	for _, tc := range []struct {
		s       string
		bracket bool
		want    string
	}{
		{"ž😀", false, "ž😀"}, {"a\x1bb\x1b[201~c", false, "ab[201~c"},
		{"a\x1bb\x1b[201~c", true, "\x1b[200~ab[201~c\x1b[201~"},
		{"", true, "\x1b[200~\x1b[201~"},
	} {
		if got := Paste(tc.s, vt.Modes{BracketedPaste: tc.bracket}); string(got) != tc.want {
			t.Fatalf("Paste(%q) = %q, want %q", tc.s, got, tc.want)
		}
	}
}

func TestMouse(t *testing.T) {
	for _, tc := range []struct {
		name     string
		kind     pointer.Kind
		buttons  pointer.Buttons
		scroll   f32.Point
		mods     key.Modifiers
		mode     vt.MouseMode
		sgr      bool
		col, row int
		want     string
	}{
		{"off", pointer.Press, pointer.ButtonPrimary, f32.Point{}, 0, vt.MouseOff, true, 0, 0, ""},
		{"left", pointer.Press, pointer.ButtonPrimary, f32.Point{}, 0, vt.MouseNormal, true, 2, 3, "\x1b[<0;3;4M"},
		{"middle", pointer.Press, pointer.ButtonTertiary, f32.Point{}, 0, vt.MouseNormal, true, 0, 0, "\x1b[<1;1;1M"},
		{"right", pointer.Press, pointer.ButtonSecondary, f32.Point{}, 0, vt.MouseNormal, true, 0, 0, "\x1b[<2;1;1M"},
		{"modifiers", pointer.Press, pointer.ButtonPrimary, f32.Point{}, key.ModShift | key.ModAlt | key.ModCtrl, vt.MouseNormal, true, 0, 0, "\x1b[<28;1;1M"},
		{"release", pointer.Release, 0, f32.Point{}, 0, vt.MouseNormal, true, 2, 3, "\x1b[<0;3;4m"},
		{"normal no motion", pointer.Move, pointer.ButtonPrimary, f32.Point{}, 0, vt.MouseNormal, true, 0, 0, ""},
		{"button no hover", pointer.Move, 0, f32.Point{}, 0, vt.MouseButton, true, 0, 0, ""},
		{"button drag", pointer.Drag, pointer.ButtonSecondary, f32.Point{}, key.ModCtrl, vt.MouseButton, true, 0, 0, "\x1b[<50;1;1M"},
		{"any hover", pointer.Move, 0, f32.Point{}, 0, vt.MouseAny, true, 0, 0, "\x1b[<35;1;1M"},
		{"any drag", pointer.Drag, pointer.ButtonTertiary, f32.Point{}, 0, vt.MouseAny, true, 0, 0, "\x1b[<33;1;1M"},
		{"wheel up", pointer.Scroll, 0, f32.Point{Y: -10}, 0, vt.MouseNormal, true, 0, 0, "\x1b[<64;1;1M"},
		{"wheel down", pointer.Scroll, 0, f32.Point{Y: 10}, key.ModShift, vt.MouseNormal, true, 0, 0, "\x1b[<69;1;1M"},
		{"wheel left", pointer.Scroll, 0, f32.Point{X: -5}, 0, vt.MouseButton, true, 0, 0, "\x1b[<66;1;1M"},
		{"wheel right", pointer.Scroll, 0, f32.Point{X: 5}, 0, vt.MouseAny, true, 0, 0, "\x1b[<67;1;1M"},
		{"two axes", pointer.Scroll, 0, f32.Point{X: 5, Y: -5}, 0, vt.MouseAny, true, 0, 0, "\x1b[<64;1;1M\x1b[<67;1;1M"},
		{"zero scroll", pointer.Scroll, 0, f32.Point{}, 0, vt.MouseNormal, true, 0, 0, ""},
		{"x10 press", pointer.Press, pointer.ButtonPrimary, f32.Point{}, key.ModCtrl, vt.MouseX10, false, 2, 3, "\x1b[M #$"},
		{"x10 no release", pointer.Release, 0, f32.Point{}, 0, vt.MouseX10, false, 0, 0, ""},
		{"x10 no wheel", pointer.Scroll, 0, f32.Point{Y: 1}, 0, vt.MouseX10, false, 0, 0, ""},
		{"classic release", pointer.Release, 0, f32.Point{}, key.ModCtrl, vt.MouseNormal, false, 0, 0, "\x1b[M3!!"},
		{"classic motion", pointer.Move, 0, f32.Point{}, 0, vt.MouseAny, false, 0, 0, "\x1b[MC!!"},
		{"classic wheel", pointer.Scroll, 0, f32.Point{Y: -1}, 0, vt.MouseNormal, false, 0, 0, "\x1b[M`!!"},
		{"classic edge", pointer.Press, pointer.ButtonPrimary, f32.Point{}, 0, vt.MouseNormal, false, 222, 222, "\x1b[M \xff\xff"},
		{"classic overflow", pointer.Press, pointer.ButtonPrimary, f32.Point{}, 0, vt.MouseNormal, false, 223, 0, ""},
		{"sgr large", pointer.Press, pointer.ButtonPrimary, f32.Point{}, 0, vt.MouseNormal, true, 999, 1000, "\x1b[<0;1000;1001M"},
		{"negative", pointer.Press, pointer.ButtonPrimary, f32.Point{}, 0, vt.MouseNormal, true, -1, 0, ""},
		{"cancel", pointer.Cancel, 0, f32.Point{}, 0, vt.MouseNormal, true, 0, 0, ""},
		{"no button", pointer.Press, 0, f32.Point{}, 0, vt.MouseNormal, true, 0, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Mouse(pointer.Event{Kind: tc.kind, Buttons: tc.buttons, Scroll: tc.scroll, Modifiers: tc.mods}, tc.col, tc.row, vt.Modes{Mouse: tc.mode, MouseSGR: tc.sgr})
			if string(got) != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	if got := Mouse(pointer.Event{Kind: pointer.Press, Source: pointer.Touch, Buttons: pointer.ButtonPrimary}, 0, 0, vt.Modes{Mouse: vt.MouseNormal, MouseSGR: true}); got != nil {
		t.Fatalf("touch: %q", got)
	}
}
