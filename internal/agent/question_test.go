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
	}{
		{"single", "Which color?", 2, false, true, false},
		{"multi", "Which toppings?", 3, true, true, true},
		{"several", "Which drink?", 2, false, true, false},
		{"second", "Which size?", 2, false, true, false},
		{"other", "Which size?", 2, false, true, false},
		{"review", "Review your answers", 2, false, false, false},
	} {
		t.Run(tc.file, func(t *testing.T) {
			g := screenFixture(t, "claude-ask-"+tc.file+".txt")
			q := ReadQuestion(model.ProviderClaude, g)
			if q == nil {
				t.Fatal("no question")
			}
			if !strings.HasPrefix(q.Prompt, tc.prompt) || len(q.Options) != tc.options || q.Multi != tc.multi || q.Other != tc.other || q.Submit != tc.submit {
				t.Fatalf("question: %+v", q)
			}
			if State(g) != model.StateAwaitingInput || ReadScreen(g) != ScreenForm {
				t.Fatal("question does not wait for input")
			}
			if !FormUp(g, "") {
				t.Fatal("question does not hold messages")
			}
			if ReadQuestion(model.ProviderCodex, g) != nil {
				t.Fatal("Claude form matched Codex")
			}
		})
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
	for _, f := range []string{"codex-ask-single.txt", "codex-ask-notes.txt", "codex-ask-several.txt"} {
		g := screenFixture(t, f)
		q := ReadQuestion(model.ProviderCodex, g)
		if q == nil || len(q.Options) != 2 || (q.Options[0].Label != "Tea" && q.Options[0].Label != "Small") || q.OtherIndex != 2 || !q.Other {
			t.Fatalf("%s: %+v", f, q)
		}
		if State(g) != model.StateAwaitingInput || ReadScreen(g) != ScreenForm {
			t.Fatal("Codex question did not wait")
		}
		if !FormUp(g, "") {
			t.Errorf("%s: question does not hold messages", f)
		}
	}
}

func TestQuestionTranscriptIsNotAForm(t *testing.T) {
	for _, tc := range []struct {
		file     string
		provider model.Provider
		prompt   string
	}{{"claude-ask-single.txt", model.ProviderClaude, "❯ New prompt"}, {"claude-ask-review.txt", model.ProviderClaude, "❯ New prompt"}, {"codex-ask-single.txt", model.ProviderCodex, "› New prompt"}} {
		g := screenFixture(t, tc.file)
		e := vt.New(100, 40, nil)
		e.Write([]byte(strings.ReplaceAll(ScreenText(g, 30)+"\n"+tc.prompt, "\n", "\r\n")))
		if ReadQuestion(tc.provider, e.Snapshot()) != nil {
			t.Fatalf("%s matched transcript above a prompt", tc.file)
		}
	}
}
