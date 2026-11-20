package claude

import (
	"bufio"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/session"
)

// fixture is testdata/local/name's lines: Claude Code 2.1.329's own, the
// account's names and zone replaced.
func fixture(t *testing.T, name string) []string {
	t.Helper()
	f, err := os.Open("testdata/local/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

// outputs are es' local command outputs, and whether any is a reply.
func outputs(es []session.Event) (out []string, reply bool) {
	for _, e := range es {
		switch {
		case e.Phase == session.PhaseOutput:
			out = append(out, e.Text)
		case e.Kind == session.KindAssistantText:
			reply = true
		}
	}
	return out, reply
}

func TestStreamLocalOutput(t *testing.T) {
	for _, c := range []struct {
		file string
		want []string
	}{
		{"stream-cost.jsonl", []string{
			"Current week (all models): 52% used · resets Nov 24, 9pm (Europe/Berlin)",
			"Estimated cost:  $0.0179",
			"claude-sonnet-5-5:  2 input, 7 output, 13.6k cache read, 4.1k cache write ($0.0179)"}},
		{"stream-context.jsonl", []string{"## Context Usage", "| Free space | 949.4k | 94.9% |"}},
		// stream-json has no /status: the card says what init did.
		{"stream-status.jsonl", []string{"Version:  2.1.329", "cwd:  /tmp/demo/repo", "Model:  claude-sonnet-5-5", "Permission mode:  default"}},
	} {
		s := newStream("")
		var es []session.Event
		for _, l := range fixture(t, c.file) {
			es = append(es, s.line([]byte(l))...)
		}
		out, reply := outputs(es)
		if reply || len(out) != 1 {
			t.Fatalf("%s: outputs %q, reply %v; want one output, no reply", c.file, out, reply)
		}
		for _, w := range c.want {
			if !strings.Contains(out[0], w) {
				t.Errorf("%s: output lacks %q:\n%s", c.file, w, out[0])
			}
		}
		if s.contextModel != "claude-sonnet-5-5" {
			t.Errorf("%s: context model %q, want the init's", c.file, s.contextModel)
		}
	}
}

// A synthetic reply with no tags or report still shows as output, its
// text as Claude wrote it.
func TestStreamLocalOutputBare(t *testing.T) {
	s := newStream("")
	es := s.line([]byte(`{"type":"assistant","uuid":"u1","message":{"id":"m1","model":"<synthetic>","content":[]},"local_command_source":"<local-command-stdout>Odd</local-command-stdout>","local_command_run":{"command":"usage"},"usage_report":"nonsense"}`))
	if out, _ := outputs(es); len(out) != 1 || out[0] != "Odd" {
		t.Fatalf("outputs %q, want [Odd]", out)
	}
}

func TestTranscriptContext(t *testing.T) {
	tr := NewTranscript()
	var es []session.Event
	for _, l := range fixture(t, "tui-context.jsonl") {
		es = append(es, tr.Line([]byte(l))...)
	}
	out, _ := outputs(es)
	if len(out) != 1 || !strings.Contains(out[0], "Context usage") || !strings.Contains(ansiCodes.ReplaceAllString(out[0], ""), "Free space: 924.4k tokens (92%)") {
		t.Fatalf("outputs %q", out)
	}
}

func TestTranscriptDialog(t *testing.T) {
	at := time.Date(2026, 11, 20, 17, 40, 0, 0, time.Local)
	tr := NewTranscript()
	if es := tr.Dialog("/usage", at); es != nil {
		t.Fatalf("before any turn: %v, want nil", es)
	}
	for _, l := range fixture(t, "tui-turn.jsonl") {
		tr.Line([]byte(l))
	}
	if es := tr.Dialog("/help", at); es != nil {
		t.Fatalf("/help: %v, want nil", es)
	}
	resets := time.Date(2026, 11, 24, 21, 0, 0, 0, time.Local)
	seen := at
	tr.Limit = func() (flow.Limit, bool) {
		return flow.Limit{Seen: seen, Windows: []flow.Window{{Minutes: 300, Used: 24}, {Minutes: 7 * 24 * 60, Used: 52.5, Resets: resets}}}, true
	}
	es := tr.Dialog("/usage", at)
	if len(es) != 2 || es[0].Phase != session.PhaseCommand || es[0].Text != "/usage" || es[1].Phase != session.PhaseOutput {
		t.Fatalf("events %+v, want a chip and its output", es)
	}
	for _, w := range []string{"Current session: 24% used\n", "Current week (all models): 52.5% used · resets Nov 24, 9pm",
		"claude-sonnet-5-5:  2 input, 7 output, 28.2k cache read, 14.4k cache write"} {
		if !strings.Contains(es[1].Text, w) {
			t.Errorf("output lacks %q:\n%s", w, es[1].Text)
		}
	}
	// A report from long before says nothing of the limits now.
	seen = at.Add(-time.Hour)
	if out := tr.Dialog("/cost", at)[1].Text; strings.Contains(out, "used") {
		t.Errorf("stale limits shown:\n%s", out)
	}
	st := tr.Dialog("/status", at)[1].Text
	for _, w := range []string{"Version:  2.1.329", "Branch:  master", "Model:  claude-sonnet-5-5", "Permission mode:  bypassPermissions"} {
		if !strings.Contains(st, w) {
			t.Errorf("status lacks %q:\n%s", w, st)
		}
	}
}

// Should the TUI write a dialog command and its "dismissed" line, the
// chat shows the command's card and not the line.
func TestTranscriptDialogWritten(t *testing.T) {
	tr := NewTranscript()
	for _, l := range fixture(t, "tui-turn.jsonl") {
		tr.Line([]byte(l))
	}
	var es []session.Event
	for _, l := range []string{
		`{"type":"system","subtype":"local_command","uuid":"c1","timestamp":"2026-11-20T17:40:00.000Z","content":"<command-name>/status</command-name>\n<command-args></command-args>"}`,
		`{"type":"system","subtype":"local_command","uuid":"c2","timestamp":"2026-11-20T17:40:01.000Z","content":"<local-command-stdout>Status dialog dismissed</local-command-stdout>"}`,
		`{"type":"system","subtype":"local_command","uuid":"c3","timestamp":"2026-11-20T17:40:02.000Z","content":"<command-name>/doctor</command-name>"}`,
		`{"type":"system","subtype":"local_command","uuid":"c4","timestamp":"2026-11-20T17:40:03.000Z","content":"<local-command-stdout>All good</local-command-stdout>"}`,
	} {
		es = append(es, tr.Line([]byte(l))...)
	}
	out, _ := outputs(es)
	if len(out) != 2 || !strings.HasPrefix(out[0], "Version:") || out[1] != "All good" {
		t.Fatalf("outputs %q", out)
	}
}

func TestKilo(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 812: "812", 1000: "1k", 28_249: "28.2k", 1_400_000: "1.4m"} {
		if got := kilo(n); got != want {
			t.Errorf("kilo(%d) = %q, want %q", n, got, want)
		}
	}
}
