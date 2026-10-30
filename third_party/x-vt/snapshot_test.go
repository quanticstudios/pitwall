package vt

import (
	"bytes"
	"encoding/gob"
	"image/color"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func decodedState(t *testing.T, e *Emulator) emulatorState {
	t.Helper()
	data, err := e.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	var s emulatorState
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&s); err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(s.Modes, func(a, b modeState) int {
		if a.ANSI != b.ANSI {
			if a.ANSI {
				return -1
			}
			return 1
		}
		return a.Number - b.Number
	})
	return s
}

func TestSnapshotFullState(t *testing.T) {
	a := NewEmulator(20, 6)
	defer a.Close()
	_, _ = a.WriteString("main 中\x1b[31;4:3;58:2::1:2:3m\x1b]8;id=test;https://example.test\a" +
		"\x1b[5 q\x1b7\x1b[2;5r\x1b[3g\x1b[7G\x1bH\x1b)0\x0e\x1b[?1049hALT" +
		"\x1b[?1;1003;1006;2004h\x1b[?25l\x1b[?6h\x1b[3;5r\x1b[2;20H!")
	a.SetIndexedColor(5, ansi.RGBColor{R: 1, G: 2, B: 3})
	a.SetForegroundColor(color.RGBA64{R: 1234, G: 4321, B: 5678, A: 65535})
	a.SetBackgroundColor(ansi.TrueColor(0x123456))
	a.SetCursorColor(color.NRGBA{R: 4, G: 5, B: 6, A: 255})
	// Also preserve a pending single shift and a CSI with an already-executed
	// newline. Rebuilding the parser must not execute that newline again.
	_, _ = a.WriteString("\x1b*0\x1bN\x1b[2\n;")
	data, err := a.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	b := NewEmulator(1, 1)
	defer b.Close()
	callbacks := 0
	b.SetCallbacks(Callbacks{Title: func(string) { callbacks++ }, EnableMode: func(ansi.Mode) { callbacks++ }})
	if err := b.UnmarshalBinary(data); err != nil {
		t.Fatal(err)
	}
	if callbacks != 0 {
		t.Fatal("restore invoked callbacks")
	}
	if !reflect.DeepEqual(decodedState(t, a), decodedState(t, b)) {
		t.Fatal("backend state changed during handoff")
	}
	for _, next := range []string{"3Hq", "\x1b[?1049l\x1b8X\tY", "\x1b[r\x1b[6;20H!WRAP"} {
		_, _ = a.WriteString(next)
		_, _ = b.WriteString(next)
		if !reflect.DeepEqual(a.Scrollback().Lines(), b.Scrollback().Lines()) ||
			!reflect.DeepEqual(a.Scrollback().Flags(), b.Scrollback().Flags()) {
			t.Fatal("continued scrollback differs")
		}
		a.ClearScrollback()
		b.ClearScrollback()
		if !reflect.DeepEqual(decodedState(t, a), decodedState(t, b)) || a.Render() != b.Render() {
			t.Fatalf("continuation differs after %q", next)
		}
	}
}

func TestSnapshotPendingLimit(t *testing.T) {
	e := NewEmulator(10, 3)
	defer e.Close()
	_, _ = e.WriteString("\x1b_" + strings.Repeat("x", maxPending))
	if _, err := e.MarshalBinary(); err == nil {
		t.Fatal("accepted an unfinished sequence past the retention limit")
	}
	_, _ = e.WriteString("\x1b\\ok")
	if _, err := e.MarshalBinary(); err != nil {
		t.Fatalf("completed sequence did not release retention limit: %v", err)
	}
}

func TestSnapshotRequiresDrainedHistory(t *testing.T) {
	e := NewEmulator(10, 1)
	defer e.Close()
	_, _ = e.WriteString("line\r\n")
	if _, err := e.MarshalBinary(); err == nil {
		t.Fatal("silently lost undrained scrollback")
	}
	e.ClearScrollback()
	if _, err := e.MarshalBinary(); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotValidation(t *testing.T) {
	e := NewEmulator(10, 3)
	defer e.Close()
	s := decodedState(t, e)
	for _, mutate := range []func(*emulatorState){
		func(s *emulatorState) { s.Version++ },
		func(s *emulatorState) { s.Width = 0 },
		func(s *emulatorState) { s.Screens[0].Lines = nil },
		func(s *emulatorState) { s.Screens[1].Flags = nil },
		func(s *emulatorState) { s.Tabs = []int{-1} },
		func(s *emulatorState) { s.GL = 4 },
	} {
		bad := s
		mutate(&bad)
		var buf bytes.Buffer
		if err := gob.NewEncoder(&buf).Encode(bad); err != nil {
			t.Fatal(err)
		}
		if err := e.UnmarshalBinary(buf.Bytes()); err == nil {
			t.Fatal("accepted invalid backend state")
		}
	}
	if err := e.UnmarshalBinary([]byte("corrupt")); err == nil {
		t.Fatal("accepted corrupt backend state")
	}
}
