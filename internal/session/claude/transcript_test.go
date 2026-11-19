package claude

import (
	"bytes"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/session"
)

// apply reduces events into snap as the pane chat does.
func apply(t *testing.T, snap session.Snapshot, es []session.Event) session.Snapshot {
	t.Helper()
	for _, e := range es {
		if e.CreatedAt == "" {
			e.CreatedAt = "2026-11-18T10:00:00.000Z"
		}
		for _, d := range session.Reduce(snap, e) {
			d.Version, d.SessionID, d.Seq, d.Revision, d.UpdatedAt = session.DeltaVersion, snap.SessionID, snap.Revision+1, snap.Revision+1, e.CreatedAt
			n, err := session.Apply(&snap, snap.Revision, d)
			if err != nil {
				t.Fatalf("%s: %v", d.Type, err)
			}
			snap = n
		}
	}
	return snap
}

func TestTranscriptSession(t *testing.T) {
	b, err := os.ReadFile("../../flow/testdata/claude/session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	tr := NewTranscript()
	snap := base()
	for _, l := range bytes.Split(b, []byte("\n")) {
		snap = apply(t, snap, tr.Line(l))
	}
	var prompts []string
	tools := map[string]string{}
	for _, it := range snap.Content {
		switch {
		case it.Kind == session.KindUserPrompt:
			prompts = append(prompts, it.Text)
		case it.Kind == session.KindToolCall:
			tools[it.Tool.ToolName+":"+it.ID] = it.Tool.Status
		}
	}
	if len(prompts) != 2 || prompts[0] != "Fix the login bug\nIt fails on empty passwords." || prompts[1] != "Now add a regression test" {
		t.Fatalf("prompts %q: want the two the user typed, no command echo or notification", prompts)
	}
	if tools["Bash:tu1"] != "failed" || tools["Read:tu2"] != "completed" || tools["Bash:x1"] != "" {
		t.Errorf("tools %v: want Bash failed, Read completed, no sidechain call", tools)
	}
	// The notification reached an idle Claude, which ran a turn for it.
	if len(snap.Turns) != 3 || snap.Turns[0].State != session.TurnCompleted || snap.Turns[1].State != session.TurnCompleted ||
		snap.Turns[2].State != session.TurnRunning {
		t.Fatalf("turns %+v: want the first completed at its turn_duration, the notification's completed, the last running", snap.Turns)
	}
	if !tr.Running() || snap.SessionState != session.StateRunning {
		t.Errorf("running %v, state %s: want the last turn running", tr.Running(), snap.SessionState)
	}
	if snap.Model == nil || *snap.Model != "claude-opus-5-5" {
		t.Errorf("model %v: want the assistant lines' model", snap.Model)
	}
	if snap.Content[0].CreatedAt != "2026-10-05T10:00:00.000Z" {
		t.Errorf("first item at %s: want its line's timestamp", snap.Content[0].CreatedAt)
	}

	// Claude Code asks about its running Grep call: a permission card for
	// that call, withdrawn when the turn ends.
	snap = apply(t, snap, tr.Ask("42", "Grep"))
	if len(snap.PendingApprovals) != 1 || snap.PendingApprovals[0].Request == nil || snap.PendingApprovals[0].Request.ToolName != "Grep" {
		t.Fatalf("approvals %+v: want one for Grep", snap.PendingApprovals)
	}
	if snap = apply(t, snap, tr.Ask("42", "Grep")); len(snap.PendingApprovals) != 1 {
		t.Fatalf("asking again opened %d approvals", len(snap.PendingApprovals))
	}
	snap = apply(t, snap, tr.End(session.TurnCompleted))
	if len(snap.PendingApprovals) != 0 || snap.SessionState != session.StateReady || tr.Running() {
		t.Errorf("after End: %d approvals, state %s: want none, ready", len(snap.PendingApprovals), snap.SessionState)
	}
}

func TestTranscriptInterruptAndQuestion(t *testing.T) {
	tr := NewTranscript()
	snap := base()
	lines := []string{
		`{"type":"user","uuid":"p1","timestamp":"2026-11-18T10:00:00Z","message":{"role":"user","content":"pick one"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-11-18T10:00:01Z","message":{"id":"m1","model":"claude-opus-5-5","content":[{"type":"tool_use","id":"q1","name":"AskUserQuestion","input":{"questions":[{"question":"Which?","header":"Pick","options":[{"label":"A","description":"first"},{"label":"B","description":"second"}]}]}}]}}`,
	}
	for _, l := range lines {
		snap = apply(t, snap, tr.Line([]byte(l)))
	}
	snap = apply(t, snap, tr.Ask("7", ""))
	if len(snap.PendingUserInputs) != 1 || len(snap.PendingUserInputs[0].Questions) != 1 || len(snap.PendingUserInputs[0].Questions[0].Options) != 2 {
		t.Fatalf("inputs %+v: want the question with its two options", snap.PendingUserInputs)
	}
	snap = apply(t, snap, tr.Line([]byte(`{"type":"user","uuid":"i1","timestamp":"2026-11-18T10:00:02Z","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user]"}]}}`)))
	if len(snap.PendingUserInputs) != 0 || snap.Turns[0].State != session.TurnInterrupted {
		t.Errorf("after the interrupt: %d inputs, turn %s: want none, interrupted", len(snap.PendingUserInputs), snap.Turns[0].State)
	}
}

// readTranscript is a testdata/transcripts file through a Transcript.
func readTranscript(t *testing.T, name string) (*Transcript, session.Snapshot) {
	t.Helper()
	b, err := os.ReadFile("testdata/transcripts/" + name)
	if err != nil {
		t.Fatal(err)
	}
	tr := NewTranscript()
	snap := base()
	for _, l := range bytes.Split(b, []byte("\n")) {
		snap = apply(t, snap, tr.Line(l))
	}
	return tr, snap
}

// systems is snap's system items as "phase: text", the text's first line.
func systems(snap session.Snapshot) []string {
	var out []string
	for _, it := range snap.Content {
		if it.Kind == session.KindSystem {
			first, _, _ := strings.Cut(it.Text, "\n")
			out = append(out, it.Phase+": "+first)
		}
	}
	return out
}

// TestTranscriptCommands: Claude Code 2.1.328's TUI, recorded live: slash
// commands, a ! command, a model switch, pasted text, an auto-mode
// notice and a /compact. No markup reaches a prompt or a line.
func TestTranscriptCommands(t *testing.T) {
	_, snap := readTranscript(t, "tui-commands.jsonl")
	want := []string{
		"command: /context",
		"output: \x1b[1mContext usage\x1b[22m",
		"command: !echo bang-output",
		"output: bang-output",
		"setting: Switched to Opus 5.5",
		"setting: Switched to Sonnet 5.5",
		"notice: Auto mode lets Claude handle permission prompts automatically",
		"compact: Conversation compacted",
	}
	got := systems(snap)
	for i := range got {
		if i < len(want) && strings.HasPrefix(got[i], want[i]) {
			got[i] = want[i]
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("system items:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	var prompts []string
	for _, it := range snap.Content {
		if it.Kind == session.KindUserPrompt {
			prompts = append(prompts, it.Text)
		}
		if strings.Contains(it.Text, "<command-") || strings.Contains(it.Text, "<local-command") || strings.Contains(it.Text, "system-reminder") {
			t.Errorf("markup in a %s item: %.120q", it.Kind, it.Text)
		}
	}
	if len(prompts) != 6 || !strings.Contains(prompts[1], `<pasted_content id="ba5e">`) {
		t.Errorf("prompts %q: want the six typed, the pasted one kept whole for its chip", prompts)
	}
	for _, it := range snap.Content {
		if it.Phase == session.PhaseCompact && !strings.Contains(it.Text, "Primary Request") {
			t.Errorf("compaction %.200q: want its summary", it.Text)
		}
	}
	if len(snap.UsageReports) == 0 || snap.UsageReports[0].EstimatedCostUSD == nil || *snap.UsageReports[0].EstimatedCostUSD <= 0 {
		t.Errorf("usage reports %+v: want each turn's tokens and cost", snap.UsageReports)
	}
}

// TestTranscriptTasks: a live TUI session with a background subagent, a
// background command that ends while Claude is idle, a question turned
// down, a hook's block and an interrupt.
func TestTranscriptTasks(t *testing.T) {
	_, snap := readTranscript(t, "tui-tasks.jsonl")
	want := []string{
		"notice: Background task finished: Sleep then echo in background",
		"setting: Effort set to high",
	}
	if got := systems(snap); !slices.Equal(got, want) {
		t.Errorf("system items:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	var agent *session.ToolCall
	for _, it := range snap.Content {
		if it.Tool != nil && it.Tool.ToolName == "Agent" {
			agent = it.Tool
		}
	}
	if agent == nil || agent.Status != "completed" || !strings.Contains(agent.Output, "2 notes.txt") {
		t.Fatalf("agent %+v: want it completed by its notification, with its report", agent)
	}
	var states []string
	for _, tu := range snap.Turns {
		states = append(states, tu.State)
	}
	if !slices.Contains(states, session.TurnInterrupted) {
		t.Errorf("turn states %v: want the interrupted one", states)
	}
}

// TestTranscriptPrintCompact: the transcript of a print-mode run with two
// background subagents, a hook's block and a /compact: thinking rows are
// timed from thinkingDurationMs.
func TestTranscriptPrintCompact(t *testing.T) {
	_, snap := readTranscript(t, "print-compact.jsonl")
	thought := 0
	for _, it := range snap.Content {
		if it.Kind != session.KindReasoningText {
			continue
		}
		thought++
		a, _ := time.Parse(time.RFC3339Nano, it.CreatedAt)
		b, _ := time.Parse(time.RFC3339Nano, it.UpdatedAt)
		if d := b.Sub(a); d < 500*time.Millisecond || d > 3*time.Second {
			t.Errorf("thinking %s took %v: want its thinkingDurationMs", it.ID, d)
		}
	}
	if thought != 2 {
		t.Errorf("%d thinking rows, want 2", thought)
	}
	if got := systems(snap); !slices.Contains(got, "compact: Conversation compacted") {
		t.Errorf("system items %q: want the compaction", got)
	}
}

func TestSettingNote(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Set model to `Opus 5.5` and saved as your default for new sessions", "Switched to Opus 5.5"},
		{"Set model to `claude-opus-5-5`", "Switched to claude-opus-5-5"},
		{"Set effort level to high (saved as your default for new sessions): Comprehensive implementation", "Effort set to high"},
		{"Model 'x' not found", ""},
	} {
		if got, _ := settingNote(c.in); got != c.want {
			t.Errorf("settingNote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAPIError(t *testing.T) {
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.Local)
	at := time.Date(2026, 11, 19, 15, 0, 0, 0, time.Local).Unix()
	if got := apiError("Claude AI usage limit reached|"+strconv.FormatInt(at, 10), now); got != "Claude AI usage limit reached · resets 15:00" {
		t.Errorf("apiError = %q", got)
	}
	if got := apiError("API Error: 529 Overloaded", now); got != "API Error: 529 Overloaded" {
		t.Errorf("apiError = %q", got)
	}
}

// TestNotifiedOnce: a notification Claude Code queued mid-turn and then
// added as a line of its own shows one row.
func TestNotifiedOnce(t *testing.T) {
	tr := NewTranscript()
	n := `<task-notification>\n<task-id>b1</task-id>\n<tool-use-id>x</tool-use-id>\n<status>completed</status>\n<summary>Background command \"Sleep\" completed (exit code 0)</summary>\n</task-notification>`
	snap := apply(t, base(), tr.Line([]byte(`{"type":"attachment","uuid":"a1","timestamp":"2026-11-19T10:00:00Z","attachment":{"type":"queued_command","prompt":"`+n+`"}}`)))
	snap = apply(t, snap, tr.Line([]byte(`{"type":"user","uuid":"u1","timestamp":"2026-11-19T10:00:01Z","origin":{"kind":"task-notification"},"message":{"role":"user","content":"`+n+`"}}`)))
	if got := systems(snap); len(got) != 1 || got[0] != "notice: Background task finished: Sleep" {
		t.Fatalf("rows %q: want one", got)
	}
}
