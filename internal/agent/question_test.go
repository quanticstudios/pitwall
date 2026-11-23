package agent

import (
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/vt"
)

func TestReadQuestion(t *testing.T) {
	for _, tc := range []struct {
		file, prompt         string
		options              int
		multi, other, submit bool
		tabs                 int
		review               bool
	}{
		{"single", "Which color?", 2, false, true, false, 1, false},
		{"multi", "Which toppings?", 3, true, true, true, 1, false},
		{"several", "Which drink?", 2, false, true, false, 2, false},
		{"second", "Which size?", 2, false, true, false, 2, false},
		{"other", "Which size?", 2, false, true, false, 2, false},
		{"review", "Review your answers", 2, false, false, false, 1, true},
		{"review-short", "Review your answers", 2, false, false, false, 2, true}, // 2.1.330 in 24 rows: no hint line
		{"3q-step1", "Which color?", 2, false, true, false, 3, false},
		{"3q-step2-multi", "Which toppings?", 3, true, true, true, 3, false},
		{"3q-step2-multi-checked", "Which toppings?", 3, true, true, true, 3, false},
		{"3q-step3", "Which size?", 2, false, true, false, 3, false},
		{"3q-step3-other", "Which size?", 2, false, true, false, 3, false},
		{"3q-review", "Review your answers", 2, false, false, false, 3, true},
		{"preview", "Which layout?", 2, false, false, false, 1, false},
		{"preview-opt2", "Which layout?", 2, false, false, false, 1, false},
	} {
		t.Run(tc.file, func(t *testing.T) {
			g := screenFixture(t, "claude-ask-"+tc.file+".txt")
			q := ReadQuestion(model.ProviderClaude, g)
			if q == nil {
				t.Fatal("no question")
			}
			if !strings.HasPrefix(q.Prompt, tc.prompt) || len(q.Options) != tc.options || q.Multi != tc.multi || q.Other != tc.other || q.Submit != tc.submit || len(q.Tabs) != tc.tabs || q.Review != tc.review {
				t.Fatalf("question: %+v", q)
			}
			if StateOf(model.ProviderClaude, g) != model.StateAwaitingInput || ReadScreenOf(model.ProviderClaude, g) != ScreenForm {
				t.Fatal("question does not wait for input")
			}
			if !FormUp(model.ProviderClaude, g, "") {
				t.Fatal("question does not hold messages")
			}
			if ReadQuestion(model.ProviderCodex, g) != nil {
				t.Fatal("Claude form matched Codex")
			}
			if ReadQuestion(model.ProviderAntigravity, g) != nil {
				t.Fatal("Claude form matched Antigravity")
			}
			if FormUp(model.ProviderAntigravity, g, "") {
				t.Fatal("Claude form matched Antigravity FormUp")
			}
		})
	}
	// Preview options keep clean labels without the preview box.
	if q := ReadQuestion(model.ProviderClaude, screenFixture(t, "claude-ask-preview.txt")); q == nil || q.Options[0].Label != "Split" || q.Options[1].Label != "Stack" || q.Cursor != 0 {
		t.Fatalf("preview: %+v", q)
	}
	if q := ReadQuestion(model.ProviderClaude, screenFixture(t, "claude-ask-preview-opt2.txt")); q == nil || q.Cursor != 1 {
		t.Fatalf("preview opt2: %+v", q)
	}
	// Multi-select checked state, Next button, and tab answered flags.
	if q := ReadQuestion(model.ProviderClaude, screenFixture(t, "claude-ask-3q-step2-multi-checked.txt")); q == nil ||
		!q.Options[0].Selected || q.Options[1].Selected || !q.Options[2].Selected || q.SubmitIndex != 4 || !q.Tabs[0].Answered || !q.Tabs[1].Answered || q.Tabs[2].Answered {
		t.Fatalf("3q step2 checked: %+v", q)
	}
	// Review page extracts each question's answer.
	if q := ReadQuestion(model.ProviderClaude, screenFixture(t, "claude-ask-3q-review.txt")); q == nil ||
		!q.Review || q.Index != 3 || q.ReviewAns["Which color?"] != "Blue" || q.ReviewAns["Which toppings?"] != "Cheese, Onion" || q.ReviewAns["Which size?"] != "Medium" {
		t.Fatalf("3q review: %+v", q)
	}
	for _, f := range []string{"claude-idle.txt", "claude-permission.txt", "claude-spinner.txt"} {
		if q := ReadQuestion(model.ProviderClaude, screenFixture(t, f)); q != nil {
			t.Fatalf("%s: %+v", f, q)
		}
	}
	e := vt.New(100, 30, nil)
	e.Write([]byte("❯ What is your plan?"))
	if ReadQuestion(model.ProviderClaude, e.Snapshot()) != nil {
		t.Fatal("matched plain text")
	}
}

func TestCodexQuestion(t *testing.T) {
	for _, tc := range []struct {
		file, first, stepPrompt string
		index, total            int
		canBack, canNext, draft bool
	}{
		{"codex-ask-single.txt", "Tea", "Which drink?", 0, 1, false, false, false},
		{"codex-ask-notes.txt", "Tea", "Which drink?", 0, 1, false, false, false},
		{"codex-ask-several.txt", "Small", "Which size?", 1, 2, true, false, false},
		{"codex-ask-3q-step1.txt", "Red (Recommended)", "Which color?", 0, 3, false, true, false},
		{"codex-ask-3q-step2.txt", "Tea (Recommended)", "Which drink?", 1, 3, true, true, false},
		{"codex-ask-3q-step3.txt", "Small (Recommended)", "Which size?", 2, 3, true, false, false},
		{"codex-ask-3q-step3-other.txt", "Small (Recommended)", "Which size?", 2, 3, true, false, true},
	} {
		t.Run(tc.file, func(t *testing.T) {
			g := screenFixture(t, tc.file)
			q := ReadQuestion(model.ProviderCodex, g)
			if q == nil || len(q.Options) != 2 || q.Options[0].Label != tc.first || q.StepPrompt != tc.stepPrompt ||
				q.Index != tc.index || q.Total != tc.total || q.CanBack != tc.canBack || q.CanNext != tc.canNext || q.Draft != tc.draft ||
				q.OtherIndex != 2 || !q.Other {
				t.Fatalf("%s: %+v", tc.file, q)
			}
			if StateOf(model.ProviderCodex, g) != model.StateAwaitingInput || ReadScreenOf(model.ProviderCodex, g) != ScreenForm {
				t.Fatal("Codex question did not wait")
			}
			if !FormUp(model.ProviderCodex, g, "") {
				t.Errorf("%s: question does not hold messages", tc.file)
			}
			if ReadQuestion(model.ProviderClaude, g) != nil {
				t.Errorf("%s: Codex form matched Claude", tc.file)
			}
			if ReadQuestion(model.ProviderAntigravity, g) != nil {
				t.Errorf("%s: Codex form matched Antigravity", tc.file)
			}
			if FormUp(model.ProviderAntigravity, g, "") {
				t.Errorf("%s: Codex form matched Antigravity FormUp", tc.file)
			}
		})
	}
}

func TestAntigravityQuestion(t *testing.T) {
	for _, tc := range []struct {
		file, prompt                  string
		index, total, options         int
		multi, other, submit, writeIn bool
		canBack, canNext              bool
	}{
		{"antigravity-question.txt", "Which color do you like?", 0, 1, 2, false, true, false, false, false, false},
		{"agy-ask-3q-step1.txt", "Which color?", 0, 3, 2, false, true, false, false, false, true},
		{"agy-ask-3q-step2-multi.txt", "Which toppings?", 1, 3, 3, true, true, true, false, true, true},
		{"agy-ask-3q-step2-multi-checked.txt", "Which toppings?", 1, 3, 3, true, true, true, false, true, true},
		{"agy-ask-3q-step3.txt", "Which size?", 2, 3, 2, false, true, false, false, true, false},
		{"agy-ask-3q-step3-other.txt", "Which toppings?", 1, 3, 3, true, true, false, true, false, false},
	} {
		t.Run(tc.file, func(t *testing.T) {
			g := screenFixture(t, tc.file)
			q := ReadQuestion(model.ProviderAntigravity, g)
			if q == nil {
				t.Fatal("no question")
			}
			if q.Prompt != tc.prompt || q.Index != tc.index || q.Total != tc.total || len(q.Options) != tc.options ||
				q.Multi != tc.multi || q.Other != tc.other || q.Submit != tc.submit || q.WriteIn != tc.writeIn ||
				q.CanBack != tc.canBack || q.CanNext != tc.canNext {
				t.Fatalf("%s: %+v", tc.file, q)
			}
			if StateOf(model.ProviderAntigravity, g) != model.StateAwaitingInput || ReadScreenOf(model.ProviderAntigravity, g) != ScreenForm {
				t.Fatalf("%s: did not wait", tc.file)
			}
			if !FormUp(model.ProviderAntigravity, g, "") {
				t.Errorf("%s: question does not hold messages", tc.file)
			}
			if ReadQuestion(model.ProviderClaude, g) != nil || ReadQuestion(model.ProviderCodex, g) != nil {
				t.Errorf("%s: Antigravity form matched another provider", tc.file)
			}
		})
	}
	if q := ReadQuestion(model.ProviderAntigravity, screenFixture(t, "agy-ask-3q-step1.txt")); q == nil || len(q.AllPrompts) != 3 || q.AllPrompts[0] != "Which color?" || q.AllPrompts[1] != "Which toppings?" || q.AllPrompts[2] != "Which size?" {
		t.Fatalf("agy all prompts: %+v", q)
	}
	if q := ReadQuestion(model.ProviderAntigravity, screenFixture(t, "agy-ask-3q-step2-multi-checked.txt")); q == nil || !q.Options[0].Selected || !q.Options[1].Selected || q.Options[2].Selected || q.Cursor != 1 {
		t.Fatalf("agy checked: %+v", q)
	}
	for _, f := range []string{"antigravity-idle.txt", "antigravity-approval.txt", "antigravity-working.txt", "antigravity-trust.txt", "antigravity-model-menu.txt", "antigravity-settings-menu.txt"} {
		if q := ReadQuestion(model.ProviderAntigravity, screenFixture(t, f)); q != nil {
			t.Fatalf("%s: %+v", f, q)
		}
	}
}

func TestQuestionTranscriptIsNotAForm(t *testing.T) {
	for _, tc := range []struct {
		file     string
		provider model.Provider
		prompt   string
	}{{"claude-ask-single.txt", model.ProviderClaude, "❯ New prompt"}, {"claude-ask-review.txt", model.ProviderClaude, "❯ New prompt"}, {"claude-ask-review-short.txt", model.ProviderClaude, "❯ New prompt"}, {"codex-ask-single.txt", model.ProviderCodex, "› New prompt"}} {
		g := screenFixture(t, tc.file)
		e := vt.New(100, 40, nil)
		e.Write([]byte(strings.ReplaceAll(ScreenText(g, 30)+"\n"+tc.prompt, "\n", "\r\n")))
		if ReadQuestion(tc.provider, e.Snapshot()) != nil {
			t.Fatalf("%s matched transcript above a prompt", tc.file)
		}
	}
}

// TestClaudeQuestionTallPreview: a preview box taller than the options
// starts on the question's header row, beside the prompt. The prompt is
// the question alone: its box rows once made the drive stop with
// `Claude Code is on "│ Codex chat tabs don't exist…"`.
func TestClaudeQuestionTallPreview(t *testing.T) {
	q := ReadQuestion(model.ProviderClaude, screenFixture(t, "claude-ask-preview-tall.txt"))
	if q == nil {
		t.Fatal("no question")
	}
	const want = "Which providers get a chat tab?"
	if q.Prompt != want || q.StepPrompt != want {
		t.Errorf("prompt %q, step prompt %q, want %q", q.Prompt, q.StepPrompt, want)
	}
	if len(q.Tabs) != 1 || q.Tabs[0].Label != "Chat tabs" {
		t.Errorf("tabs %+v", q.Tabs)
	}
	if len(q.Options) != 2 || q.Options[0].Label != "Claude and agy" || q.Options[1].Label != "Every provider" || q.Cursor != 0 {
		t.Errorf("options %+v, cursor %d", q.Options, q.Cursor)
	}
}
