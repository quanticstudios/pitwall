package panel

import (
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
)

// TestBlankFor: each view says it is reading while git or the session
// file has not answered, and shows an empty state once it has and there
// is nothing, or its rows when there are some.
func TestBlankFor(t *testing.T) {
	claude := &model.Pane{Provider: model.ProviderClaude, Transcript: "/t"}
	feed := &flow.Feed{}
	full := &flow.Feed{Plan: []flow.Step{{Text: "a"}}, Subagents: []flow.Subagent{{ID: "s"}}, Turns: []flow.Turn{{Prompt: "go"}}}
	for _, tc := range []struct {
		name string
		in   Input
		v    View
		want string // "wait:" for a loading line, "" for rows
	}{
		{"git asked", Input{WaitGit: true}, ViewChanges, "wait:Reading git…"},
		{"not a repo", Input{}, ViewChanges, "git-branch:Not in a git repository."},
		{"no changes", Input{Git: true, Base: "refs/heads/main"}, ViewChanges, "check:No changes from main."},
		{"changes", Input{Git: true, Files: []gitstat.FileStat{{Path: "a"}}}, ViewChanges, ""},
		{"no pane", Input{}, ViewFlow, "terminal:No pane is focused."},
		{"shell", Input{Pane: &model.Pane{}}, ViewFlow, "bot:"},
		{"no session file pitwall reads", Input{Pane: &model.Pane{Provider: model.ProviderAmp}}, ViewFlow, "book-open:"},
		{"reading the session", Input{Pane: claude, WaitFeed: true}, ViewTimeline, "wait:Reading the session file…"},
		{"no session file", Input{Pane: claude}, ViewFlow, "book-open:"},
		{"flow", Input{Pane: claude, Feed: feed}, ViewFlow, ""},
		{"no subagents", Input{Pane: claude, Feed: feed}, ViewSubagents, "bot:"},
		{"no plan", Input{Pane: claude, Feed: feed}, ViewPlan, "layers:"},
		{"no timeline", Input{Pane: claude, Feed: feed}, ViewTimeline, "zap:Nothing has happened in this session yet."},
		{"subagents", Input{Pane: claude, Feed: full}, ViewSubagents, ""},
		{"plan", Input{Pane: claude, Feed: full}, ViewPlan, ""},
		{"timeline", Input{Pane: claude, Feed: full}, ViewTimeline, ""},
	} {
		b := blankFor(&tc.in, tc.v)
		got := ""
		if b != nil {
			got = b.icon + ":" + b.text
			if b.wait {
				got = "wait:" + b.text
			}
		}
		if tc.want == "" && got != "" || !strings.HasPrefix(got, tc.want) {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}
