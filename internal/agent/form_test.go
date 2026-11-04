package agent

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/vt"
)

func TestOffers(t *testing.T) {
	question := model.Activity{Provider: model.ProviderClaude, State: model.StateAwaitingInput,
		Detail: "Which database should the cache use?", Options: []string{"Redis", "SQLite"},
		OptionNotes: []string{"Fast, runs as a service", "One file, no service"}}
	otherNote := question
	otherNote.OptionNotes = []string{"Fast, runs as a service", "Two files"}
	stale := question
	stale.Detail = "Which queue should it use?"
	moved := question
	moved.Options = []string{"Postgres", "SQLite"}
	plan := model.Activity{Provider: model.ProviderClaude, State: model.StatePlanReady}
	perm := model.Activity{Provider: model.ProviderClaude, State: model.StatePendingApproval}
	codex := model.Activity{Provider: model.ProviderCodex, State: model.StatePendingApproval}
	gemini := model.Activity{Provider: model.ProviderGemini, State: model.StatePendingApproval}
	for _, tc := range []struct {
		name string
		a    model.Activity
		file string
		want []Offer
	}{
		{"question", question, "claude-question.txt", []Offer{{"Redis", "1"}, {"SQLite", "2"}}},
		{"question not on screen", stale, "claude-question.txt", nil},
		{"option not on screen", moved, "claude-question.txt", nil},
		{"description not on screen", otherNote, "claude-question.txt", nil},
		{"multi-select has no options", model.Activity{Provider: model.ProviderClaude, State: model.StateAwaitingInput, Detail: question.Detail}, "claude-question.txt", nil},
		{"numberless form has no keys", question, "tuios-claude-question.txt", nil},
		{"plan", plan, "claude-plan.txt", []Offer{{ApprovePlan, "3"}}},
		{"plan on a permission screen", plan, "claude-permission.txt", nil},
		{"permission", perm, "claude-permission.txt", []Offer{{AllowAlways, "2"}}},
		{"permission on a plan screen", perm, "claude-plan.txt", nil},
		{"permission on an idle screen", perm, "claude-idle.txt", nil},
		{"codex permission", codex, "tuios-codex-approval.txt", []Offer{{AllowAlways, "A"}}},
		{"codex trust dialog", codex, "codex-trust.txt", nil},
		{"no keys for gemini", gemini, "claude-permission.txt", nil},
	} {
		if got := Offers(tc.a, screenFixture(t, tc.file)); !slices.Equal(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestEmptyPlan(t *testing.T) {
	g := screenFixture(t, "claude-empty-plan.txt")
	if got := ReadScreen(g); got != ScreenForm {
		t.Errorf("screen = %d, want form", got)
	}
	if got := StateOf(model.ProviderClaude, g); got != model.StatePlanReady {
		t.Errorf("state = %q, want plan-ready", got)
	}
	if !FormUp(g, "1. Yes, and use auto mode\n2. No") {
		t.Error("empty plan must hold typed text")
	}
	if got := Offers(model.Activity{Provider: model.ProviderClaude, State: model.StatePlanReady}, g); got != nil {
		t.Errorf("auto mode has offers: %v", got)
	}
	for _, text := range []string{
		"I will exit plan mode after writing the plan.",
		"⏺ The dialog says Claude wants to exit plan mode.",
		"When Claude wants to exit plan mode it calls ExitPlanMode.",
		"The dialog asks Exit plan mode? before continuing.",
	} {
		chat := gridOf(text, "❯ ")
		for _, provider := range []model.Provider{model.ProviderClaude, model.ProviderCodex, model.ProviderPi} {
			if ReadScreen(chat) != ScreenNone || StateOf(provider, chat) != "" || FormUp(chat, "") {
				t.Errorf("%s: chat text matches the empty plan screen: %q", provider, text)
			}
		}
	}
}

func TestEmptyPlanTitles(t *testing.T) {
	for _, title := range []string{
		"Exit plan mode? · from a subagent",
		"Exit plan mode? · from the planner agent",
		"Exit plan mode?                                         2 of 2",
	} {
		t.Run(title, func(t *testing.T) {
			g := gridOf(title, "", "Claude wants to exit plan mode", "",
				"❯ 1. Yes, and use auto mode", "  2. No")
			if ReadScreen(g) != ScreenForm || StateOf(model.ProviderClaude, g) != model.StatePlanReady {
				t.Fatal("empty plan is not waiting")
			}
			trace := ExplainScreen(model.ProviderClaude, g)
			if trace.Rule != "plan/claude wants to exit plan mode" {
				t.Fatalf("rule = %q", trace.Rule)
			}
			if got := Offers(model.Activity{Provider: model.ProviderClaude, State: model.StatePlanReady}, g); got != nil {
				t.Errorf("auto mode has offers: %v", got)
			}
		})
	}
}

func TestEmptyPlanNarrow(t *testing.T) {
	b, err := os.ReadFile("testdata/claude-empty-plan.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, width := range []int{20, 28, 29, 30, 40, 100} {
		e := vt.New(width, 30, nil)
		e.Write([]byte(strings.ReplaceAll(string(b), "\n", "\r\n")))
		g := e.Snapshot()
		if ReadScreen(g) != ScreenForm || State(g) != model.StatePlanReady || !FormUp(g, "") {
			t.Errorf("width %d: empty plan is not waiting", width)
		}
	}
}

// The plan's other yeses raise the permission mode, and two ways to say
// always are a guess, so neither gets a button.
func TestOffersAmbiguous(t *testing.T) {
	perm := model.Activity{Provider: model.ProviderCodex, State: model.StatePendingApproval}
	if got := Offers(perm, gridOf("Would you like to run the following command?", "› 1. Yes, proceed (y)",
		"  2. Yes, and don't ask again for this command (a)", "  3. Yes, and don't ask again for commands that start with `go` (p)", "esc to cancel")); got != nil {
		t.Errorf("two always options: %v", got)
	}
	plan := model.Activity{Provider: model.ProviderClaude, State: model.StatePlanReady}
	if got := Offers(plan, gridOf("Would you like to proceed?", "❯ 1. Yes, and use auto mode", "  2. Yes, auto-accept edits", "  3. No, keep planning")); got != nil {
		t.Errorf("no manual yes: %v", got)
	}
}

// A form that starts like the asked one is another form: the whole question
// and every option must match, however they wrap.
func TestOffersWholeForm(t *testing.T) {
	q := func(detail string, options ...string) model.Activity {
		return model.Activity{Provider: model.ProviderClaude, State: model.StateAwaitingInput, Detail: detail, Options: options}
	}
	long := "Do you want me to remove all the cached build files and deployment artifacts belonging to account "
	footer := "Enter to select · Esc to cancel"
	for _, tc := range []struct {
		name string
		a    model.Activity
		g    vt.Grid
		want int
	}{
		{"exact", q("Which?", "Yes", "No"), gridOf("☐ Pick", "", "Which?", "", "> 1. Yes", "  2. No", footer), 2},
		{"wrapped question", q(long+"staging?", "Yes", "No"), gridOf(long[:60], long[60:]+"staging?", "> 1. Yes", "  2. No", footer), 2},
		{"wrapped option", q("Where?", "Deploy release to staging now", "Cancel"), gridOf("Where?", "> 1. Deploy release to", "     staging now", "  2. Cancel", footer), 2},
		{"question with the same start", q(long+"staging?", "Yes", "No"), gridOf(long+"production?", "> 1. Yes", "  2. No", footer), 0},
		{"question with the same end", q("delete staging?", "Yes", "No"), gridOf("do not delete staging?", "> 1. Yes", "  2. No", footer), 0},
		{"same end across a wrap", q("delete staging?", "Yes", "No"), gridOf("do not", "delete staging?", "> 1. Yes", "  2. No", footer), 0},
		{"a paragraph more", q("Which?", "Yes", "No"), gridOf("First read this.", "", "Which?", "> 1. Yes", "  2. No", footer), 0},
		{"wrapped option differs", q("Where?", "Deploy release to staging", "Cancel"), gridOf("Where?", "> 1. Deploy release to", "     production", "  2. Cancel", footer), 0},
		{"option longer on screen", q("Where?", "Deploy release to", "Cancel"), gridOf("Where?", "> 1. Deploy release to", "     production", "  2. Cancel", footer), 0},
		{"question scrolled off", q(long+"staging?", "Yes", "No"), gridOf("staging?", "> 1. Yes", "  2. No", footer), 0},
		{"more after a blank row", q("Where?", "Deploy to staging", "Cancel"), gridOf("Where?", "> 1. Deploy to staging", "", "     and production", "  2. Cancel", footer), 0},
		{"more that reads like a footer", q("Where?", "Deploy to staging", "Cancel"), gridOf("Where?", "> 1. Deploy to staging", "     Enter to select production too", "  2. Cancel", footer), 0},
		{"last option with more", q("Where?", "Deploy", "Cancel"), gridOf("Where?", "> 1. Deploy", "  2. Cancel", "", "     everything", footer), 0},
		{"last option with no footer", q("Where?", "Deploy", "Cancel"), gridOf("Where?", "> 1. Deploy", "  2. Cancel", "Press enter to confirm"), 0},
	} {
		if got := Offers(tc.a, tc.g); len(got) != tc.want {
			t.Errorf("%s: got %v, want %d offers", tc.name, got, tc.want)
		}
	}
}

// FormUp holds typed text and Enter for any form but a free-text one, and does
// not take a quote of a footer, a transcript's list or the paste's own echo for
// one.
func TestFormUp(t *testing.T) {
	for _, file := range []string{"claude-question.txt", "claude-plan.txt", "claude-permission.txt", "tuios-claude-question.txt", "tuios-codex-approval.txt", "codex-trust.txt", "gemini-question.txt", "opencode-question.txt"} {
		if !FormUp(screenFixture(t, file), "") {
			t.Errorf("%s: no form", file)
		}
	}
	for _, file := range []string{"claude-idle.txt", "codex-idle.txt", "claude-streaming.txt", "common-input.txt"} {
		if FormUp(screenFixture(t, file), "") {
			t.Errorf("%s: a form", file)
		}
	}
	menu := gridOf("Which?", "> 1. Redis", "  2. SQLite", "Press enter to confirm or esc to cancel")
	for _, tc := range []struct {
		name   string
		g      vt.Grid
		pasted string
		want   bool
	}{
		{"menu with another footer", menu, "", true},
		{"menu after a paste", menu, "Use Postgres", true},
		{"footer quoted in output", gridOf("The menu says Enter to select", "> "), "", false},
		{"numbered prompt in the transcript", gridOf("❯ 1. fix a", "  2. fix b", "⏺ Done", "❯ "), "", false},
		{"numbered paste at the prompt", gridOf("❯ 1. do a", "  2. do b"), "1. do a\n2. do b", false},
		{"menu after a numbered paste", gridOf("❯ 1. Redis", "  2. SQLite"), "1. do a\n2. do b", true},
		{"menu whose option the paste names", gridOf("❯ 1. Postgres", "  2. SQLite"), "Use Postgres", true},
	} {
		if got := FormUp(tc.g, tc.pasted); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestChoosing(t *testing.T) {
	for file, want := range map[string]bool{
		"claude-question.txt":       true,
		"tuios-claude-question.txt": true,
		"opencode-question.txt":     true,
		"gemini-question.txt":       true,
		"common-input.txt":          false, // a free-text form
		"claude-idle.txt":           false,
		"codex-idle.txt":            false,
		"claude-streaming.txt":      false,
	} {
		if got := Choosing(screenFixture(t, file)); got != want {
			t.Errorf("%s: got %v, want %v", file, got, want)
		}
	}
}

// A hook's AskUserQuestion names its options when it asks one single-choice
// question; a later event takes them back.
func TestDeriveOptions(t *testing.T) {
	now := time.Now()
	b, err := os.ReadFile("testdata/claude_ask_user_question.json")
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := Derive(nil, model.ProviderClaude, b, now); !ok || a.Options != nil {
		t.Errorf("two questions: %+v", a)
	}
	one := `{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_input":{"questions":[` +
		`{"question":"Which?","multiSelect":false,"options":[{"label":" Redis  cache ","description":"d"},{"label":"SQLite"}]}]}}`
	a, ok := Derive(nil, model.ProviderClaude, []byte(one), now)
	if !ok || a.State != model.StateAwaitingInput || !slices.Equal(a.Options, []string{"Redis cache", "SQLite"}) || !slices.Equal(a.OptionNotes, []string{"d", ""}) || !a.Menu {
		t.Fatalf("one question: %+v", a)
	}
	a.Buttons = a.Options
	next, ok := Derive(&a, model.ProviderClaude, []byte(`{"hook_event_name":"PostToolUse","tool_name":"AskUserQuestion"}`), now)
	if !ok || next.Options != nil || next.OptionNotes != nil || next.Buttons != nil || next.Menu {
		t.Errorf("after the answer: %+v", next)
	}
	multi := `{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_input":{"questions":[` +
		`{"question":"Which?","multiSelect":true,"options":[{"label":"Redis"},{"label":"SQLite"}]}]}}`
	if a, _ := Derive(nil, model.ProviderClaude, []byte(multi), now); a.Options != nil {
		t.Errorf("multi-select: %+v", a)
	}
}

func gridOf(lines ...string) vt.Grid {
	e := vt.New(100, 30, nil)
	e.Write([]byte(strings.Join(lines, "\r\n")))
	return e.Snapshot()
}
