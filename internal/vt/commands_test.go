package vt

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func recordedCommands(t *testing.T) *emulator {
	t.Helper()
	raw, err := os.ReadFile("testdata/commands.raw")
	if err != nil {
		t.Fatal(err)
	}
	e := New(40, 3, nil).(*emulator)
	t.Cleanup(func() { _ = e.Close() })
	// PTY reads can split any escape sequence or UTF-8 character.
	for _, c := range raw {
		feed(e, string([]byte{c}))
	}
	return e
}

func TestRecordedCommands(t *testing.T) {
	before := time.Now()
	e := recordedCommands(t)
	blocks, integrated := e.Commands()
	if !integrated || len(blocks) != 3 {
		t.Fatalf("integrated %v, blocks %+v", integrated, blocks)
	}
	for i, want := range []struct {
		command, output string
		code            int
	}{
		{"printf hello", "hello\n", 0}, {"true", "", 0}, {"false", "failed\n", 1},
	} {
		b := blocks[i]
		if b.Command != want.command || b.Output != want.output || b.ExitCode != want.code || !b.Finished || !b.StatusKnown {
			t.Errorf("block %d: %+v", i, b)
		}
		if b.Started.Before(before) || b.Started.After(time.Now()) {
			t.Errorf("start %v", b.Started)
		}
	}
	for _, size := range [][2]int{{12, 5}, {60, 2}, {7, 4}, {40, 3}} {
		e.Resize(size[0], size[1])
		got, _ := e.Commands()
		for i, b := range got {
			if b.Command != blocks[i].Command || b.Output != blocks[i].Output || b.Started != blocks[i].Started {
				t.Fatalf("resize %v: %+v", size, got)
			}
		}
		if len(got) != len(blocks) {
			t.Fatalf("resize %v: %+v", size, got)
		}
	}
	data, err := Save(e)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(data, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.(*emulator).Close()
	got, ok := restored.(*emulator).Commands()
	for i := range got {
		got[i].Started = got[i].Started.Round(0).UTC()
		blocks[i].Started = blocks[i].Started.Round(0).UTC()
	}
	if !ok || !reflect.DeepEqual(got, blocks) {
		t.Fatalf("restored %+v", got)
	}
}

func TestCommandCopy(t *testing.T) {
	blocks, _ := recordedCommands(t).Commands()
	for _, tc := range []struct {
		i          int
		part, want string
	}{
		{0, "command", "printf hello"}, {0, "output", "hello\n"}, {0, "both", "printf hello\nhello\n"},
		{1, "output", ""}, {1, "both", "true"}, {2, "both", "false\nfailed\n"}, {0, "invalid", ""},
	} {
		if got := blocks[tc.i].Copy(tc.part); got != tc.want {
			t.Errorf("copy %d %s = %q, want %q", tc.i, tc.part, got, tc.want)
		}
	}
}

func TestCommandsRequireMarks(t *testing.T) {
	e := New(40, 3, nil).(*emulator)
	defer e.Close()
	feed(e, "$ echo guessed\r\nguessed\r\n$ ")
	if b, ok := e.Commands(); ok || len(b) > 0 || e.HasPromptMarks() {
		t.Fatalf("guessed %+v", b)
	}
	feed(e, "\x1b]133;A\x07$ ")
	if b, ok := e.Commands(); !ok || len(b) > 0 || !e.HasPromptMarks() {
		t.Fatalf("idle prompt %+v", b)
	}
	feed(e, "\x1b[?1049h\x1b]133;A\x07alternate")
	if b, ok := e.Commands(); ok || len(b) > 0 {
		t.Fatalf("alternate screen %+v", b)
	}
	feed(e, "\x1b[?1049l")
	if !e.HasPromptMarks() {
		t.Fatal("lost main screen marks")
	}
}

func TestCommandsMultilineAndRunning(t *testing.T) {
	e := New(10, 4, nil).(*emulator)
	defer e.Close()
	feed(e, "\x1b]133;A\x07$ \x1b]133;B\x07echo 界界界\r\ncontinued\r\n\x1b]133;C\x07one\r\n\r\ntwo")
	blocks, ok := e.Commands()
	if !ok || len(blocks) != 1 || blocks[0].Command != "echo 界界界\ncontinued" || blocks[0].Output != "one\n\ntwo" || blocks[0].Finished {
		t.Fatalf("running %+v", blocks)
	}
	feed(e, "\x1b]133;D;bad\x07")
	blocks, _ = e.Commands()
	if !blocks[0].Finished || blocks[0].StatusKnown {
		t.Fatalf("unknown status %+v", blocks)
	}
}

func TestCommandsDiscardEvictedBlocks(t *testing.T) {
	e := recordedCommands(t)
	e.SetScrollback(2)
	feed(e, strings.Repeat("line\r\n", 10))
	if b, ok := e.Commands(); ok || len(b) > 0 || e.HasPromptMarks() {
		t.Fatalf("evicted %+v", b)
	}
}

func TestRecordedPromptTargets(t *testing.T) {
	e := recordedCommands(t)
	blocks, _ := e.Commands()
	off := 0
	for i := len(blocks) - 1; i >= 0; i-- {
		next := e.PromptOffset(off, 1)
		// The last command starts on the live screen in this recording.
		if blocks[i].Prompt.Line >= e.ScrollbackPushed() {
			continue
		}
		if line := e.ScrollbackPushed() - uint64(next); line != blocks[i].Prompt.Line {
			t.Errorf("jump line %d, want %d", line, blocks[i].Prompt.Line)
		}
		off = next
	}
	if got := e.PromptOffset(off, 99); got != off {
		t.Errorf("oldest moved %d to %d", off, got)
	}
	if got := e.PromptOffset(off, -99); got != 0 {
		t.Errorf("next past newest %d", got)
	}
}

func TestCommandsAtRightEdge(t *testing.T) {
	e := New(8, 3, nil).(*emulator)
	defer e.Close()
	feed(e, "\x1b]133;A\x07$ \x1b]133;B\x07true\r\n\x1b]133;C\x0712345678")
	b, _ := e.Commands()
	if len(b) != 1 || b[0].Output != "12345678" {
		t.Fatalf("running at edge: %+v", b)
	}
	feed(e, "\x1b]133;D;0\x07")
	b, _ = e.Commands()
	if len(b) != 1 || b[0].Output != "12345678" || !b[0].Finished {
		t.Fatalf("finished at edge: %+v", b)
	}
}

func BenchmarkCommandReflow(b *testing.B) {
	raw := strings.Repeat("\x1b]133;A\x07$ \x1b]133;B\x07true\r\n\x1b]133;C\x07x\r\n\x1b]133;D;0\x07", 5000)
	e := New(80, 24, nil).(*emulator)
	defer e.Close()
	feed(e, raw)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.Resize(80+i%2, 24)
	}
}

func TestCommandsForgetClearedScreen(t *testing.T) {
	e := New(40, 10, nil).(*emulator)
	defer e.Close()
	feed(e, "\x1b]133;A\x07$ \x1b]133;B\x07true\r\n\x1b]133;C\x07\x1b]133;D;0\x07")
	feed(e, "\x1b[2J\x1b[Hreplacement text")
	if blocks, ok := e.Commands(); ok || len(blocks) > 0 {
		t.Fatalf("cleared screen retains commands: %+v", blocks)
	}
}
