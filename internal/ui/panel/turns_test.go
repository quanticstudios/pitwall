package panel

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"gioui.org/op"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// rowsOf is tr's rows in short: "prompt", "prose:<first words>", a fold
// "fold N" of N calls (running "live N"), one of thinking alone
// "thought", an inline call "call", an
// open fold's item "item", the reply "reply"; system rows and cards by
// their segment.
func rowsOf(s *session.Snapshot, tr *Transcript) string {
	var out []string
	for _, r := range tr.rows {
		switch {
		case r.tail != nil || r.res != nil:
			out = append(out, "tail")
		case r.seg == segBlock && r.b.kind == blockPrompt:
			out = append(out, "prompt")
		case r.seg == segProse:
			w := strings.Fields(s.Content[r.i].Text)
			out = append(out, "prose:"+strings.Join(w[:min(2, len(w))], " "))
		case r.seg == segHead && r.b.hi > r.b.lo && r.b.tools == 0:
			out = append(out, "thought")
		case r.seg == segHead && r.running:
			out = append(out, "live "+strconv.Itoa(r.b.tools))
		case r.seg == segHead:
			out = append(out, "fold "+strconv.Itoa(r.b.tools))
		case r.seg == segItem && r.top:
			out = append(out, "call")
		case r.seg == segItem:
			out = append(out, "item")
		case r.seg == segReply:
			out = append(out, "reply")
		case r.seg == segTodo:
			out = append(out, "todo")
		case r.seg == segChanges:
			out = append(out, "changes")
		default:
			out = append(out, "block")
		}
	}
	return strings.Join(out, " | ")
}

// TestTurnSplit: each provider's turn reads "summary, fold, summary,
// fold, reply": what was written for the reader sits between the folds
// in reading order, each fold holds the calls between, a lone call is
// its own row once the turn moved past it, and a running turn's last
// fold says it works.
func TestTurnSplit(t *testing.T) {
	th := theme.Dark()
	want := map[string]string{
		"claude-finished": "prompt | prose:Running the | fold 3 | prose:The divide | call | prose:Running the | call | reply | changes",
		"claude-running": "prompt | prose:Running the | fold 3 | prose:The divide | call | prose:Running the | call | reply | changes | " +
			"prompt | prose:Reading the | call | prose:The tests | live 2",
		// The rollout logs an MCP call and the plan before its prompt.
		"codex-finished": "call | todo | prompt | prose:I’ll look | fold 2 | prose:`update_plan` is | fold 2 | prose:The regression | fold 4 | reply | changes",
		"codex-running":  "live 1 | todo | prompt | prose:I’ll look | fold 2 | prose:`update_plan` is | fold 2 | prose:The regression | live 4",
		"agy-finished":   "prompt | thought | prose:I'll run | fold 3 | prose:`divide` floors | fold 2 | reply",
		"agy-running":    "prompt | thought | prose:I'll run | fold 3 | prose:`divide` floors | live 1",
	}
	for _, f := range turnFixtures {
		for _, run := range []bool{false, true} {
			n, name := f.finished, f.name+"-finished"
			if run {
				n, name = f.running, f.name+"-running"
			}
			s := f.replay(t, n, run)
			var tr Transcript
			var ops op.Ops
			now := time.Date(2026, 11, 19, 14, 1, 0, 0, time.UTC)
			transcriptFrame(&tr, th, s, &ops, now)
			transcriptFrame(&tr, th, s, &ops, now)
			if got := rowsOf(s, &tr); got != want[name] {
				t.Errorf("%s:\n got %s\nwant %s", name, got, want[name])
			}
		}
	}
}

// TestTurnSplitFolds: each fold keeps its own open state as events come
// and across a reconnect, and a new summary does not move the fold the
// user opened.
func TestTurnSplitFolds(t *testing.T) {
	th := theme.Dark()
	f := turnFixtures[0]
	s := f.replay(t, f.running, true)
	var tr Transcript
	var ops op.Ops
	now := time.Date(2026, 11, 19, 14, 1, 0, 0, time.UTC)
	frame := func(s *session.Snapshot) string {
		transcriptFrame(&tr, th, s, &ops, now)
		transcriptFrame(&tr, th, s, &ops, now)
		return rowsOf(s, &tr)
	}
	frame(s)
	first := slices.IndexFunc(tr.rows, func(r trow) bool { return r.seg == segHead })
	tr.clicks[logID(s, tr.rows[first].b)].Click()
	open := "prompt | prose:Running the | fold 3 | item | item | item | item | prose:The divide"
	if got := frame(s); !strings.HasPrefix(got, open) {
		t.Fatalf("opened the first fold:\n got %s\nwant %s…", got, open)
	}
	turn := deref(s.Content[len(s.Content)-1].TurnID)
	s.Content = append(s.Content,
		session.Item{ID: "x1", Kind: session.KindReasoningText, Phase: session.PhaseSummary, Text: "Checking the new test fails without the fix.", TurnID: &turn},
		session.Item{ID: "x2", Kind: session.KindToolCall, TurnID: &turn, Tool: &session.ToolCall{ToolName: "Bash", Command: "git stash", Status: "running"}})
	got := frame(s)
	if !strings.HasPrefix(got, open) || !strings.HasSuffix(got, "prose:The tests | fold 2 | prose:Checking the | live 1") {
		t.Fatalf("new events:\n got %s", got)
	}
	again := *s // a reconnect: the same chat in a fresh snapshot
	again.Content = slices.Clone(s.Content)
	if got := frame(&again); !strings.HasPrefix(got, open) {
		t.Fatalf("a reconnect closed the fold the user opened:\n got %s", got)
	}
}
